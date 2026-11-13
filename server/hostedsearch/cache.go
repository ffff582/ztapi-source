package hostedsearch

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type PageStore interface {
	Get(string) (Result, bool)
	Put(string, Result)
	RememberSource(Source)
	Sources() []Source
}

type cachedPage struct {
	result            Result
	identity          string
	expires, accessed time.Time
	bytes             int
}

// This bounded memory cache is scoped to the authenticated key identity. It
// retains fetched pages across Codex's local-tool turns, never on disk.
type PageCache struct {
	mu             sync.Mutex
	entries        map[string]cachedPage
	bytes, maximum int
	ttl            time.Duration
	now            func() time.Time
}

func NewPageCache(maximum int, ttl time.Duration) *PageCache {
	if maximum < 1 || maximum > 16*1024*1024 || ttl <= 0 || ttl > 15*time.Minute {
		panic("invalid hosted page cache limits")
	}
	return &PageCache{entries: map[string]cachedPage{}, maximum: maximum, ttl: ttl, now: time.Now}
}

type pageCacheView struct {
	cache    *PageCache
	identity string
}

func (c *PageCache) ForIdentity(identity string) PageStore {
	if identity == "" {
		panic("hosted page cache requires authenticated identity")
	}
	hash := sha256.Sum256([]byte(identity))
	return pageCacheView{cache: c, identity: hex.EncodeToString(hash[:])}
}
func (v pageCacheView) key(url string) string {
	hash := sha256.Sum256([]byte(v.identity + "\x00" + url))
	return hex.EncodeToString(hash[:])
}
func (c *PageCache) expire(now time.Time) {
	for key, entry := range c.entries {
		if !entry.expires.After(now) {
			c.bytes -= entry.bytes
			delete(c.entries, key)
		}
	}
}
func (v pageCacheView) Get(url string) (Result, bool) {
	c := v.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.expire(now)
	key := v.key(url)
	entry, ok := c.entries[key]
	if !ok {
		return Result{}, false
	}
	entry.accessed = now
	c.entries[key] = entry
	return entry.result, true
}
func (v pageCacheView) Put(url string, result Result) {
	if result.Outcome != OutcomeSucceeded || result.HTTPStatus != 200 || result.Content == "" || ValidatePublicURL(url) != nil {
		return
	}
	result.Sources = nil
	v.store(url, result, len(url)+len(result.Title)+len(result.Content)+256)
}

func (v pageCacheView) RememberSource(source Source) {
	if ValidatePublicURL(source.URL) != nil {
		return
	}
	source.Summary = ""
	source.Content = ""
	v.store("source:"+source.URL, Result{Sources: []Source{source}}, len(source.Title)+len(source.URL)+256)
}

func (v pageCacheView) Sources() []Source {
	c := v.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(c.now())
	sources := []Source{}
	for _, entry := range c.entries {
		if entry.identity == v.identity && len(entry.result.Sources) == 1 {
			sources = append(sources, entry.result.Sources[0])
		}
	}
	return sources
}

func (v pageCacheView) store(url string, result Result, count int) {
	c := v.cache
	if count > c.maximum || len(result.Content) > 256*1024 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.expire(now)
	key := v.key(url)
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.bytes
		delete(c.entries, key)
	}
	for c.bytes+count > c.maximum || len(c.entries) >= 256 {
		oldest := ""
		var accessed time.Time
		for k, e := range c.entries {
			if oldest == "" || e.accessed.Before(accessed) {
				oldest = k
				accessed = e.accessed
			}
		}
		if oldest == "" {
			return
		}
		c.bytes -= c.entries[oldest].bytes
		delete(c.entries, oldest)
	}
	c.entries[key] = cachedPage{result: result, identity: v.identity, bytes: count, expires: now.Add(c.ttl), accessed: now}
	c.bytes += count
}
