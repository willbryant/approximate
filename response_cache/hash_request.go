package response_cache

import "bytes"
import "io"
import "io/ioutil"
import "crypto/sha256"
import "encoding/hex"
import "net/http"
import "sort"

func digestRequestAndBody(req *http.Request) ([]byte, error) {
	terminator := [...]byte{0}
	hasher := sha256.New()

	// hash the method
	if _, err := io.WriteString(hasher, req.Method); err != nil {
		return nil, err
	}
	if _, err := hasher.Write(terminator[:]); err != nil {
		return nil, err
	}

	// hash the URL
	if _, err := io.WriteString(hasher, req.URL.String()); err != nil {
		return nil, err
	}
	if _, err := hasher.Write(terminator[:]); err != nil {
		return nil, err
	}

	// hash the URL
	if _, err := io.WriteString(hasher, req.Proto); err != nil {
		return nil, err
	}
	if _, err := hasher.Write(terminator[:]); err != nil {
		return nil, err
	}

	// hash the request headers; explicitly sort the headers by name as maps have unordered iteration
	var keys []string
	for k := range req.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// write the field name
		if _, err := io.WriteString(hasher, k); err != nil {
			return nil, err
		}
		if _, err := hasher.Write(terminator[:]); err != nil {
			return nil, err
		}

		// write each value
		for _, v := range req.Header[k] {
			if _, err := io.WriteString(hasher, v); err != nil {
				return nil, err
			}
			if _, err := hasher.Write(terminator[:]); err != nil {
				return nil, err
			}
		}

		// extra separator
		if _, err := hasher.Write(terminator[:]); err != nil {
			return nil, err
		}
	}

	// read the request body, and substitute our copy for the original reader since we can't rewind that
	body := make([]byte, req.ContentLength)
	if _, err := io.ReadFull(req.Body, body); err != nil {
		return nil, err
	}

	req.Body.Close()
	req.Body = ioutil.NopCloser(bytes.NewReader(body))

	// hash the request body
	if _, err := hasher.Write(body); err != nil {
		return nil, err
	}
	if _, err := hasher.Write(terminator[:]); err != nil {
		return nil, err
	}

	return hasher.Sum(nil), nil
}

func digestToHash(digest []byte) string {
	return hex.EncodeToString(digest)
}

func HashRequestAndBody(req *http.Request) (string, error) {
	digest, err := digestRequestAndBody(req)
	if err != nil {
		return "", err
	}
	return digestToHash(digest), nil
}
