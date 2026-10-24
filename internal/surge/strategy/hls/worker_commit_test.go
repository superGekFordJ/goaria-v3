package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// TestHLSWorkerCommitKillMidReadRequeuesWhole: a kill during the body read
// rolls the unaccepted credit back, requeues the WHOLE segment (no partial
// Range), keeps retry budget intact, and the retry succeeds end to end.
func TestHLSWorkerCommitKillMidReadRequeuesWhole(t *testing.T) {
	body := bytes.Repeat([]byte("killme-"), 6000) // ~42KiB
	var calls atomic.Int64
	release1 := make(chan struct{}) // lets request 1 finish its body
	release2 := make(chan struct{}) // gates request 2 so the rollback is observable
	var rel1Once, rel2Once sync.Once
	open1 := func() { rel1Once.Do(func() { close(release1) }) }
	open2 := func() { rel2Once.Do(func() { close(release2) }) }
	defer open1()
	defer open2()
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body[:len(body)/2])
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				<-release1 // hold mid-body until the kill lands
				_, _ = w.Write(body[len(body)/2:])
				return
			}
			<-release2 // retry stays quiet until the rollback was observed
			_, _ = w.Write(body)
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	// Wait for progress bytes to land, then kill mid-read.
	deadline := time.Now().Add(5 * time.Second)
	for f.prog.Bytes.Downloaded.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no progress before kill")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ids := e.liveSessionIDs()
	if len(ids) != 1 || !e.cancelAttemptKill(ids[0]) {
		t.Fatalf("kill failed: sessions=%v", ids)
	}
	// The kill rolls the unaccepted credit back exactly once; the retry is
	// parked behind release2 so the zero point cannot be missed.
	deadline = time.Now().Add(5 * time.Second)
	for f.prog.Bytes.Downloaded.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("credit not rolled back after kill")
		}
		time.Sleep(5 * time.Millisecond)
	}
	open1()
	open2()
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, body) {
		t.Fatalf("output %d bytes after kill+requeue", len(got))
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (whole-segment re-GET)", calls.Load())
	}
	task := e.queue.tasks[0]
	if task.retries != 0 {
		t.Fatalf("kill consumed retries: %d", task.retries)
	}
	if v := f.prog.Bytes.VerifiedProgress.Load(); v != int64(len(body)) {
		t.Fatalf("VerifiedProgress = %d, want %d", v, len(body))
	}
}

// TestHLSWorkerCommitKillLosesToCommitGate: once the commit phase is claimed
// a kill returns false and must not disturb the in-flight Ingest.
func TestHLSWorkerCommitKillLosesToCommitGate(t *testing.T) {
	body := []byte("commit-first-payload")
	ops := defaultFileOps()
	realWrite := ops.write
	inWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	var relOnce sync.Once
	openWrite := func() { relOnce.Do(func() { close(releaseWrite) }) }
	defer openWrite()
	var once atomic.Int64
	ops.write = func(f *os.File, b []byte) (int, error) {
		if once.CompareAndSwap(0, 1) {
			close(inWrite)
			<-releaseWrite
		}
		return realWrite(f, b)
	}
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{ops: ops})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	select {
	case <-inWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("commit write never reached")
	}
	ids := e.liveSessionIDs()
	if len(ids) != 1 {
		t.Fatalf("sessions = %v", ids)
	}
	if e.cancelAttemptKill(ids[0]) {
		t.Fatal("kill during commit returned true")
	}
	openWrite()
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, body) {
		t.Fatalf("output = %q", got)
	}
}

// TestHLSWorkerCommitDrainDuringIngest: drain while Ingest is blocked on
// disk leaves the commit intact — it settles, then the worker exits.
func TestHLSWorkerCommitDrainDuringIngest(t *testing.T) {
	body := []byte("drain-during-commit")
	ops := defaultFileOps()
	realWrite := ops.write
	inWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	var relOnce sync.Once
	openWrite := func() { relOnce.Do(func() { close(releaseWrite) }) }
	defer openWrite()
	var once atomic.Int64
	ops.write = func(f *os.File, b []byte) (int, error) {
		if once.CompareAndSwap(0, 1) {
			close(inWrite)
			<-releaseWrite
		}
		return realWrite(f, b)
	}
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{ops: ops})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	select {
	case <-inWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("commit write never reached")
	}
	ids := e.liveSessionIDs()
	if !e.markDraining(ids[0]) {
		t.Fatal("markDraining returned false")
	}
	openWrite()
	// The commit settled and the queue emptied — drain with nothing left is
	// a clean stop, not an incomplete one.
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, body) {
		t.Fatalf("output = %q", got)
	}
}

// TestHLSWorkerCommitStorageFaultLatches: a mid-Ingest write failure is a
// storage fatal — the first latch wins, the error stays As-able, and peers
// are cancelled rather than fed a poisoned window.
func TestHLSWorkerCommitStorageFaultLatches(t *testing.T) {
	ops := defaultFileOps()
	var once atomic.Int64
	injected := errors.New("injected write fault")
	ops.write = func(f *os.File, b []byte) (int, error) {
		if once.CompareAndSwap(0, 1) {
			return 0, injected
		}
		return f.Write(b)
	}
	f := newHLSFixture(t, [][]byte{[]byte("one"), []byte("two")}, fixtureOpts{ops: ops})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if _, ok := errors.AsType[*storageError](err); !ok {
		t.Fatalf("wait = %v (%T), want *storageError", err, err)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("injected cause lost: %v", err)
	}
	if len(e.liveSessionIDs()) != 0 {
		t.Fatal("peers still running after fatal")
	}
}

// TestHLSWorkerCommitDiskFullPreserved: a platform disk-full errno inside a
// commit write keeps the ErrInsufficientDiskSpace annotation through the
// storageError latch.
func TestHLSWorkerCommitDiskFullPreserved(t *testing.T) {
	ops := defaultFileOps()
	var once atomic.Int64
	ops.write = func(f *os.File, b []byte) (int, error) {
		if once.CompareAndSwap(0, 1) {
			return 0, diskFullErrno()
		}
		return f.Write(b)
	}
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{ops: ops})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("wait = %v, want ErrInsufficientDiskSpace", err)
	}
}

// TestHLSWorkerCommitAcceptedThenReplayError: a direct ingest succeeds and
// the following pending-drain write fails — the accepted segment keeps its
// credit (never rolled back), the task is done, and the fault still kills
// the engine.
func TestHLSWorkerCommitAcceptedThenReplayError(t *testing.T) {
	body0 := []byte("frontier-")
	body1 := []byte("pending--")
	holdFirst := make(chan struct{})
	var holdOnce sync.Once
	openHold := func() { holdOnce.Do(func() { close(holdFirst) }) }
	defer openHold()
	ops := defaultFileOps()
	var writes atomic.Int64
	realWrite := ops.write
	injected := errors.New("drain write failed")
	ops.write = func(f *os.File, b []byte) (int, error) {
		// First write is seg0's direct append; the pending replay of seg1 is
		// the second write — that one fails.
		if writes.Add(1) == 2 {
			return 0, injected
		}
		return realWrite(f, b)
	}
	f := newHLSFixture(t, [][]byte{body0, body1}, fixtureOpts{
		ops: ops,
		handler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/seg0" {
				<-holdFirst // make seg1 win the ingest race
			}
			if r.URL.Path == "/seg1" {
				_, _ = w.Write(body1)
				return
			}
			_, _ = w.Write(body0)
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	// Wait until seg1 is buffered pending, then release seg0 — its direct
	// write lands (accepted), then the pending replay fails.
	time.Sleep(120 * time.Millisecond)
	openHold()
	err := e.wait()
	if _, ok := errors.AsType[*storageError](err); !ok {
		t.Fatalf("wait = %v (%T), want *storageError", err, err)
	}
	// Both ingests were accepted before the drain failure — the buffered
	// pending copy and the direct append both keep their credit.
	want := int64(len(body0) + len(body1))
	if d := f.prog.Bytes.Downloaded.Load(); d != want {
		t.Fatalf("Downloaded = %d, want %d kept for both accepted segments", d, want)
	}
	for i := range 2 {
		if st, _ := e.queue.state(i); st != segDone {
			t.Fatalf("seg%d state = %v, want done", i, st)
		}
	}
}

// TestHLSWorkerCommitShortWriteFatal: a zero-progress write inside Ingest is
// a storage fault (not retryable), not a data error.
func TestHLSWorkerCommitShortWriteFatal(t *testing.T) {
	ops := defaultFileOps()
	var once atomic.Int64
	ops.write = func(f *os.File, b []byte) (int, error) {
		if once.CompareAndSwap(0, 1) {
			return 0, nil // zero-progress write
		}
		return f.Write(b)
	}
	f := newHLSFixture(t, [][]byte{[]byte("shortwrite")}, fixtureOpts{ops: ops})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if _, ok := errors.AsType[*storageError](err); !ok {
		t.Fatalf("wait = %v (%T), want *storageError", err, err)
	}
	// Storage faults never consume retry budget.
	task := e.queue.tasks[0]
	if task.retries != 0 {
		t.Fatalf("storage fault consumed retries: %d", task.retries)
	}
	// The rejected attempt's credit rolled back fully.
	if d := f.prog.Bytes.Downloaded.Load(); d != 0 {
		t.Fatalf("Downloaded = %d, want 0 after rollback", d)
	}
}

// TestHLSWorkerCommitProgressMidflight proves the 64KiB/50ms ledger actually
// publishes during the read (not only at the end).
func TestHLSWorkerCommitProgressMidflight(t *testing.T) {
	body := bytes.Repeat([]byte("m"), 3*hlsWorkerBatchSize)
	release := make(chan struct{})
	var relOnce sync.Once
	open := func() { relOnce.Do(func() { close(release) }) }
	defer open()
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			if calls.Add(1) == 1 {
				_, _ = w.Write(body[:hlsWorkerBatchSize+10])
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				<-release
				_, _ = w.Write(body[hlsWorkerBatchSize+10:])
				return
			}
			_, _ = w.Write(body)
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v := f.prog.Bytes.Downloaded.Load(); v >= hlsWorkerBatchSize {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mid-flight Downloaded = %d, want >=64KiB before body end",
				f.prog.Bytes.Downloaded.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	open()
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if v := f.prog.Bytes.VerifiedProgress.Load(); v != int64(len(body)) {
		t.Fatalf("VerifiedProgress = %d, want %d", v, len(body))
	}
}

// TestHLSWorkerCommitWindowFileIsolation is a byte-exactness guard: mixed
// plaintext + encrypted out-of-order segments produce the reference
// plaintext concatenation.
func TestHLSWorkerCommitWindowFileIsolation(t *testing.T) {
	key := bytes.Repeat([]byte{0x9c}, 16)
	plainSegs := [][]byte{
		[]byte("plain-zero|"),
		bytes.Repeat([]byte("enc-one-"), 40),
		[]byte("plain-two-|"),
	}
	iv := make([]byte, 16)
	iv[15] = 1 // segment 1's implicit IV (Sequence=1)
	bodies := [][]byte{
		plainSegs[0],
		encryptTestCBC(t, key, iv, plainSegs[1]),
		plainSegs[2],
	}
	holdPlain := make(chan struct{})
	var holdOnce sync.Once
	openHold := func() { holdOnce.Do(func() { close(holdPlain) }) }
	defer openHold()
	f := newHLSFixture(t, bodies, fixtureOpts{
		key: key,
		segTweak: func(i int, seg *types.HLSSegmentState) {
			if i != 1 {
				seg.KeyIndex = 0 // plaintext
			}
		},
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/key.bin":
				_, _ = w.Write(key)
			case "/seg0", "/seg2":
				<-holdPlain // let the encrypted middle segment land first
				var i int
				_, _ = fmt.Sscanf(r.URL.Path, "/seg%d", &i)
				_, _ = w.Write(bodies[i])
			case "/seg1":
				_, _ = w.Write(bodies[1])
			}
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(3); err != nil {
		t.Fatalf("start = %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	openHold()
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	want := concat(plainSegs)
	if got := f.output(t); !bytes.Equal(got, want) {
		t.Fatalf("output = %q\nwant  = %q", got, want)
	}
	if v := f.prog.Bytes.VerifiedProgress.Load(); v != int64(len(want)) {
		t.Fatalf("VerifiedProgress = %d, want %d", v, len(want))
	}
}
