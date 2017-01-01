package response_cache

import "bytes"
import "io"
import "net/http"
import "os"
import "sync"

type memoryCacheEntry struct {
	status int
	header http.Header
	body   []byte
}

func (entry memoryCacheEntry) Status() int {
	return entry.status
}

func (entry memoryCacheEntry) Header() http.Header {
	return entry.header
}

func (entry memoryCacheEntry) Body() io.Reader {
	return bytes.NewReader(entry.body)
}

func (entry memoryCacheEntry) Close() {
	// do nothing
}

func (entry memoryCacheEntry) WriteTo(w http.ResponseWriter) {
	WriteEntryTo(entry, w)
}

type memoryCache struct {
	sync.RWMutex
	Entries map[string]memoryCacheEntry
}

func NewMemoryCache() ResponseCache {
	return memoryCache{
		Entries: make(map[string]memoryCacheEntry),
	}
}

func (cache memoryCache) Get(key string) (Entry, error) {
	cache.RLock()
	defer cache.RUnlock()
	entry, ok := cache.Entries[key]
	if ok {
		return entry, nil
	}
	return nil, os.ErrNotExist
}

type memoryCacheBodyWriter struct {
	cache memoryCache
	key   string
	entry *memoryCacheEntry
}

func (writer memoryCacheBodyWriter) Write(data []byte) (int, error) {
	writer.entry.body = append(writer.entry.body, data...)
	return len(data), nil
}

func (writer memoryCacheBodyWriter) Finish() error {
	writer.cache.RLock()
	defer writer.cache.RUnlock()
	writer.cache.Entries[writer.key] = *writer.entry
	return nil
}

func (writer memoryCacheBodyWriter) Abort() error {
	// do nothing
	return nil
}

func (cache memoryCache) BeginWrite(key string, status int, header http.Header) (CacheBodyWriter, error) {
	entry := memoryCacheEntry{
		status: status,
		header: make(http.Header),
	}
	CopyHeader(entry.Header(), header)

	return memoryCacheBodyWriter{
		cache: cache,
		key:   key,
		entry: &entry,
	}, nil
}