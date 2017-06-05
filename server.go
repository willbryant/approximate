package main

import "fmt"
import "net"
import "net/http"
import "os"
import "github.com/willbryant/proximate/response_cache"
import "github.com/willbryant/proximate/response_cache/httputil"
import "strings"

type proximateServer struct {
	Listener         net.Listener
	Tracker          *ConnectionTracker
	Closed           uint32
	Quiet            bool
	Cache            response_cache.ResponseCache
	GitPackUpstreams *response_cache.Upstreams
	DebPoolUpstreams *response_cache.Upstreams
	Proxy            *httputil.ReverseProxy
}

func ProximateServer(listener net.Listener, cacheDirectory string, gitPackUpstreams string, debPoolUpstreams string, quiet bool) proximateServer {
	return proximateServer{
		Listener:         listener,
		Tracker:          NewConnectionTracker(),
		Quiet:            quiet,
		Cache:            response_cache.NewDiskCache(cacheDirectory),
		GitPackUpstreams: response_cache.NewUpstreams(gitPackUpstreams),
		DebPoolUpstreams: response_cache.NewUpstreams(debPoolUpstreams),
		Proxy:            &httputil.ReverseProxy{Director: setProxyUserAgentDirector},
	}
}

func setProxyUserAgentDirector(req *http.Request) {
	if ua, ok := req.Header["User-Agent"]; ok {
		req.Header.Set("X-Proxy-Client-Agent", ua[0])
	}
	req.Header.Set("User-Agent", banner())
	fmt.Fprintf(os.Stdout, "proxying %s request to %s\n", req.Method, req.URL)
}

func cachableGitPackRequest(req *http.Request) bool {
	return req.ContentLength > 0 && req.ContentLength < 65536 && // arbitrary
		req.Method == "POST" &&
		req.Header.Get("Content-Type") == "application/x-git-upload-pack-request" &&
		req.Header.Get("Accept") == "application/x-git-upload-pack-result" &&
		req.Header.Get("Cache-Control") == "" &&
		req.Header.Get("Authorization") == ""
}

func cacheableDebPoolRequest(req *http.Request) bool {
	return req.Method == "GET" &&
		req.Header.Get("Cache-Control") == "" &&
		req.Header.Get("Authorization") == "" &&
		strings.HasSuffix(req.URL.Path, ".deb") &&
		strings.Contains(req.URL.Path, "/pool/")
}

func (server proximateServer) serveCacheableRequest(rw http.ResponseWriter, req *http.Request) {
	hash, err := response_cache.HashRequestAndBody(req)
	if err != nil {
		http.Error(rw, err.Error(), 401)
	}

	forwarded := false
	res, err := server.Cache.Get(hash, func() (*http.Response, error) {
		forwarded = true
		// TODO: never cancel, or at least only cancel if all clients abort?
		return server.Proxy.Forward(server.Proxy.CancelContext(rw, req), req)
	})

	server.Proxy.CopyResponse(rw, res)

	if err == response_cache.Uncacheable {
		fmt.Fprintf(os.Stdout, "%s request to %s was not actually cacheable, request hash %s\n", req.Method, req.URL, hash)
	} else if err != nil {
		fmt.Fprintf(os.Stdout, "error making %s request to %s, request hash %s, error %s\n", req.Method, req.URL, hash, err)
	} else if forwarded {
		fmt.Fprintf(os.Stdout, "%s request to %s saved to cache, request hash %s\n", req.Method, req.URL, hash)
	} else {
		fmt.Fprintf(os.Stdout, "%s request to %s served from cache, request hash %s\n", req.Method, req.URL, hash)
	}
}

func (server proximateServer) extractHostFromPrefix(req *http.Request) {
	req.URL.Scheme = "https"
	parts := strings.SplitN(req.URL.Path, "/", 3)
	req.URL.Host = parts[1]
	req.URL.Path = "/" + parts[2]
	req.Host = req.URL.Host
}

func (server proximateServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	logger := &responseLogger{w: w, req: req}

	// proxy-mode requests will have a full URL in the request path, with the Host populated;
	// we don't need to touch those.  get-mode requests we move the first bit of the path down
	// to be the host.
	if req.URL.Host == "" {
		server.extractHostFromPrefix(req)
	}

	if (cachableGitPackRequest(req) && server.GitPackUpstreams.UpstreamListed(req.URL)) ||
		(cacheableDebPoolRequest(req) && server.DebPoolUpstreams.UpstreamListed(req.URL)) {
		server.serveCacheableRequest(logger, req)
	} else {
		server.Proxy.ServeHTTP(logger, req)
	}

	if !server.Quiet && server.Active() {
		logger.ClfLog()
	}
}
