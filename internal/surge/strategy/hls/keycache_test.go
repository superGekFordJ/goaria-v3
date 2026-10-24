package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// countHooks records hook invocations so tests can assert activity stamps,
// status notes and limiter bytes without a real session.
type countHooks struct {
	activity   atomic.Int64
	statuses   atomic.Int64
	lastStatus atomic.Int64
	throttled  atomic.Int64
	throttleN  atomic.Int64
	blockCh    chan struct{}
}

func (h *countHooks) noteActivity()    { h.activity.Add(1) }
func (h *countHooks) noteStatus(s int) { h.statuses.Add(1); h.lastStatus.Store(int64(s)) }
func (h *countHooks) throttle(ctx context.Context, n int64) error {
	h.throttled.Add(1)
	h.throttleN.Add(n)
	if h.blockCh != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.blockCh:
		}
	}
	return nil
}

func keyTestServer(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchTestKey(t *testing.T, srv *httptest.Server, uri string, hooks hlsAttemptHooks) ([16]byte, error) {
	t.Helper()
	base := mustParseURLT(t, srv.URL+"/media.m3u8")
	client, release := newHLSClient(srv.Client(), nil, base)
	if release != nil {
		defer release()
	}
	return fetchHLSKey(context.Background(), client, 7, uri, nil, base, nil, hooks)
}

func TestHLSKeyFetchExact16(t *testing.T) {
	want := [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	srv := keyTestServer(t, want[:], 200)
	hooks := &countHooks{}
	got, err := fetchTestKey(t, srv, srv.URL+"/k.bin", hooks)
	if err != nil {
		t.Fatalf("fetchHLSKey = %v", err)
	}
	if got != want {
		t.Fatalf("key = %x, want %x", got, want)
	}
	if hooks.throttleN.Load() != 16 {
		t.Fatalf("throttled bytes = %d, want 16", hooks.throttleN.Load())
	}
	if hooks.lastStatus.Load() != 200 {
		t.Fatalf("lastStatus = %d, want 200", hooks.lastStatus.Load())
	}
	if hooks.activity.Load() == 0 {
		t.Fatal("no activity stamps recorded")
	}
}

func TestHLSKeyFetchSizeViolations(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"Short15", make([]byte, 15)},
		{"Empty", nil},
		{"Long17", make([]byte, 17)},
		{"Long1024", make([]byte, 1024)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := keyTestServer(t, tc.body, 200)
			_, err := fetchTestKey(t, srv, srv.URL+"/k.bin", &countHooks{})
			if _, ok := errors.AsType[*segmentDataError](err); !ok {
				t.Fatalf("fetchHLSKey err = %v (%T), want *segmentDataError", err, err)
			}
			if types.IsPermanentHLSError(err) {
				t.Fatalf("err %v must not match permanent HLS sentinel", err)
			}
			if strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("error leaks URL: %v", err)
			}
		})
	}
}

func TestHLSKeyFetchStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantStatus bool // *hlsStatusError vs ErrHLSInvalidSegmentResponse
	}{
		{"NotFound", 404, true},
		{"Forbidden", 403, true},
		{"TooMany", 429, true},
		{"ServerErr", 500, true},
		{"Unexpected2xx", 204, false},
		{"Unhandled3xx", 304, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := keyTestServer(t, nil, tc.status)
			_, err := fetchTestKey(t, srv, srv.URL+"/k.bin", &countHooks{})
			if tc.wantStatus {
				var se *hlsStatusError
				if !errors.As(err, &se) {
					t.Fatalf("err = %v (%T), want *hlsStatusError", err, err)
				}
				if se.status != tc.status || se.resource != "key" || se.index != 7 {
					t.Fatalf("status error = %+v", se)
				}
			} else if !errors.Is(err, types.ErrHLSInvalidSegmentResponse) {
				t.Fatalf("err = %v, want ErrHLSInvalidSegmentResponse", err)
			}
			if strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("error leaks URL: %v", err)
			}
		})
	}
}

func TestHLSKeyFetchRetryAfterCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := fetchTestKey(t, srv, srv.URL+"/k.bin", &countHooks{})
	var se *hlsStatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *hlsStatusError", err, err)
	}
	if se.retryAfter != "3" {
		t.Fatalf("retryAfter = %q, want 3", se.retryAfter)
	}
}

func TestHLSKeyFetchMidBodyError(t *testing.T) {
	// A body that serves 16 bytes then fails non-EOF must not succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 16))
		// Close abruptly — the promised body never finishes.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()
	_, err := fetchTestKey(t, srv, srv.URL+"/k.bin", &countHooks{})
	if err == nil {
		t.Fatal("fetchHLSKey succeeded on truncated body, want failure")
	}
}

func TestHLSKeyFetchContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until client ctx fires
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	base := mustParseURLT(t, srv.URL+"/media.m3u8")
	client, _ := newHLSClient(srv.Client(), nil, base)
	_, err := fetchHLSKey(ctx, client, 3, srv.URL+"/k.bin", nil, base, nil, &countHooks{})
	if err == nil {
		t.Fatal("fetchHLSKey succeeded under cancelled ctx")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded identity", err)
	}
}

func TestHLSKeyCacheMissFailThenRefetch(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(make([]byte, 16))
	}))
	defer srv.Close()

	kc := newHLSKeyCache()
	seg := &types.HLSSegmentState{Index: 4, URI: "s.ts", KeyIndex: 1}
	keys := []types.HLSKeyInfo{{Method: "AES-128", URI: srv.URL + "/k.bin"}}
	fetch := func(uri string) ([16]byte, error) {
		return fetchTestKey(t, srv, uri, &countHooks{})
	}

	if _, err := kc.keyTable(seg, keys, fetch); err == nil {
		t.Fatal("first fetch should fail")
	}
	// A failed fetch is never cached: the retry must hit the network again.
	table, err := kc.keyTable(seg, keys, fetch)
	if err != nil {
		t.Fatalf("refetch = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("GET count = %d, want 2 (no negative cache)", calls.Load())
	}
	if len(table) != 1 || len(table[0]) != 16 {
		t.Fatalf("table = %v slots", table)
	}
	// Third call hits the published entry — no further GET.
	if _, err := kc.keyTable(seg, keys, fetch); err != nil {
		t.Fatalf("cached fetch = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("GET count = %d after cache hit, want 2", calls.Load())
	}
}

func TestHLSKeyCacheIndexAndQueryIsolation(t *testing.T) {
	kc := newHLSKeyCache()
	seg := &types.HLSSegmentState{Index: 0, KeyIndex: 2}
	keys := []types.HLSKeyInfo{
		{Method: "AES-128", URI: "https://x.example/k.bin?v=1"},
		{Method: "AES-128", URI: "https://x.example/k.bin?v=2"},
	}
	var fetched []string
	fetch := func(uri string) ([16]byte, error) {
		fetched = append(fetched, uri)
		return [16]byte{9}, nil
	}
	table, err := kc.keyTable(seg, keys, fetch)
	if err != nil {
		t.Fatalf("keyTable = %v", err)
	}
	if len(table) != 2 || table[0] != nil || len(table[1]) != 16 || table[1][0] != 9 {
		t.Fatalf("table = %v", table)
	}
	if len(fetched) != 1 || fetched[0] != keys[1].URI {
		t.Fatalf("fetched = %v, want only slot-2 URI", fetched)
	}

	// Different query strings are different keys — slot 1 must miss.
	seg2 := &types.HLSSegmentState{Index: 1, KeyIndex: 1}
	if _, err := kc.keyTable(seg2, keys, fetch); err != nil {
		t.Fatalf("keyTable slot1 = %v", err)
	}
	if len(fetched) != 2 || fetched[1] != keys[0].URI {
		t.Fatalf("fetched = %v, query variant must not share entry", fetched)
	}
}

func TestHLSKeyCacheSameURIDifferentIVShares(t *testing.T) {
	kc := newHLSKeyCache()
	uri := "https://x.example/rotate.bin"
	keys := []types.HLSKeyInfo{{Method: "AES-128", URI: uri}}
	var calls int
	fetch := func(string) ([16]byte, error) {
		calls++
		return [16]byte{5}, nil
	}
	// Two segments, same key URI, different IVs — one fetch, two hits.
	for _, iv := range []string{"", "00", "ff"} {
		seg := &types.HLSSegmentState{Index: 1, KeyIndex: 1, IVHex: iv}
		table, err := kc.keyTable(seg, keys, fetch)
		if err != nil {
			t.Fatalf("keyTable iv=%q = %v", iv, err)
		}
		if len(table[0]) != 16 || table[0][0] != 5 {
			t.Fatalf("slot material wrong: %x", table[0])
		}
	}
	if calls != 1 {
		t.Fatalf("GET count = %d, want 1 for shared URI", calls)
	}
}

func TestHLSKeyCachePlaintextAndBadIndex(t *testing.T) {
	kc := newHLSKeyCache()
	fetch := func(string) ([16]byte, error) {
		t.Fatal("fetch must not run for plaintext/invalid index")
		return [16]byte{}, nil
	}
	table, err := kc.keyTable(&types.HLSSegmentState{Index: 0, KeyIndex: 0}, nil, fetch)
	if err != nil || len(table) != 0 {
		t.Fatalf("plaintext table = %v, err %v", table, err)
	}
	for _, seg := range []*types.HLSSegmentState{
		{Index: 2, KeyIndex: -1},
		{Index: 3, KeyIndex: 5},
	} {
		if _, err := kc.keyTable(seg, []types.HLSKeyInfo{{URI: "u"}}, fetch); err == nil {
			t.Fatalf("KeyIndex %d accepted, want data error", seg.KeyIndex)
		} else {
			if _, ok := errors.AsType[*segmentDataError](err); !ok {
				t.Fatalf("err type %T, want *segmentDataError", err)
			}
		}
	}
	// Empty URI slot is a data-plane failure, never a fetch.
	seg := &types.HLSSegmentState{Index: 4, KeyIndex: 1}
	if _, err := kc.keyTable(seg, []types.HLSKeyInfo{{URI: ""}}, fetch); err == nil {
		t.Fatal("empty key URI accepted")
	}
}

func TestHLSKeyCacheConcurrentRaces(t *testing.T) {
	var calls atomic.Int64
	kc := newHLSKeyCache()
	uri := "https://x.example/k.bin"
	seg := &types.HLSSegmentState{Index: 0, KeyIndex: 1}
	keys := []types.HLSKeyInfo{{Method: "AES-128", URI: uri}}
	fetch := func(string) ([16]byte, error) {
		calls.Add(1)
		return [16]byte{1, 2, 3}, nil
	}

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			table, err := kc.keyTable(seg, keys, fetch)
			if err != nil {
				errs <- err
				return
			}
			if len(table) != 1 || table[0][0] != 1 {
				errs <- fmt.Errorf("bad slot: %v", table)
			}
			// Mutating the private slot must not corrupt the cache.
			table[0][0] = 0xff
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Cold-miss duplicate GETs are permitted; the published value is stable.
	if calls.Load() < 1 {
		t.Fatalf("GET count = %d, want >=1", calls.Load())
	}
	table, err := kc.keyTable(seg, keys, fetch)
	if err != nil || table[0][0] != 1 {
		t.Fatalf("post-race lookup = %v err %v", table, err)
	}
}

// encryptTestCBC pads pt with PKCS#7 and CBC-encrypts it — the padded
// counterpart of decrypt_test.go's raw encryptRaw.
func encryptTestCBC(t *testing.T, key, iv, pt []byte) []byte {
	t.Helper()
	pad := 16 - len(pt)%16
	padded := append(append([]byte(nil), pt...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	return encryptRaw(t, key, iv, padded)
}

// TestHLSKeyTableFeedsDecrypt proves the private table actually drives 3a
// decryption: encrypted segment + fetched key round-trips to plaintext.
func TestHLSKeyTableFeedsDecrypt(t *testing.T) {
	key := [16]byte{
		0x2b, 0x7e, 0x15, 0x16, 0x28, 0xae, 0xd2, 0xa6,
		0xab, 0xf7, 0x15, 0x88, 0x09, 0xcf, 0x4f, 0x3c,
	}
	srv := keyTestServer(t, key[:], 200)
	kc := newHLSKeyCache()
	seg := &types.HLSSegmentState{Index: 9, Sequence: 0, KeyIndex: 1}
	keys := []types.HLSKeyInfo{{Method: "AES-128", URI: srv.URL + "/k.bin"}}
	fetch := func(uri string) ([16]byte, error) {
		return fetchTestKey(t, srv, uri, &countHooks{})
	}
	table, err := kc.keyTable(seg, keys, fetch)
	if err != nil {
		t.Fatalf("keyTable = %v", err)
	}
	// Encrypt a known plaintext with the same implicit IV (seq 0), then run
	// the real decryptor against the fetched table.
	plain := bytes.Repeat([]byte("segment-payload-"), 8)[:120]
	ct := encryptTestCBC(t, key[:], make([]byte, 16), plain)
	buf := append([]byte(nil), ct...)
	out, pad, err := decryptSegmentInPlace(buf, seg, table)
	if err != nil {
		t.Fatalf("decryptSegmentInPlace = %v", err)
	}
	if pad == 0 || !bytes.Equal(out, plain) {
		t.Fatalf("decrypt out len=%d pad=%d, want %d plaintext bytes", len(out), pad, len(plain))
	}
}
