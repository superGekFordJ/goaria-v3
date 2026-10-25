package hls

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// engineWait bounds a terminal engine wait so a stuck flight or lease can
// never hang the test binary.
func engineWait(t *testing.T, e *segmentEngine, d time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return fmt.Errorf("engine wait timed out after %v", d)
	}
}

func TestHLSSegmentRetryRefresh_Singleflight(t *testing.T) {
	bodies := [][]byte{[]byte("aa"), []byte("bb"), []byte("cc"), []byte("dd")}
	const n = 4
	arrived := make(chan struct{}, n)
	release := make(chan struct{})
	var listMu sync.Mutex
	playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
		"#EXTINF:4.0,\nv2/seg0\n#EXTINF:4.0,\nv2/seg1\n#EXTINF:4.0,\nv2/seg2\n#EXTINF:4.0,\nv2/seg3\n#EXT-X-ENDLIST\n"
	f := newHLSFixture(t, bodies, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media.m3u8":
				_, _ = w.Write([]byte(playlist))
			case "/seg0", "/seg1", "/seg2", "/seg3":
				arrived <- struct{}{}
				<-release
				w.WriteHeader(http.StatusForbidden)
			case "/v2/seg0", "/v2/seg1", "/v2/seg2", "/v2/seg3":
				listMu.Lock()
				defer listMu.Unlock()
				idx := int(r.URL.Path[len("/v2/seg")] - '0')
				_, _ = w.Write(bodies[idx])
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	e.setAdmissionOpen(true)
	if err := e.start(n); err != nil {
		t.Fatalf("start = %v", err)
	}
	// Every worker expires on its old-URI request before the release — the
	// refresh candidates are genuinely concurrent.
	for i := range n {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d concurrent expired requests arrived", i)
		}
	}
	close(release)
	if err := engineWait(t, e, 30*time.Second); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.requests.count("/media.m3u8"); got != 1 {
		t.Fatalf("manifest GETs = %d, want exactly 1 shared refresh", got)
	}
	for i := range n {
		if got := f.requests.count(fmt.Sprintf("/seg%d", i)); got != 1 {
			t.Fatalf("/seg%d expired requests = %d, want 1", i, got)
		}
		if got := f.requests.count(fmt.Sprintf("/v2/seg%d", i)); got != 1 {
			t.Fatalf("/v2/seg%d refreshed requests = %d, want 1", i, got)
		}
	}
	if got := f.output(t); !bytes.Equal(got, concat(bodies)) {
		t.Fatalf("output = %q", got)
	}
	if e.refreshGen != 1 {
		t.Fatalf("refreshGen = %d, want 1", e.refreshGen)
	}
}

func TestHLSSegmentRetryRefresh_CooldownAndGeneration(t *testing.T) {
	t.Run("fresh 403 on the newest URI waits out the cooldown", func(t *testing.T) {
		body0, body1 := []byte("aa"), []byte("bb")
		// Signature rotation keeps cleaned paths identical — the query is
		// the signature surface, so every refresh validates the same VOD.
		var mu sync.Mutex
		manifestCalls := 0
		hits := map[string]int{}
		hit := func(k string) { mu.Lock(); hits[k]++; mu.Unlock() }
		f := newHLSFixture(t, [][]byte{body0, body1}, fixtureOpts{
			handler: func(w http.ResponseWriter, r *http.Request) {
				sig := r.URL.Query().Get("sig")
				switch r.URL.Path {
				case "/media.m3u8":
					mu.Lock()
					manifestCalls++
					v := manifestCalls + 1 // call 1 -> sig=v2, call 2 -> sig=v3
					mu.Unlock()
					_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n"+
						"#EXTINF:4.0,\nseg0?sig=v%d\n#EXTINF:4.0,\nseg1?sig=v%d\n#EXT-X-ENDLIST\n", v, v)
				case "/seg0":
					hit("/seg0?sig=" + sig)
					switch sig {
					case "v2":
						_, _ = w.Write(body0)
					default:
						w.WriteHeader(http.StatusForbidden)
					}
				case "/seg1":
					hit("/seg1?sig=" + sig)
					if sig == "v3" {
						_, _ = w.Write(body1)
					} else {
						w.WriteHeader(http.StatusForbidden)
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
		})
		clk := newFakeClock()
		e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
		e.setAdmissionOpen(true)
		if err := e.start(1); err != nil {
			t.Fatalf("start = %v", err)
		}
		if err := engineWait(t, e, 30*time.Second); err != nil {
			t.Fatalf("wait = %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if manifestCalls != 2 {
			t.Fatalf("manifest GETs = %d, want exactly 2 (one per cooldown window)", manifestCalls)
		}
		// The refreshed-but-still-dead URI burns ordinary budget inside the
		// cooldown: five GETs against v2 before the window lets a second
		// refresh through.
		if n := hits["/seg1?sig=v2"]; n != 5 {
			t.Fatalf("seg1 v2 requests = %d, want 5 in-cooldown retries", n)
		}
		if n := hits["/seg1?sig=v3"]; n != 1 {
			t.Fatalf("seg1 v3 requests = %d, want 1 post-cooldown fetch", n)
		}
		if n := hits["/seg0?sig=v2"]; n != 1 {
			t.Fatalf("seg0 v2 requests = %d, want 1", n)
		}
		if e.refreshGen != 2 {
			t.Fatalf("refreshGen = %d, want 2", e.refreshGen)
		}
		if out := f.output(t); !bytes.Equal(out, concat([][]byte{body0, body1})) {
			t.Fatalf("output = %q", out)
		}
	})

	t.Run("failed flight stays throttled and keeps the mark", func(t *testing.T) {
		f := newHLSFixture(t, [][]byte{[]byte("aa")}, fixtureOpts{
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/media.m3u8":
					w.WriteHeader(http.StatusInternalServerError)
				case "/seg0":
					w.WriteHeader(http.StatusForbidden)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
		})
		clk := newFakeClock()
		e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
		e.setAdmissionOpen(true)
		if err := e.start(1); err != nil {
			t.Fatalf("start = %v", err)
		}
		err := engineWait(t, e, 30*time.Second)
		if !errors.Is(err, types.ErrPermanentHTTP) {
			t.Fatalf("wait = %v, want ErrPermanentHTTP", err)
		}
		// The failing flight throttles: six segment attempts share two
		// manifest fetches — one immediate, one after the window — rather
		// than one per flagged attempt.
		if n := f.requests.count("/media.m3u8"); n != 2 {
			t.Fatalf("manifest GETs = %d, want 2 — failed flights stay throttled in-window", n)
		}
		if e.refreshGen != 0 {
			t.Fatalf("refreshGen = %d after failed flights, want 0", e.refreshGen)
		}
	})
}

func TestHLSSegmentRetryRefresh_KeyExpiry(t *testing.T) {
	key := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
	}
	plain := []byte("key-rotated-segment")
	iv := make([]byte, 16)
	ct := encryptTestCBC(t, key, iv, plain)
	playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key2.bin\"\n" +
		"#EXTINF:4.0,\nv2/seg0\n#EXT-X-ENDLIST\n"
	f := newHLSFixture(t, [][]byte{ct}, fixtureOpts{
		key: key,
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media.m3u8":
				_, _ = w.Write([]byte(playlist))
			case "/seg0":
				// The media signature is still alive — only the key's
				// expired. Its 403 drives the same shared refresh.
				_, _ = w.Write(ct)
			case "/key.bin":
				w.WriteHeader(http.StatusForbidden)
			case "/v2/seg0":
				_, _ = w.Write(ct)
			case "/key2.bin":
				_, _ = w.Write(key)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	e.setAdmissionOpen(true)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := engineWait(t, e, 30*time.Second); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if n := f.requests.count("/media.m3u8"); n != 1 {
		t.Fatalf("manifest GETs = %d, want 1", n)
	}
	if n := f.requests.count("/key2.bin"); n != 1 {
		t.Fatalf("/key2.bin fetches = %d, want 1 refreshed key", n)
	}
	if got := f.output(t); !bytes.Equal(got, plain) {
		t.Fatalf("output = %q", got)
	}
	// The failed ciphertext credit rolled back exactly once — the counter
	// shows the plaintext, not two ciphertext downloads.
	if d := f.prog.Bytes.Downloaded.Load(); d != int64(len(plain)) {
		t.Fatalf("Downloaded = %d, want %d", d, len(plain))
	}
}

func TestHLSSegmentRetryRefresh_PermanentMismatch(t *testing.T) {
	f := newHLSFixture(t, [][]byte{[]byte("aa")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media.m3u8":
				// A different VOD came back — count changed from 1 to 2.
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
					"#EXTINF:4.0,\nv2/seg0\n#EXTINF:4.0,\nv2/seg1\n#EXT-X-ENDLIST\n"))
			case "/seg0":
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	e.setAdmissionOpen(true)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := engineWait(t, e, 30*time.Second)
	if !errors.Is(err, types.ErrSourceMetadataMismatch) {
		t.Fatalf("wait = %v, want ErrSourceMetadataMismatch", err)
	}
	if n := f.requests.count("/media.m3u8"); n != 1 {
		t.Fatalf("manifest GETs = %d, want 1 before the terminal latch", n)
	}
}
