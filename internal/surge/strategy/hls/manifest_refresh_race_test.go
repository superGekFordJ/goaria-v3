package hls

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the deadline passes — the engine
// transitions these tests observe are scheduler-owned, never sleeps the
// test can fake.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHLSSegmentRetryRefresh_AttemptSnapshot(t *testing.T) {
	// A mid-flight attempt keeps its locked generation: while seg0's
	// ciphertext body is parked mid-Read, a peer's 403 refreshes every URI
	// (segment and key alike). The parked attempt then finishes the old
	// body with the old key descriptor — never a mixed-generation pair.
	key := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
	}
	plain0, plain1 := []byte("parked-body-0"), []byte("second-body-1")
	iv1 := make([]byte, 16)
	iv1[15] = 1 // implicit IV for sequence 1
	ct0 := encryptTestCBC(t, key, make([]byte, 16), plain0)
	ct1 := encryptTestCBC(t, key, iv1, plain1)
	release0 := make(chan struct{})
	seg0Started := make(chan struct{})
	var startOnce, release0Once sync.Once
	// Unblock the parked seg0 handler on any failure path, or the
	// fixture's httptest.Server.Close wedges until the test timeout.
	t.Cleanup(func() { release0Once.Do(func() { close(release0) }) })
	playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key2.bin\"\n" +
		"#EXTINF:4.0,\nv2/seg0\n#EXTINF:4.0,\nv2/seg1\n#EXT-X-ENDLIST\n"
	f := newHLSFixture(t, [][]byte{ct0, ct1}, fixtureOpts{
		key: key,
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/seg0":
				startOnce.Do(func() { close(seg0Started) })
				w.WriteHeader(http.StatusOK)
				// Split the ciphertext so the body read genuinely parks
				// mid-flight while the refresh lands.
				_, _ = w.Write(ct0[:len(ct0)/2])
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				<-release0
				_, _ = w.Write(ct0[len(ct0)/2:])
			case "/seg1":
				w.WriteHeader(http.StatusForbidden)
			case "/media.m3u8":
				_, _ = w.Write([]byte(playlist))
			case "/v2/seg1":
				_, _ = w.Write(ct1)
			case "/key.bin", "/key2.bin":
				_, _ = w.Write(key)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	e.setAdmissionOpen(true)
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	<-seg0Started
	// Wait until the refresh published — the new key URI only exists after
	// adoption, so its fetch proves the generation flipped mid-attempt.
	waitFor(t, "refresh adoption", 10*time.Second,
		func() bool { return f.requests.count("/key2.bin") > 0 })
	release0Once.Do(func() { close(release0) })
	if err := engineWait(t, e, 30*time.Second); err != nil {
		t.Fatalf("wait = %v", err)
	}
	// The parked attempt's key fetch used its own old-generation descriptor:
	// /key.bin was hit even though /key2.bin was already published.
	if n := f.requests.count("/key.bin"); n != 1 {
		t.Fatalf("/key.bin fetches = %d, want the snapshot's old key URI once", n)
	}
	if n := f.requests.count("/media.m3u8"); n != 1 {
		t.Fatalf("manifest GETs = %d, want 1", n)
	}
	if got := f.output(t); !bytes.Equal(got, concat([][]byte{plain0, plain1})) {
		t.Fatalf("output = %q", got)
	}
}

func TestHLSSegmentRetryRefresh_CallerCancellation(t *testing.T) {
	bodies := [][]byte{[]byte("aa"), []byte("bb")}
	releaseManifest := make(chan struct{})
	manifestEntered := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseManifest) }) }
	// Unblock the parked manifest handler on any failure path, or the
	// fixture's httptest.Server.Close wedges until the test timeout.
	t.Cleanup(release)
	playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
		"#EXTINF:4.0,\nv2/seg0\n#EXTINF:4.0,\nv2/seg1\n#EXT-X-ENDLIST\n"
	f := newHLSFixture(t, bodies, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media.m3u8":
				enteredOnce.Do(func() { close(manifestEntered) })
				<-releaseManifest
				_, _ = w.Write([]byte(playlist))
			case "/seg0", "/seg1":
				w.WriteHeader(http.StatusForbidden)
			case "/v2/seg0":
				_, _ = w.Write(bodies[0])
			case "/v2/seg1":
				_, _ = w.Write(bodies[1])
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	e.setAdmissionOpen(true)
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	// The shared fetch is parked; every candidate is now waiting on done.
	<-manifestEntered
	ids := e.liveSessionIDs()
	if len(ids) != 2 {
		t.Fatalf("live sessions = %v, want 2", ids)
	}
	// manifestEntered only pins the fetch itself — the losing worker may
	// still be cycling its 403 through settle/backoff with no published
	// attempt, and cancelAttemptKill correctly refuses that state. Wait
	// for every session to park an attempt in the refresh wait first.
	waitFor(t, "all workers parked on the refresh wait", 5*time.Second, func() bool {
		waiting := 0
		for _, v := range e.sessionViews() {
			if v.AttemptGen != 0 && v.Phase == hlsPhaseWait {
				waiting++
			}
		}
		return waiting == len(ids)
	})
	attemptGenOf := func(id int) uint64 {
		for _, v := range e.sessionViews() {
			if v.ID == id {
				return v.AttemptGen
			}
		}
		return 0
	}
	// Kill one waiter mid-refresh: its segCtx must interrupt the wait even
	// though the shared fetch itself is still parked — the next published
	// attempt generation proves the killed attempt unwound and requeued
	// rather than dying inside the fetch.
	genBefore := attemptGenOf(ids[0])
	if !e.cancelAttemptKill(ids[0]) {
		t.Fatalf("kill refused on a refresh-waiting attempt %d", ids[0])
	}
	waitFor(t, "killed attempt to unwind", 5*time.Second, func() bool {
		return attemptGenOf(ids[0]) != genBefore
	})
	// Drain the other worker entirely — also a prompt exit from the wait.
	if !e.markDraining(ids[1]) {
		t.Fatalf("drain refused on session %d", ids[1])
	}
	waitFor(t, "drained worker exit", 5*time.Second,
		func() bool { return len(e.liveSessionIDs()) == 1 })
	// The fetch still owns the flight; releasing it finishes the refresh
	// and the surviving worker drains both segments on the new URIs.
	release()
	if err := engineWait(t, e, 30*time.Second); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if n := f.requests.count("/media.m3u8"); n != 1 {
		t.Fatalf("manifest GETs = %d, want 1 — kill/drain must not strangle the flight", n)
	}
	if got := f.output(t); !bytes.Equal(got, concat(bodies)) {
		t.Fatalf("output = %q", got)
	}
}

func TestHLSSegmentRetryRefresh_EngineExitJoinsFlight(t *testing.T) {
	releaseManifest := make(chan struct{})
	manifestEntered := make(chan struct{})
	manifestAborted := make(chan struct{})
	var onceEntered, onceAborted sync.Once
	f := newHLSFixture(t, [][]byte{[]byte("aa")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media.m3u8":
				onceEntered.Do(func() { close(manifestEntered) })
				select {
				case <-releaseManifest:
					_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\nv2/seg0\n#EXT-X-ENDLIST\n"))
				case <-r.Context().Done():
					onceAborted.Do(func() { close(manifestAborted) })
				}
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
	<-manifestEntered
	// Engine cancel must abort the engine-bound fetch and wait() must join
	// the flight before returning — never leaving a dangling goroutine
	// holding the transport lease.
	e.stop()
	select {
	case <-manifestAborted:
	case <-time.After(5 * time.Second):
		t.Fatal("engine cancel did not abort the shared manifest fetch")
	}
	err := engineWait(t, e, 10*time.Second)
	if err == nil {
		t.Fatal("wait = nil on a cancelled engine")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, errEngineIncomplete) {
		t.Fatalf("wait = %v, want cancel/incomplete identity", err)
	}
	close(releaseManifest) // let the handler fully unwind for the server close
}
