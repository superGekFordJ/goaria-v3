package extension

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func withIdempotencyLimits(t *testing.T, ttl time.Duration, max int) {
	t.Helper()
	origTTL, origMax := idempotencyTTL, idempotencyMax
	idempotencyTTL = ttl
	idempotencyMax = max
	t.Cleanup(func() {
		idempotencyTTL = origTTL
		idempotencyMax = origMax
	})
}

func TestIdempotency_HitSamePayload(t *testing.T) {
	c := newIdempotencyCache()
	raw := json.RawMessage(`{"type":"extractor_resolve","request_id":"r1","x":1}`)
	digest := canonicalDigest(raw)
	st, _, _ := c.begin(1, MsgTypeExtractorResolve, "r1", digest)
	if st != idempMiss {
		t.Fatalf("first lookup want miss, got %d", st)
	}
	ack := []byte(`{"error_code":"unsupported"}`)
	c.complete(1, MsgTypeExtractorResolve, "r1", digest, ack)
	st, cached, _ := c.lookup(1, MsgTypeExtractorResolve, "r1", digest)
	if st != idempHit {
		t.Fatalf("replay want hit, got %d", st)
	}
	if string(cached) != string(ack) {
		t.Fatalf("cached ack mismatch: %s", cached)
	}
}

func TestIdempotency_ConflictDifferentPayload(t *testing.T) {
	c := newIdempotencyCache()
	d1 := canonicalDigest(json.RawMessage(`{"type":"extractor_resolve","request_id":"r1","x":1}`))
	d2 := canonicalDigest(json.RawMessage(`{"type":"extractor_resolve","request_id":"r1","x":2}`))
	if d1 == d2 {
		t.Fatal("digests should differ when payload differs")
	}
	st, _, _ := c.begin(1, MsgTypeExtractorResolve, "r1", d1)
	if st != idempMiss {
		t.Fatalf("first lookup want miss, got %d", st)
	}
	c.complete(1, MsgTypeExtractorResolve, "r1", d1, []byte(`ok`))
	st, _, _ = c.lookup(1, MsgTypeExtractorResolve, "r1", d2)
	if st != idempConflict {
		t.Fatalf("want conflict, got %d", st)
	}
}

func TestIdempotency_EvictOldestCompleted(t *testing.T) {
	withIdempotencyLimits(t, time.Minute, 2)
	c := newIdempotencyCache()
	for i, id := range []string{"a", "b"} {
		d := canonicalDigest(json.RawMessage(`{"n":` + string(rune('1'+i)) + `}`))
		c.begin(1, MsgTypeExtractorResolve, id, d)
		c.complete(1, MsgTypeExtractorResolve, id, d, []byte(id))
	}
	d3 := canonicalDigest(json.RawMessage(`{"n":3}`))
	c.begin(1, MsgTypeExtractorResolve, "c", d3)
	c.complete(1, MsgTypeExtractorResolve, "c", d3, []byte("c"))
	if c.len() != 2 {
		t.Fatalf("want 2 entries after eviction, got %d", c.len())
	}
	d1 := canonicalDigest(json.RawMessage(`{"n":1}`))
	st, _, _ := c.lookup(1, MsgTypeExtractorResolve, "a", d1)
	if st == idempHit {
		t.Fatal("oldest completed entry should have been evicted")
	}
}

func TestIdempotency_ExpiryIgnoresHit(t *testing.T) {
	withIdempotencyLimits(t, 20*time.Millisecond, 256)
	c := newIdempotencyCache()
	d := canonicalDigest(json.RawMessage(`{"x":1}`))
	c.begin(1, MsgTypeExtractorResolve, "r1", d)
	c.complete(1, MsgTypeExtractorResolve, "r1", d, []byte("ack"))
	time.Sleep(40 * time.Millisecond)
	st, _, _ := c.lookup(1, MsgTypeExtractorResolve, "r1", d)
	if st != idempMiss {
		t.Fatalf("expired hit should become miss, got %d", st)
	}
}

func TestIdempotency_InFlightCoalesce(t *testing.T) {
	c := newIdempotencyCache()
	d := canonicalDigest(json.RawMessage(`{"x":1}`))
	st, _, _ := c.begin(1, MsgTypeExtractorResolve, "r1", d)
	if st != idempMiss {
		t.Fatalf("owner want miss, got %d", st)
	}
	st, _, wait := c.lookup(1, MsgTypeExtractorResolve, "r1", d)
	if st != idempCoalesce {
		t.Fatalf("second want coalesce, got %d", st)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var got []byte
	go func() {
		defer wg.Done()
		got = <-wait
	}()
	ack := []byte("shared")
	c.complete(1, MsgTypeExtractorResolve, "r1", d, ack)
	wg.Wait()
	if string(got) != "shared" {
		t.Fatalf("waiter got %q", got)
	}
}

func TestIdempotency_AbandonFansOutAck(t *testing.T) {
	c := newIdempotencyCache()
	d := canonicalDigest(json.RawMessage(`{"x":1}`))
	if st, _, _ := c.begin(1, MsgTypeExtractorResolve, "r1", d); st != idempMiss {
		t.Fatalf("owner want miss, got %d", st)
	}
	st, _, wait := c.lookup(1, MsgTypeExtractorResolve, "r1", d)
	if st != idempCoalesce {
		t.Fatalf("second want coalesce, got %d", st)
	}
	busy := []byte(`{"error_code":"busy"}`)
	done := make(chan []byte, 1)
	go func() { done <- <-wait }()
	c.abandon(1, MsgTypeExtractorResolve, "r1", d, busy)
	select {
	case got := <-done:
		if string(got) != string(busy) {
			t.Fatalf("waiter got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter was dropped without a busy ack")
	}
}

func TestCanonicalDigest_IgnoresTypeAndRequestID(t *testing.T) {
	a := canonicalDigest(json.RawMessage(`{"type":"extractor_resolve","request_id":"a","k":1}`))
	b := canonicalDigest(json.RawMessage(`{"k":1,"request_id":"b","type":"extractor_resolve"}`))
	if a != b {
		t.Fatalf("type/request_id must not affect digest: %s vs %s", a, b)
	}
}

func TestIdempotency_ClearDropsHits(t *testing.T) {
	c := newIdempotencyCache()
	d := canonicalDigest(json.RawMessage(`{"x":1}`))
	c.begin(1, MsgTypeExtractorResolve, "r1", d)
	c.complete(1, MsgTypeExtractorResolve, "r1", d, []byte("ack"))
	c.clear()
	st, _, _ := c.lookup(1, MsgTypeExtractorResolve, "r1", d)
	if st != idempMiss {
		t.Fatalf("after clear want miss, got %d", st)
	}
}
