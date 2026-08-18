package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

var (
	idempotencyTTL = 60 * time.Second
	idempotencyMax = 256
)

type idempStatus int

const (
	idempMiss idempStatus = iota
	idempHit
	idempCoalesce
	idempConflict
)

type idempEntry struct {
	digest    string
	ack       []byte
	completed bool
	expiresAt time.Time
	waiters   []chan []byte
}

type idempotencyCache struct {
	mu        sync.Mutex
	entries   map[string]*idempEntry
	completed []string
}

func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{entries: make(map[string]*idempEntry)}
}

func idempKey(gen uint64, msgType, requestID string) string {
	return strconv.FormatUint(gen, 10) + "\x00" + msgType + "\x00" + requestID
}

func canonicalDigest(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	delete(m, "type")
	delete(m, "request_id")
	b, err := json.Marshal(m)
	if err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (c *idempotencyCache) lookupOrBegin(gen uint64, msgType, requestID, digest string) (idempStatus, []byte, <-chan []byte) {
	key := idempKey(gen, msgType, requestID)
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		if e.completed && time.Now().After(e.expiresAt) {
			c.removeLocked(key)
		} else {
			if e.digest != digest {
				return idempConflict, nil, nil
			}
			if e.completed {
				return idempHit, e.ack, nil
			}
			ch := make(chan []byte, 1)
			e.waiters = append(e.waiters, ch)
			return idempCoalesce, nil, ch
		}
	}

	c.evictIfNeededLocked()
	c.entries[key] = &idempEntry{digest: digest}
	return idempMiss, nil, nil
}

func (c *idempotencyCache) abandon(gen uint64, msgType, requestID, digest string) {
	key := idempKey(gen, msgType, requestID)
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok || e.completed || e.digest != digest {
		c.mu.Unlock()
		return
	}
	waiters := e.waiters
	delete(c.entries, key)
	c.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (c *idempotencyCache) complete(gen uint64, msgType, requestID, digest string, ack []byte) {
	key := idempKey(gen, msgType, requestID)
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok || e.digest != digest || e.completed {
		c.mu.Unlock()
		return
	}
	e.ack = ack
	e.completed = true
	e.expiresAt = time.Now().Add(idempotencyTTL)
	waiters := e.waiters
	e.waiters = nil
	c.completed = append(c.completed, key)
	c.mu.Unlock()
	for _, ch := range waiters {
		ch <- ack
		close(ch)
	}
}

func (c *idempotencyCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		for _, ch := range e.waiters {
			close(ch)
		}
	}
	c.entries = make(map[string]*idempEntry)
	c.completed = nil
}

func (c *idempotencyCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *idempotencyCache) evictIfNeededLocked() {
	for len(c.entries) >= idempotencyMax && len(c.completed) > 0 {
		old := c.completed[0]
		c.completed = c.completed[1:]
		if e, ok := c.entries[old]; ok && e.completed {
			delete(c.entries, old)
		}
	}
}

func (c *idempotencyCache) removeLocked(key string) {
	delete(c.entries, key)
	out := c.completed[:0]
	for _, k := range c.completed {
		if k != key {
			out = append(out, k)
		}
	}
	c.completed = out
}
