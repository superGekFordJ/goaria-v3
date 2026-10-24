package hls

import (
	"context"
	"crypto/aes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"goaria-v3/internal/surge/types"
)

// hlsKeyProbeBytes reads one byte past the AES-128 key length so an oversized
// body is detected instead of silently truncated to 16 bytes.
const hlsKeyProbeBytes = aes.BlockSize + 1

// hlsAttemptHooks is the seam a worker attempt implements so shared
// sub-request helpers can stamp telemetry and pass payload bytes through the
// task limiter without knowing session internals. nil is a valid no-op.
type hlsAttemptHooks interface {
	// noteActivity stamps the owning attempt's LastActivity.
	noteActivity()
	// noteStatus records the HTTP status observed on the wire (0 resets).
	noteStatus(status int)
	// throttle waits for n body bytes on the shared byte limiter. The
	// implementation owns the waiting flags around the block; key bytes pass
	// through it but never reach media progress accounting.
	throttle(ctx context.Context, n int64) error
}

func noteReadActivity(h hlsAttemptHooks) {
	if h != nil {
		h.noteActivity()
	}
}

func noteReadStatus(h hlsAttemptHooks, status int) {
	if h != nil {
		h.noteStatus(status)
	}
}

func throttleRead(ctx context.Context, h hlsAttemptHooks, n int64) error {
	if h == nil || n <= 0 {
		return nil
	}
	return h.throttle(ctx, n)
}

// hlsKeyCache is the session-scoped AES-128 key cache. Keys are indexed by
// the fully resolved HLSKeyInfo.URI (query included); the same URI shared by
// segments with different IVs hits one entry while each KeyIndex slot keeps
// its own position. Values are immutable [16]byte copies published under a
// short mutex — the lock never covers a network fetch, a cold miss may race
// into a duplicate GET (accepted over singleflight), and a failure is never
// cached as a negative entry.
type hlsKeyCache struct {
	mu    sync.Mutex
	items map[string][aes.BlockSize]byte
}

func newHLSKeyCache() *hlsKeyCache {
	return &hlsKeyCache{items: make(map[string][aes.BlockSize]byte)}
}

func (c *hlsKeyCache) lookup(uri string) ([aes.BlockSize]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.items[uri]
	return k, ok
}

func (c *hlsKeyCache) publish(uri string, key [aes.BlockSize]byte) {
	c.mu.Lock()
	c.items[uri] = key
	c.mu.Unlock()
}

// keyTable builds the private per-attempt key slot table consumed by
// decryptSegmentInPlace: len(keys) slots with only Keys[seg.KeyIndex-1]
// filled by a private copy of the cached material. KeyIndex 0 (plaintext)
// yields an all-nil table. The fetch callback runs outside the cache mutex.
func (c *hlsKeyCache) keyTable(
	seg *types.HLSSegmentState,
	keys []types.HLSKeyInfo,
	fetch func(uri string) ([aes.BlockSize]byte, error),
) ([][]byte, error) {
	if seg == nil {
		return nil, newSegmentDataError(-1, "missing segment state")
	}
	table := make([][]byte, len(keys))
	if seg.KeyIndex == 0 {
		return table, nil
	}
	if seg.KeyIndex < 0 || seg.KeyIndex > len(keys) {
		return nil, newSegmentDataError(seg.Index, "key index %d out of range", seg.KeyIndex)
	}
	info := keys[seg.KeyIndex-1]
	if info.URI == "" {
		return nil, newSegmentDataError(seg.Index, "key slot %d has no URI", seg.KeyIndex)
	}
	key, ok := c.lookup(info.URI)
	if !ok {
		fetched, err := fetch(info.URI)
		if err != nil {
			return nil, err
		}
		key = fetched
		c.publish(info.URI, key)
	}
	slot := make([]byte, aes.BlockSize)
	copy(slot, key[:])
	table[seg.KeyIndex-1] = slot
	return table, nil
}

// fetchHLSKey performs one gated GET for keyURI and enforces the exact-size
// contract: only HTTP 200 succeeds and the body must be exactly 16 bytes —
// the 17-byte probe rejects oversize, a short body fails, and a non-EOF read
// error after 16 bytes still fails. Size violations are *segmentDataError
// (retryable through the owning media attempt's budget — a key miss never
// nests its own retry loop); transport and HTTP failures return their raw
// classification carriers for the retry state machine.
func fetchHLSKey(
	ctx context.Context,
	client *http.Client,
	segIndex int,
	keyURI string,
	taskHeaders map[string]string,
	credentialBaseURL *url.URL,
	runCfg *types.RuntimeConfig,
	hooks hlsAttemptHooks,
) ([aes.BlockSize]byte, error) {
	var zero [aes.BlockSize]byte
	if client == nil {
		return zero, newHLSContractError("missing http client")
	}
	req, err := newHLSSubRequest(ctx, keyURI, taskHeaders, credentialBaseURL, runCfg, nil)
	if err != nil {
		return zero, err
	}
	noteReadStatus(hooks, 0)
	resp, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("hls key (segment %d): %w", segIndex, err)
	}
	noteReadActivity(hooks)
	noteReadStatus(hooks, resp.StatusCode)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return zero, fmt.Errorf("hls key (segment %d): unexpected status %d: %w",
				segIndex, resp.StatusCode, types.ErrHLSInvalidSegmentResponse)
		}
		return zero, &hlsStatusError{
			resource:   "key",
			index:      segIndex,
			status:     resp.StatusCode,
			retryAfter: resp.Header.Get("Retry-After"),
		}
	}

	var buf [hlsKeyProbeBytes]byte
	total := 0
	for {
		n, rerr := resp.Body.Read(buf[total:])
		if n > 0 {
			total += n
			noteReadActivity(hooks)
			if terr := throttleRead(ctx, hooks, int64(n)); terr != nil {
				return zero, terr
			}
			noteReadActivity(hooks)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return zero, fmt.Errorf("hls key (segment %d): body read: %w", segIndex, rerr)
		}
		if total >= len(buf) {
			break
		}
		if n == 0 {
			return zero, fmt.Errorf("hls key (segment %d): body read: %w", segIndex, io.ErrNoProgress)
		}
	}
	if total != aes.BlockSize {
		return zero, newSegmentDataError(segIndex, "key body is %d bytes, want %d", total, aes.BlockSize)
	}
	var key [aes.BlockSize]byte
	copy(key[:], buf[:aes.BlockSize])
	return key, nil
}
