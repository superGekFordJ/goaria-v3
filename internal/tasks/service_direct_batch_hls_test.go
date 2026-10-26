package tasks

import (
	"context"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/strategy/hls"
	surgetypes "goaria-v3/internal/surge/types"
)

// hlsCallRecorder pairs each AddUri call's URL with its options atomically —
// concurrent batch submission makes positional matching unreliable.
type hlsCallRecorder struct {
	diskSpaceStubEngine
	mu      sync.Mutex
	entries []hlsCallRecord
}

type hlsCallRecord struct {
	url  string
	opts rpc.AddURIOptions
}

func (e *hlsCallRecorder) AddUri(url string, opts rpc.AddURIOptions) (string, error) {
	e.mu.Lock()
	e.entries = append(e.entries, hlsCallRecord{url: url, opts: opts})
	e.mu.Unlock()
	return e.diskSpaceStubEngine.AddUri(url, opts)
}

func (e *hlsCallRecorder) optionsFor(url string) (rpc.AddURIOptions, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rec := range e.entries {
		if rec.url == url {
			return rec.opts, true
		}
	}
	return rpc.AddURIOptions{}, false
}

func (e *hlsCallRecorder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.entries)
}

// A mixed batch: the .m3u8 item auto-detects HLS with no strategy field and
// picks its output name from PageTitle; the ordinary item is untouched by
// the title metadata.
func TestAddPreparedDirectItems_HLSAutoDetectAndTitleNaming(t *testing.T) {
	aria2Cap := &capturingAria2Engine{}
	surgeCap := &hlsCallRecorder{}
	svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)
	stubAddTaskHLSPreflight(t, successfulHLSPreflight(hls.ContainerTS, "https://seg.invalid/s0.ts", 0))

	const manifestURL = "https://cdn.example.invalid/index.m3u8"
	const binURL = "https://files.example.invalid/downloads/b.bin"
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	result, err := svc.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: manifestURL, PageTitle: "Live Show"},
			{ClientItemID: idB, URL: binURL, Filename: "b.bin"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems: %v", err)
	}
	assertBatchAddStringsUnordered(t, "succeeded", result.Succeeded, []string{idA, idB})
	if len(result.Errors) != 0 {
		t.Fatalf("errors = %v", result.Errors)
	}
	if got := len(aria2Cap.snapshotOptions()); got != 0 {
		t.Fatalf("aria2 AddUri calls = %d, want 0", got)
	}
	if surgeCap.count() != 2 {
		t.Fatalf("surge AddUri calls = %d, want 2", surgeCap.count())
	}

	hlsOpts, ok := surgeCap.optionsFor(manifestURL)
	if !ok {
		t.Fatal("manifest item never reached surge")
	}
	if hlsOpts.Strategy != surgetypes.StrategyHLS || hlsOpts.HLSResume == nil {
		t.Fatalf("m3u8 item not routed as HLS: %+v", hlsOpts)
	}
	want := expectedHLSOut(t, manifestURL, "", "Live Show", hls.ContainerTS)
	if hlsOpts.Out != want {
		t.Fatalf("HLS Out = %q, want %q", hlsOpts.Out, want)
	}
	if !strings.HasSuffix(hlsOpts.Out, ".ts") {
		t.Fatalf("HLS Out = %q, want .ts", hlsOpts.Out)
	}

	binOpts, ok := surgeCap.optionsFor(binURL)
	if !ok {
		t.Fatal("bin item never reached surge")
	}
	if binOpts.Strategy != "" || binOpts.HLSResume != nil {
		t.Fatalf("ordinary item gained HLS state: %+v", binOpts)
	}
	if binOpts.Out != "b.bin" {
		t.Fatalf("ordinary Out = %q, want b.bin (title must not leak)", binOpts.Out)
	}
}

// An explicit PreparedDirectAddItem.Strategy pin keeps working next to the
// title path; a pinned non-manifest URL still submits as HLS.
func TestAddPreparedDirectItems_HLSExplicitStrategyPin(t *testing.T) {
	aria2Cap := &capturingAria2Engine{}
	surgeCap := &hlsCallRecorder{}
	svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)
	stubAddTaskHLSPreflight(t, successfulHLSPreflight(hls.ContainerFMP4, "https://seg.invalid/s0.m4s", 0))

	const url = "https://cdn.example.invalid/live/edge" // extensionless
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := svc.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: url, Strategy: surgetypes.StrategyHLS, PageTitle: "Pinned Show"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems: %v", err)
	}
	assertBatchAddStringsUnordered(t, "succeeded", result.Succeeded, []string{idA})
	opts, ok := surgeCap.optionsFor(url)
	if !ok {
		t.Fatal("pinned item never reached surge")
	}
	if opts.Strategy != surgetypes.StrategyHLS {
		t.Fatalf("Strategy = %q, want hls", opts.Strategy)
	}
	want := expectedHLSOut(t, url, "", "Pinned Show", hls.ContainerFMP4)
	if opts.Out != want || !strings.HasSuffix(opts.Out, ".mp4") {
		t.Fatalf("Out = %q, want %q", opts.Out, want)
	}
	if got := len(aria2Cap.snapshotOptions()); got != 0 {
		t.Fatalf("aria2 AddUri calls = %d, want 0", got)
	}
}
