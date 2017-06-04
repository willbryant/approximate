package response_cache

import "io"
import "github.com/tinylib/msgp/msgp"
import "net/http"
import "sync"
import "os"

type diskCache struct {
	cacheDirectory        string
	progressTrackersMutex sync.Mutex
	progressTrackers      map[string]chan func() (*http.Response, error)
}

func NewDiskCache(cacheDirectory string) ResponseCache {
	return &diskCache{
		cacheDirectory:   cacheDirectory,
		progressTrackers: make(map[string]chan func() (*http.Response, error)),
	}
}

func (cache *diskCache) cacheEntryPath(key string) string {
	return cache.cacheDirectory + "/" + key
}

func (cache *diskCache) Get(key string, miss func() (*http.Response, error)) (*http.Response, error) {
	path := cache.cacheEntryPath(key)

	for {
		// optimistically try to open the entry in the cache, so we don't need to mutex
		file, err := os.Open(path)

		if err == nil {
			return cache.cachedResponse(file)
		}

		if !os.IsNotExist(err) {
			// TODO: log but otherwise ignore other file open errors
			return nil, err
		}

		// cache miss
		readFunction, ok := <-cache.channelFor(path, miss)
		if ok {
			res, err := readFunction()
			if err == nil {
				err = os.ErrNotExist // indicates a cache miss
			}
			return res, err
		}
		// we missed the forwarding function's execution, which is fine because now it will have stored into the cache.  or
		// the other possibility is that the response turned out to be uncacheable (eg. an unexpected 500).  in both cases,
		// loop around and try again - having to loop is the price we pay for being optimistic and avoiding the mutex above.
	}
}

func (cache *diskCache) channelFor(path string, miss func() (*http.Response, error)) chan func() (*http.Response, error) {
	cache.progressTrackersMutex.Lock()
	defer cache.progressTrackersMutex.Unlock()
	ch := cache.progressTrackers[path]
	if ch == nil {
		ch = make(chan func() (*http.Response, error))
		cache.progressTrackers[path] = ch
		go cache.populate(path, ch, miss)
	}
	return ch
}

func (cache *diskCache) clearProgressTrackerFor(path string) {
	cache.progressTrackersMutex.Lock()
	defer cache.progressTrackersMutex.Unlock()
	delete(cache.progressTrackers, path)
}

func (cache *diskCache) populate(path string, ch chan func() (*http.Response, error), miss func() (*http.Response, error)) {
	defer close(ch)
	defer cache.clearProgressTrackerFor(path)

	// forward the request upstream
	res, err := miss()

	// if uncacheable, send the response back to just 1 waiter, and close the channel to let others know there's no point waiting
	if !CacheableResponse(res.StatusCode, res.Header) {
		ch <- func() (*http.Response, error) { return res, Uncacheable }
		return
	}

	// open a temporary file to write to
	file, err := os.OpenFile(path+".temp", os.O_RDWR|os.O_CREATE|os.O_TRUNC, os.ModePerm)
	sf := NewSharedFile(file)

	if err == nil {
		err = cache.writeHeader(sf, res)
	}

	// if we can't open that file or write the header to it, handle it like we did above for uncacheable files to minimize client suffering
	if err != nil {
		ch <- func() (*http.Response, error) { return res, err }
		return
	}

	done := make(chan interface{})

	// copy the response body to the cache
	go func() {
		_, err := io.Copy(sf, res.Body)
		if err == nil {
			// publish the result in the cache
			sf.Sync()
			err = os.Rename(file.Name(), path)
		}
		if err != nil {
			sf.Abort(err)
		} else {
			sf.Close()
		}
		close(done)
	}()

	readFunction := func() (*http.Response, error) {
		reader, err := sf.SpawnReader()
		if err != nil {
			return nil, err
		}
		return cache.cachedResponse(reader)
	}

	for {
		select {
		case ch <- readFunction:
			break

		case <-done:
			return
		}
	}
}

func (cache *diskCache) writeHeader(w io.Writer, res *http.Response) error {
	diskCacheHeader := DiskCacheHeader{
		Version:       1,
		StatusCode:    res.StatusCode,
		Header:        res.Header,
		ContentLength: res.ContentLength,
	}

	streamer := msgp.NewWriter(w)

	if err := diskCacheHeader.EncodeMsg(streamer); err != nil {
		return err
	}

	if err := streamer.Flush(); err != nil {
		return err
	}

	return nil
}

func (cache *diskCache) cachedResponse(r io.ReadCloser) (*http.Response, error) {
	// wrap the file reader in a msgpack reader so we can decode the header
	streamer := msgp.NewReader(r)

	var diskCacheHeader DiskCacheHeader
	if err := diskCacheHeader.DecodeMsg(streamer); err != nil {
		return nil, err
	}

	// although the remainder of the file is just the raw body and is not msgpacked, we have to
	// keep using the msgpack reader object as it may have some bytes in its buffer, so we return
	// reader rather than r as the Body stream.  we have to combine that back with the io.Closer
	// interface from r, because msgp.NewReader returns an io.Reader rather than io.ReadCloser.
	return &http.Response{
		StatusCode:    diskCacheHeader.StatusCode,
		Header:        diskCacheHeader.Header,
		ContentLength: diskCacheHeader.ContentLength,
		Body:          ReaderAndCloser(streamer, r),
	}, nil
}

func (cache *diskCache) Clear() error {
	return clearDirectory(cache.cacheDirectory)
}

func clearDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()

	for {
		filenames, err := dir.Readdirnames(1000)

		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		for _, filename := range filenames {
			// ignore hidden files in the cache root directory
			if filename[0] != '.' {
				err := os.RemoveAll(directory + string(os.PathSeparator) + filename)

				if err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
}
