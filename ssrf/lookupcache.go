package ssrf

import (
	"context"
	"net"
	"sync"
	"time"
)

const (
	// lookupCacheTTL is how long a name's answer, or its failure, is reused. A
	// download makes several requests to one host (token, manifest, each blob,
	// each redirect hop) and a runner without DNS would otherwise wait out the
	// lookup timeout for every one.
	lookupCacheTTL = 30 * time.Second
	// lookupErrorTTL is how long a failure other than "no such host" is reused. It
	// is short so that one passing resolver hiccup does not refuse a host for long.
	lookupErrorTTL = 5 * time.Second
	// lookupCacheMax bounds the cache; it is cleared when full.
	lookupCacheMax = 1024
)

// lookupCache holds name lookups for CheckProxiedHost. It stores the answer, not
// the verdict on it, so it is shared safely between guards with different
// settings. Concurrent lookups of one name share a single call.
type lookupCache struct {
	mu       sync.Mutex
	now      func() time.Time
	entries  map[string]lookupResult
	inflight map[string]*lookupCall
}

type lookupResult struct {
	addrs   []net.IPAddr
	err     error
	expires time.Time
}

type lookupCall struct {
	done chan struct{}
	res  lookupResult
}

var sharedLookups = &lookupCache{now: time.Now}

// reset empties the cache; tests use it to isolate stubbed lookups.
func (c *lookupCache) reset() {
	c.mu.Lock()
	c.entries, c.inflight = nil, nil
	c.mu.Unlock()
}

// lookup resolves host through lookupIPAddr, bounded by timeout. The shared call
// runs on its own context so one caller giving up does not fail the others; each
// caller still returns as soon as its own ctx is done.
func (c *lookupCache) lookup(ctx context.Context, host string, timeout time.Duration) ([]net.IPAddr, error) {
	c.mu.Lock()
	if r, ok := c.entries[host]; ok && c.now().Before(r.expires) {
		c.mu.Unlock()
		return r.addrs, r.err
	}
	call, ok := c.inflight[host]
	if !ok {
		call = &lookupCall{done: make(chan struct{})}
		if c.inflight == nil {
			c.inflight = map[string]*lookupCall{}
		}
		c.inflight[host] = call
		go c.run(lookupIPAddr, host, timeout, call)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.res.addrs, call.res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *lookupCache) run(lookup func(context.Context, string) ([]net.IPAddr, error), host string, timeout time.Duration, call *lookupCall) {
	// Waiters block on done, so it must close even if the lookup panics.
	defer close(call.done)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	addrs, err := lookup(ctx, host)

	ttl := lookupCacheTTL
	if err != nil && !isNotFound(err) {
		ttl = lookupErrorTTL
	}
	call.res = lookupResult{addrs: addrs, err: err, expires: c.now().Add(ttl)}
	c.mu.Lock()
	if c.entries == nil || len(c.entries) >= lookupCacheMax {
		c.entries = map[string]lookupResult{}
	}
	c.entries[host] = call.res
	delete(c.inflight, host)
	c.mu.Unlock()
}
