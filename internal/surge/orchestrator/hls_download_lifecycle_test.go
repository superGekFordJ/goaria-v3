package orchestrator

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// hlsE2EStack wires the production event path: pool events feed the bus
// InputCh, the lifecycle worker consumes one subscription for persistence
// and final-file promotion, and the test drains a second subscription.
type hlsE2EStack struct {
	bus  *EventBus
	pool *scheduler.Scheduler
	mgr  *LifecycleManager

	mu  sync.Mutex
	evs []types.DownloadEvent
}

func newHLSE2EStack(t *testing.T, slots int) *hlsE2EStack {
	t.Helper()
	return newHLSE2EStackAt(t, slots, filepath.Join(t.TempDir(), "state.db"))
}

// newHLSE2EStackAt is newHLSE2EStack with an explicit state path so a second
// stack can reopen the same store after a simulated process restart.
func newHLSE2EStackAt(t *testing.T, slots int, dbPath string) *hlsE2EStack {
	t.Helper()
	store.Configure(dbPath)
	t.Cleanup(store.CloseDB)

	bus := NewEventBus()
	pool := scheduler.New(bus.InputCh, slots)
	mgr := NewLifecycleManager(pool, bus, config.DefaultSettings())
	// The persistence worker rides the reliable feed: lifecycle events must
	// not be lost when an ordinary observer falls behind.
	workerSub, workerCleanup := bus.SubscribeLifecycle()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		mgr.StartEventWorker(workerSub)
	}()

	s := &hlsE2EStack{bus: bus, pool: pool, mgr: mgr}
	sub, subCleanup := bus.Subscribe()
	observerDone := make(chan struct{})
	go func() {
		defer close(observerDone)
		for ev := range sub {
			s.mu.Lock()
			s.evs = append(s.evs, ev)
			s.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		// Shutdown drains the reliable feed and closes workerSub, then the
		// worker and the observer are joined so nothing outlives the test.
		mgr.Shutdown()
		<-workerDone
		workerCleanup()
		subCleanup()
		<-observerDone
	})
	return s
}

func (s *hlsE2EStack) eventsFor(id string) []types.DownloadEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []types.DownloadEvent
	for _, ev := range s.evs {
		if ev.DownloadID == id {
			out = append(out, ev)
		}
	}
	return out
}

func (s *hlsE2EStack) countEvents(id string, typ types.EventType) int {
	n := 0
	for _, ev := range s.eventsFor(id) {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func waitForCond(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *hlsE2EStack) waitMasterStatus(t *testing.T, id, want string, timeout time.Duration) *types.DownloadRecord {
	t.Helper()
	var entry *types.DownloadRecord
	waitForCond(t, timeout, "master status "+want, func() bool {
		e, _ := store.GetDownload(id)
		if e != nil && e.Status == want {
			entry = e
			return true
		}
		return false
	})
	return entry
}

// hlsOrigin is a recording VOD origin; the mux is populated per test.
type hlsOrigin struct {
	srv *httptest.Server
	mux *http.ServeMux
	mu  sync.Mutex
	req map[string]int
}

func newHLSOrigin(t *testing.T) *hlsOrigin {
	t.Helper()
	o := &hlsOrigin{mux: http.NewServeMux(), req: make(map[string]int)}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.req[r.URL.Path]++
		o.mu.Unlock()
		o.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *hlsOrigin) url(path string) string { return o.srv.URL + path }

func (o *hlsOrigin) count(path string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.req[path]
}

func (o *hlsOrigin) servePlaylist(path string, segRefs []string, tags ...string) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
	for _, tag := range tags {
		b.WriteString(tag + "\n")
	}
	for _, ref := range segRefs {
		b.WriteString("#EXTINF:4.0,\n" + ref + "\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	body := b.String()
	o.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(body))
	})
}

func (o *hlsOrigin) serve(path string, body []byte) {
	o.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
}

func e2eCBCEncrypt(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, len(plain)+pad)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func e2eEnqueueHLS(t *testing.T, s *hlsE2EStack, manifestURL, dir, filename string) string {
	t.Helper()
	id, gotName, err := s.mgr.Enqueue(context.Background(), &DownloadRequest{
		URL: manifestURL, Path: dir, Filename: filename, Strategy: types.StrategyHLS,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == "" || gotName != filename {
		t.Fatalf("Enqueue = id %q filename %q, want filename %q", id, gotName, filename)
	}
	return id
}

func TestHLSDownloadLifecycle_TSVODCompletesAndPromotes(t *testing.T) {
	s := newHLSE2EStack(t, 2)
	o := newHLSOrigin(t)
	segs := [][]byte{[]byte("seg-0-"), []byte("seg-1-"), []byte("seg-2-tail")}
	refs := []string{"s0.ts", "s1.ts", "s2.ts"}
	for i, body := range segs {
		o.serve("/"+refs[i], body)
	}
	o.servePlaylist("/media.m3u8", refs)

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, o.url("/media.m3u8"), dir, "video.ts")

	entry := s.waitMasterStatus(t, id, "completed", 15*time.Second)
	want := append(append(append([]byte{}, segs[0]...), segs[1]...), segs[2]...)
	final := filepath.Join(dir, "video.ts")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("final content %q, want %q", got, want)
	}
	if _, err := os.Stat(final + types.IncompleteSuffix); !os.IsNotExist(err) {
		t.Fatalf(".surge still present after promotion: %v", err)
	}
	if entry.TotalSize != int64(len(want)) || entry.Downloaded != int64(len(want)) {
		t.Fatalf("completed entry counters = %d/%d, want %d", entry.TotalSize, entry.Downloaded, len(want))
	}
	if entry.Strategy != types.StrategyHLS {
		t.Fatalf("completed entry strategy = %q", entry.Strategy)
	}
	// Completion deletes the detail gob — no credential/checkpoint residue.
	if states, _ := store.LoadStates([]string{id}); states[id] != nil {
		t.Fatal("completed HLS task kept a detail record")
	}
	// Event shape: HLS TotalSize was zero at start; the single FirstByte came
	// from the downloader; EventComplete carries the physical size.
	started := s.countEvents(id, types.EventStarted)
	if started != 1 {
		t.Fatalf("EventStarted count = %d, want 1", started)
	}
	for _, ev := range s.eventsFor(id) {
		if ev.Type == types.EventStarted && ev.Total != 0 {
			t.Fatalf("EventStarted.Total = %d, want 0 (unknown until physical completion)", ev.Total)
		}
		if ev.Type == types.EventComplete && ev.Total != int64(len(want)) {
			t.Fatalf("EventComplete.Total = %d, want %d", ev.Total, len(want))
		}
	}
	if n := s.countEvents(id, types.EventFirstByte); n != 1 {
		t.Fatalf("EventFirstByte count = %d, want exactly 1", n)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
}

func TestHLSDownloadLifecycle_EncryptedFMP4CompletesAndPromotes(t *testing.T) {
	s := newHLSE2EStack(t, 2)
	o := newHLSOrigin(t)
	key := make([]byte, aes.BlockSize)
	iv := make([]byte, aes.BlockSize)
	for i := range key {
		key[i] = byte(0x10 + i)
		iv[i] = byte(0x50 + i)
	}
	initPlain := []byte("FTYP-INIT-SEGMENT")
	plain := [][]byte{[]byte("moof-mdat-0"), []byte("moof-mdat-1")}
	o.serve("/init.mp4", e2eCBCEncrypt(t, key, iv, initPlain))
	refs := make([]string, 0, len(plain))
	for i, p := range plain {
		ref := fmt.Sprintf("seg%d.m4s", i)
		o.serve("/"+ref, e2eCBCEncrypt(t, key, iv, p))
		refs = append(refs, ref)
	}
	o.serve("/key.bin", key)
	o.servePlaylist("/vod.m3u8", refs,
		fmt.Sprintf("#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x%x", iv),
		"#EXT-X-MAP:URI=\"init.mp4\"")

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, o.url("/vod.m3u8"), dir, "video.mp4")

	s.waitMasterStatus(t, id, "completed", 15*time.Second)
	final := filepath.Join(dir, "video.mp4")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	want := append(append(append([]byte{}, initPlain...), plain[0]...), plain[1]...)
	if string(got) != string(want) {
		t.Fatalf("final content %q, want decrypted init+segments %q", got, want)
	}
	if n := o.count("/key.bin"); n < 1 {
		t.Fatalf("key fetched %d times, want >= 1", n)
	}
	if n := o.count("/init.mp4"); n != 1 {
		t.Fatalf("init fetched %d times, want exactly 1", n)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
}

func TestHLSDownloadLifecycle_TransientErrorRetriesThenCompletes(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	o := newHLSOrigin(t)
	segs := [][]byte{[]byte("r0"), []byte("r1")}
	refs := []string{"s0.ts", "s1.ts"}
	for i, body := range segs {
		o.serve("/"+refs[i], body)
	}
	var manifestCalls atomic.Int32
	o.mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if manifestCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
			"#EXTINF:4.0,\ns0.ts\n#EXTINF:4.0,\ns1.ts\n#EXT-X-ENDLIST\n"))
	})

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, o.url("/media.m3u8"), dir, "video.ts")

	s.waitMasterStatus(t, id, "completed", 20*time.Second)
	got, err := os.ReadFile(filepath.Join(dir, "video.ts"))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != "r0r1" {
		t.Fatalf("final content %q, want %q", got, "r0r1")
	}
	if starts := s.countEvents(id, types.EventStarted); starts != 2 {
		t.Fatalf("EventStarted count = %d, want 2 (initial + retry)", starts)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d — a transient failure must retry, not terminate", n)
	}
	if n := s.countEvents(id, types.EventQueued); n != 2 {
		// One from enqueue itself, one from the retry requeue.
		t.Fatalf("EventQueued count = %d, want 2 (enqueue + retry)", n)
	}
	if manifestCalls.Load() != 2 {
		t.Fatalf("manifest fetches = %d, want 2", manifestCalls.Load())
	}
}

func TestHLSDownloadLifecycle_PauseResumePreservesMergedPrefix(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	o := newHLSOrigin(t)
	o.serve("/s0.ts", []byte("part-0"))
	// The first s1 request parks until the run ctx dies (the pause cancels
	// it); later requests serve immediately so the resumed run completes.
	var s1Calls atomic.Int32
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		if s1Calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("-part-1"))
	})
	o.servePlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, o.url("/media.m3u8"), dir, "video.ts")

	// Wait until s0 is merged and the s1 request is in flight, then pause.
	waitForCond(t, 10*time.Second, "s1 in flight", func() bool {
		return o.count("/s1.ts") >= 1
	})
	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// The paused lifecycle event is the durable-checkpoint boundary.
	waitForCond(t, 10*time.Second, "EventPaused observed", func() bool {
		return s.countEvents(id, types.EventPaused) >= 1
	})
	entry := s.waitMasterStatus(t, id, "paused", 10*time.Second)
	if entry.Strategy != types.StrategyHLS {
		t.Fatalf("paused entry strategy = %q", entry.Strategy)
	}
	// The durable checkpoint must already sit in the detail gob.
	states, _ := store.LoadStates([]string{id})
	detail := states[id]
	if detail == nil || detail.HLSResume == nil {
		t.Fatal("paused HLS task has no detail checkpoint")
	}
	if detail.HLSResume.MergedSegments != 1 || detail.HLSResume.VerifiedBytes != int64(len("part-0")) {
		t.Fatalf("paused checkpoint = %+v, want merged prefix of s0", detail.HLSResume)
	}

	if err := s.mgr.Resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	s.waitMasterStatus(t, id, "completed", 15*time.Second)

	got, err := os.ReadFile(filepath.Join(dir, "video.ts"))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != "part-0-part-1" {
		t.Fatalf("final content %q, want %q", got, "part-0-part-1")
	}
	if n := o.count("/s0.ts"); n != 1 {
		t.Fatalf("s0 fetched %d times — the merged prefix was refetched", n)
	}
	if n := s.countEvents(id, types.EventResumed); n != 1 {
		t.Fatalf("EventResumed count = %d, want 1", n)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
}

func TestHLSDownloadLifecycle_CancelRemovesArtifacts(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	o := newHLSOrigin(t)
	o.serve("/s0.ts", []byte("part-0"))
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	o.servePlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, o.url("/media.m3u8"), dir, "video.ts")

	waitForCond(t, 10*time.Second, "s1 in flight", func() bool {
		return o.count("/s1.ts") >= 1
	})
	if err := s.mgr.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitForCond(t, 10*time.Second, "EventRemoved observed", func() bool {
		return s.countEvents(id, types.EventRemoved) >= 1
	})
	waitForCond(t, 10*time.Second, "master row removed", func() bool {
		e, _ := store.GetDownload(id)
		return e == nil
	})
	final := filepath.Join(dir, "video.ts")
	if _, err := os.Stat(final + types.IncompleteSuffix); !os.IsNotExist(err) {
		t.Fatalf(".surge survived cancel: %v", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final file exists after cancel: %v", err)
	}
	if states, _ := store.LoadStates([]string{id}); states[id] != nil {
		t.Fatal("detail record survived cancel")
	}
	// The removal fence: neither EventError nor EventComplete may leak after
	// the removed claim settles.
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d after removal", n)
	}
	if n := s.countEvents(id, types.EventComplete); n != 0 {
		t.Fatalf("EventComplete count = %d after removal", n)
	}
}
