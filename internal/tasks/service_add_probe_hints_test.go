package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/strategy/hls"
	surgetypes "goaria-v3/internal/surge/types"
)

type capturingAddURIEngine struct {
	diskSpaceStubEngine
	optMu   sync.Mutex
	options []rpc.AddURIOptions
}

func (e *capturingAddURIEngine) AddUri(url string, options rpc.AddURIOptions) (string, error) {
	e.optMu.Lock()
	e.options = append(e.options, options)
	e.optMu.Unlock()
	return e.diskSpaceStubEngine.AddUri(url, options)
}

func (e *capturingAddURIEngine) snapshotOptions() []rpc.AddURIOptions {
	e.optMu.Lock()
	defer e.optMu.Unlock()
	out := make([]rpc.AddURIOptions, len(e.options))
	copy(out, e.options)
	return out
}

// capturingAria2Engine returns ar_-prefixed GIDs so the smart-thread Aria2
// branch (prepareAria2 + tracker writes) runs end to end in tests.
type capturingAria2Engine struct {
	diskSpaceStubEngine
	optMu   sync.Mutex
	options []rpc.AddURIOptions
}

func (e *capturingAria2Engine) AddUri(url string, options rpc.AddURIOptions) (string, error) {
	e.optMu.Lock()
	e.options = append(e.options, options)
	n := len(e.options)
	e.optMu.Unlock()
	return fmt.Sprintf("ar_gid-%d", n), nil
}

func (e *capturingAria2Engine) snapshotOptions() []rpc.AddURIOptions {
	e.optMu.Lock()
	defer e.optMu.Unlock()
	out := make([]rpc.AddURIOptions, len(e.options))
	copy(out, e.options)
	return out
}

func setupProbeHintsAddService(t *testing.T, engine rpc.DownloadEngine, smartThread bool) *Service {
	t.Helper()
	svc := setupDiskSpaceAddService(t, engine)
	cfg := *config.Get()
	cfg.SmartThreadMode = smartThread
	cfg.MaxConnections = "8"
	config.SetTestConfig(&cfg)
	return svc
}

func TestAddUriFromExtension_TrustedSizeFilenameSetsPayloadFirstSkip(t *testing.T) {
	for _, smartThread := range []bool{false, true} {
		for _, skipHead := range []bool{false, true} {
			t.Run(smartThreadName(smartThread, skipHead), func(t *testing.T) {
				cap := &capturingAddURIEngine{}
				svc := setupProbeHintsAddService(t, cap, smartThread)
				_, err := svc.AddUriFromExtension(extension.DownloadRequest{
					Type:          extension.MsgTypeDownload,
					URL:           "https://example.com/ext.bin",
					Filename:      "ext.bin",
					FileSize:      4096,
					SkipHeadProbe: skipHead,
				})
				if err != nil {
					t.Fatalf("AddUriFromExtension: %v", err)
				}
				opts := cap.snapshotOptions()
				if len(opts) != 1 {
					t.Fatalf("AddUri calls = %d, want 1", len(opts))
				}
				got := opts[0]
				if got.FileSize != 4096 {
					t.Fatalf("FileSize = %d, want 4096", got.FileSize)
				}
				if got.Out != "ext.bin" {
					t.Fatalf("Out = %q, want ext.bin", got.Out)
				}
				if got.SupportsRange != nil {
					t.Fatalf("SupportsRange = %v, want nil", ptrBool(got.SupportsRange))
				}
				if got.RangeAcquisitionMode != "payload_first_unknown" {
					t.Fatalf("RangeAcquisitionMode = %q, want payload_first_unknown", got.RangeAcquisitionMode)
				}
				if !got.SkipServerProbe {
					t.Fatal("SkipServerProbe = false, want true")
				}
			})
		}
	}
}

func TestAddUriFromExtension_MissingFilenameUsesURLBasename(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		cap := &capturingAddURIEngine{}
		svc := setupProbeHintsAddService(t, cap, false)
		_, err := svc.AddUriFromExtension(extension.DownloadRequest{
			Type:     extension.MsgTypeDownload,
			URL:      "https://example.com/nofile.bin",
			FileSize: 4096,
		})
		if err != nil {
			t.Fatalf("AddUriFromExtension: %v", err)
		}
		opts := cap.snapshotOptions()
		if len(opts) != 1 {
			t.Fatalf("AddUri calls = %d, want 1", len(opts))
		}
		if opts[0].Out != "nofile.bin" {
			t.Fatalf("Out = %q, want nofile.bin", opts[0].Out)
		}
	})

	t.Run("whitespace", func(t *testing.T) {
		cap := &capturingAddURIEngine{}
		svc := setupProbeHintsAddService(t, cap, false)
		_, err := svc.AddUriFromExtension(extension.DownloadRequest{
			Type:     extension.MsgTypeDownload,
			URL:      "https://example.com/ws.bin",
			Filename: "   ",
			FileSize: 4096,
		})
		if err != nil {
			t.Fatalf("AddUriFromExtension: %v", err)
		}
		opts := cap.snapshotOptions()
		if len(opts) != 1 {
			t.Fatalf("AddUri calls = %d, want 1", len(opts))
		}
		if opts[0].Out != "ws.bin" {
			t.Fatalf("Out = %q, want ws.bin", opts[0].Out)
		}
	})
}

func TestAddUriFromExtension_SkipHeadProbeWithoutSizeLeavesRangeNil(t *testing.T) {
	cap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, cap, false)
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:          extension.MsgTypeDownload,
		URL:           "https://example.com/nosize.bin",
		Filename:      "nosize.bin",
		FileSize:      0,
		SkipHeadProbe: true,
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension: %v", err)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].FileSize != 0 || opts[0].SupportsRange != nil || opts[0].RangeAcquisitionMode != "" || opts[0].SkipServerProbe {
		t.Fatalf("FileSize=%d SupportsRange=%v mode=%q skip=%v, want 0/nil/empty/false",
			opts[0].FileSize, ptrBool(opts[0].SupportsRange), opts[0].RangeAcquisitionMode, opts[0].SkipServerProbe)
	}
}

func TestAddUriFromExtension_PreservesAuthHeadersOnSkip(t *testing.T) {
	cap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, cap, false)
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:     extension.MsgTypeDownload,
		URL:      "https://example.com/auth.bin",
		Filename: "auth.bin",
		FileSize: 2048,
		Headers:  []string{"Cookie: sid=abc", "Authorization: Bearer tok"},
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension: %v", err)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	got := opts[0].Headers
	if !containsHeader(got, "Cookie: sid=abc") || !containsHeader(got, "Authorization: Bearer tok") {
		t.Fatalf("headers = %#v, want Cookie and Authorization", got)
	}
	if opts[0].SupportsRange != nil {
		t.Fatalf("SupportsRange = %v, want nil", ptrBool(opts[0].SupportsRange))
	}
	if opts[0].RangeAcquisitionMode != "payload_first_unknown" || !opts[0].SkipServerProbe {
		t.Fatalf("mode=%q skip=%v, want payload_first_unknown/true", opts[0].RangeAcquisitionMode, opts[0].SkipServerProbe)
	}
}

func TestAddUri_TrustedProbeExtractorLeavesRangeNil(t *testing.T) {
	cap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, cap, false)
	shareURL := "https://share.fixture.invalid/d/probe-hints"
	svc.Adapter = &fakePortAdapter{items: []ResolvedItem{{
		SourceURL: shareURL,
		URL:       "https://download.fixture.invalid/file.bin",
		Filename:  "file.bin",
		SizeBytes: 10 * 1024 * 1024,
		ID:        "item-1",
	}}}

	if result := svc.AddUri(shareURL); result != "success" {
		t.Fatalf("AddUri = %q, want success", result)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].FileSize != 0 || opts[0].SupportsRange != nil || opts[0].RangeAcquisitionMode != "" || opts[0].SkipServerProbe {
		t.Fatalf("extractor FileSize=%d SupportsRange=%v mode=%q skip=%v, want 0/nil/empty/false", opts[0].FileSize, ptrBool(opts[0].SupportsRange), opts[0].RangeAcquisitionMode, opts[0].SkipServerProbe)
	}
	if opts[0].Out != "file.bin" {
		t.Fatalf("Out = %q, want file.bin", opts[0].Out)
	}
}

func TestAddUri_ProtectedTrustedSizeFilenameLeavesRangeNil(t *testing.T) {
	candidate := addTaskCandidate{
		protected: true,
		sizeBytes: 4096,
		out:       "prot.bin",
	}
	if shouldSkipEngineProbe(candidate, "prot.bin") {
		t.Fatal("protected=true must not skip engine probe")
	}
	opts := buildCandidateAddURIOptions(`D:\Downloads`, "prot.bin", nil, 8, 0, nil, candidate)
	if opts.FileSize != 0 || opts.SupportsRange != nil || opts.RangeAcquisitionMode != "" || opts.SkipServerProbe {
		t.Fatalf("protected skip fields set: FileSize=%d SupportsRange=%v mode=%q skip=%v",
			opts.FileSize, ptrBool(opts.SupportsRange), opts.RangeAcquisitionMode, opts.SkipServerProbe)
	}
}

func smartThreadName(smartThread, skipHead bool) string {
	st := "smartThreadOff"
	if smartThread {
		st = "smartThreadOn"
	}
	sh := "skipHeadFalse"
	if skipHead {
		sh = "skipHeadTrue"
	}
	return st + "/" + sh
}

func ptrBool(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

func containsHeader(headers []string, want string) bool {
	return slices.Contains(headers, want)
}

func TestAddUriFromExtension_ReservedFilenameIsRenamed(t *testing.T) {
	cap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, cap, false)
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:     extension.MsgTypeDownload,
		URL:      "https://files.alpha.test/downloads/b.bin",
		Filename: "CON.txt",
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension: %v", err)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].Out == "CON.txt" || opts[0].Out == "" {
		t.Fatalf("Out = %q, want renamed non-reserved name", opts[0].Out)
	}
}

func TestAddUriFromExtension_EmptyFilenameDeviceURLUsesDownloadBin(t *testing.T) {
	cap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, cap, false)
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type: extension.MsgTypeDownload,
		URL:  "https://files.alpha.test/NUL",
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension: %v", err)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].Out == "" || opts[0].Out == "NUL" {
		t.Fatalf("Out = %q, want download.bin or other non-device name", opts[0].Out)
	}
}

// Two sequential unknown-size Aria2 adds on a known domain: the first must
// plan with the real N_sat and reserve its target bandwidth; the second —
// seeing the first task's live confirmed-add occupancy claim through the
// fresh batch ledger — converges to the domain-exhaustion floor instead of
// blindly opening maxConn again.
func TestAddUri_Aria2UnknownSize_UsesBBRNSatAndReservesLedger(t *testing.T) {
	engine := &capturingAria2Engine{}
	svc := setupProbeHintsAddService(t, engine, true)
	cfg := *config.Get()
	cfg.MaxConnections = "16"
	config.SetTestConfig(&cfg)

	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	origTr := monitor.State.GetTracker()
	tr := monitor.NewTaskTracker()
	monitor.State.SetTracker(tr)
	t.Cleanup(func() { monitor.State.SetTracker(origTr) })

	// Literal-IP URLs: no DNS, deterministic classification (scope=wan,
	// domain=IP literal). envKey comes from the same derivation the add path
	// uses, so seeding matches on any machine.
	const url1 = "https://203.0.113.10/stream1.bin"
	const url2 = "https://203.0.113.10/stream2.bin"
	envKey := monitor.ComputeEnvKeyForDownload(url1, "203.0.113.10")

	// 203.0.113.10: eff 2MiB/s, domain peak 8MiB/s. A foreign-domain record
	// lifts the scope global peak to 64MiB/s so vTarget meets the domain peak.
	speedstats.AddRecordV2(8<<20, 4, 200<<20, false, 100, "203.0.113.10", "wan", envKey)
	speedstats.AddRecordV2(64<<20, 8, 200<<20, false, 100, "198.51.100.20", "wan", envKey)

	gid1, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:          extension.MsgTypeDownload,
		URL:           url1,
		Filename:      "stream1.bin",
		FileSize:      0,
		SkipHeadProbe: true,
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension #1: %v", err)
	}
	if gid1 != "ar_gid-1" {
		t.Fatalf("gid1 = %q, want ar_gid-1", gid1)
	}
	opts := engine.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	// N_sat = ceil(8/2)+1 = 5 — never the old blind maxConn=16.
	if opts[0].Split != 5 {
		t.Fatalf("task1 Split = %d, want 5", opts[0].Split)
	}
	if opts[0].MinSplitSize != 0 {
		t.Fatalf("task1 MinSplitSize = %d, want 0 (unknown size)", opts[0].MinSplitSize)
	}
	tt1 := mustTracked(t, tr, gid1)
	if tt1.ThreadCount != 5 {
		t.Fatalf("task1 ThreadCount = %d, want 5", tt1.ThreadCount)
	}
	if tt1.TargetBandwidth != 10<<20 {
		t.Fatalf("task1 TargetBandwidth = %d, want 10MiB (2MiB/s * 5)", tt1.TargetBandwidth)
	}
	if tt1.MinChunk != 0 {
		t.Fatalf("task1 MinChunk = %d, want 0", tt1.MinChunk)
	}
	// Split == NSat == 5 → no keep-alive deficit to flag.
	if tt1.IsKeepAlive {
		t.Fatal("task1 IsKeepAlive = true, want false (Split == NSat)")
	}

	// The confirmed-add placeholder makes task1's 10MiB claim visible to the
	// next ledger seed — the domain bucket (8MiB peak) is oversubscribed.
	seeded := smartthread.NewBandwidthLedger(BuildOccupancyTaskInfos())
	if got := seeded.ReservedByDomain("wan", "203.0.113.10"); got != 10<<20 {
		t.Fatalf("seeded domain reservation = %d, want 10MiB", got)
	}
	if got := seeded.Reserved("wan", envKey); got != 10<<20 {
		t.Fatalf("seeded scope reservation = %d, want 10MiB", got)
	}

	gid2, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:          extension.MsgTypeDownload,
		URL:           url2,
		Filename:      "stream2.bin",
		FileSize:      0,
		SkipHeadProbe: true,
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension #2: %v", err)
	}
	if gid2 != "ar_gid-2" {
		t.Fatalf("gid2 = %q, want ar_gid-2", gid2)
	}
	opts = engine.snapshotOptions()
	if len(opts) != 2 {
		t.Fatalf("AddUri calls = %d, want 2", len(opts))
	}
	// vDomainAvailable = max(8-10, 0) = 0 → vTarget = 0 → nSat = 1 → floor 1.
	if opts[1].Split != 1 {
		t.Fatalf("task2 Split = %d, want 1 (domain exhausted floor)", opts[1].Split)
	}
	if opts[1].MinSplitSize != 0 {
		t.Fatalf("task2 MinSplitSize = %d, want 0", opts[1].MinSplitSize)
	}
	tt2 := mustTracked(t, tr, gid2)
	if tt2.ThreadCount != 1 || tt2.TargetBandwidth != 2<<20 || tt2.MinChunk != 0 {
		t.Fatalf("task2 tracker = {threads:%d target:%d minChunk:%d}, want 1/2MiB/0",
			tt2.ThreadCount, tt2.TargetBandwidth, tt2.MinChunk)
	}
}

// --- HLS candidate + service-chain coverage ---

// stubAddTaskHLSPreflight swaps the manifest preflight for one test so the
// service path can be exercised without real HTTP.
func stubAddTaskHLSPreflight(t *testing.T, fn func(ctx context.Context, client *http.Client, url string, headers map[string]string, runCfg *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error)) {
	t.Helper()
	orig := hlsPreflightManifest
	hlsPreflightManifest = fn
	t.Cleanup(func() { hlsPreflightManifest = orig })
}

// successfulHLSPreflight returns a preflight stub that resolves one segment on
// a cross-site host — the classification-domain anchor for assertions.
func successfulHLSPreflight(container, segURL string, estBytes int64) func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
	return func(_ context.Context, _ *http.Client, manifestURL string, _ map[string]string, _ *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
		ext := ".ts"
		if container == hls.ContainerFMP4 {
			ext = ".mp4"
		}
		return &hls.HLSPreflightResult{
			ResumeState: &surgetypes.HLSResumeState{
				ManifestURL:      manifestURL,
				MediaPlaylistURL: manifestURL,
				SegmentRefURL:    segURL,
				ContainerFormat:  container,
				EstimatedBytes:   estBytes,
				RecordEligible:   true,
				TotalSegments:    1,
				CompletedBitmap:  []byte{0},
				Segments:         []surgetypes.HLSSegmentState{{Index: 0, URI: segURL}},
			},
			ContainerExt: ext,
		}, nil
	}
}

// The HLS branch of buildCandidateAddURIOptions is terminal: no size-based
// probe hints, no payload-first fallback — the resume checkpoint is the plan.
func TestBuildCandidateAddURIOptions_HLSBranchIsTerminal(t *testing.T) {
	resume := &surgetypes.HLSResumeState{
		ManifestURL:     "https://cdn.invalid/master.m3u8",
		SegmentRefURL:   "https://seg.invalid/s0.ts",
		ContainerFormat: hls.ContainerTS,
		RecordEligible:  true,
		CompletedBitmap: []byte{0x0f},
		Segments:        []surgetypes.HLSSegmentState{{Index: 0, URI: "https://seg.invalid/s0.ts"}},
	}
	candidate := addTaskCandidate{
		url:       "https://cdn.invalid/master.m3u8",
		out:       "movie.ts",
		sizeBytes: 4096,
		strategy:  surgetypes.StrategyHLS,
		hlsResume: resume,
	}
	opts := buildCandidateAddURIOptions(`D:\Downloads`, "movie.ts", nil, 8, 0, nil, candidate)

	if opts.Strategy != surgetypes.StrategyHLS {
		t.Fatalf("Strategy = %q, want hls", opts.Strategy)
	}
	if opts.FileSize != 0 {
		t.Fatalf("FileSize = %d, want 0", opts.FileSize)
	}
	if !opts.SkipServerProbe {
		t.Fatal("SkipServerProbe = false, want true")
	}
	if opts.RangeAcquisitionMode != surgetypes.RangeAcquireProbeAtEnqueue {
		t.Fatalf("RangeAcquisitionMode = %q, want probe-at-enqueue (empty)", opts.RangeAcquisitionMode)
	}
	if opts.IsExplicitCategory {
		t.Fatal("IsExplicitCategory = true without a download group")
	}
	if opts.HLSResume == nil {
		t.Fatal("HLSResume not forwarded")
	}
	if opts.HLSResume == resume {
		t.Fatal("HLSResume must be cloned, not aliased")
	}
	opts.HLSResume.CompletedBitmap[0] = 0
	opts.HLSResume.Segments[0].URI = "mutated"
	if resume.CompletedBitmap[0] != 0x0f || resume.Segments[0].URI == "mutated" {
		t.Fatal("candidate resume state aliased into engine options")
	}
}

func TestNormalizeHLSCandidate(t *testing.T) {
	t.Run("nil is a no-op", func(t *testing.T) {
		normalizeHLSCandidate(nil)
	})

	t.Run("extracted candidates are never reclassified", func(t *testing.T) {
		c := &addTaskCandidate{url: "https://cdn.invalid/master.m3u8", extracted: true, sizeBytes: 99, externalSizeBytes: 88}
		normalizeHLSCandidate(c)
		if c.strategy == surgetypes.StrategyHLS {
			t.Fatal("extracted candidate must not gain HLS strategy from a .m3u8 URL")
		}
		if c.sizeBytes != 99 || c.externalSizeBytes != 88 {
			t.Fatal("extracted candidate sizes must stay untouched")
		}
	})

	t.Run("explicit strategy wins on any URL", func(t *testing.T) {
		c := &addTaskCandidate{
			url: "https://cdn.invalid/page", strategy: surgetypes.StrategyHLS,
			sizeBytes: 100, externalSizeBytes: 50, hlsResume: &surgetypes.HLSResumeState{SegmentRefURL: "https://seg.invalid/s.ts"},
		}
		resume := c.hlsResume
		normalizeHLSCandidate(c)
		if c.strategy != surgetypes.StrategyHLS || !c.skipHeadProbe {
			t.Fatalf("strategy=%q skipHeadProbe=%v", c.strategy, c.skipHeadProbe)
		}
		if c.sizeBytes != 0 || c.externalSizeBytes != 0 {
			t.Fatal("sizes must be zeroed for HLS")
		}
		if c.hlsResume == resume {
			t.Fatal("resume state must be cloned on normalize")
		}
	})

	t.Run("m3u8 detection on url and finalURL", func(t *testing.T) {
		byURL := &addTaskCandidate{url: "https://cdn.invalid/master.m3u8?token=1", sizeBytes: 7}
		normalizeHLSCandidate(byURL)
		if byURL.strategy != surgetypes.StrategyHLS || !byURL.skipHeadProbe || byURL.sizeBytes != 0 {
			t.Fatalf("url detection failed: %+v", byURL)
		}
		byFinal := &addTaskCandidate{url: "https://cdn.invalid/watch", finalURL: "https://cdn.invalid/play.m3u8", externalSizeBytes: 9}
		normalizeHLSCandidate(byFinal)
		if byFinal.strategy != surgetypes.StrategyHLS || byFinal.externalSizeBytes != 0 {
			t.Fatalf("finalURL detection failed: %+v", byFinal)
		}
		plain := &addTaskCandidate{url: "https://cdn.invalid/file.bin", sizeBytes: 5}
		normalizeHLSCandidate(plain)
		if plain.strategy != "" || plain.sizeBytes != 5 {
			t.Fatalf("non-HLS candidate mutated: %+v", plain)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		src := &surgetypes.HLSResumeState{RecordEligible: true, SegmentRefURL: "https://seg.invalid/s0.ts"}
		c := &addTaskCandidate{url: "https://cdn.invalid/master.m3u8", hlsResume: src}
		normalizeHLSCandidate(c)
		normalizeHLSCandidate(c)
		if c.strategy != surgetypes.StrategyHLS || !c.skipHeadProbe {
			t.Fatal("repeated normalize must keep HLS fields asserted")
		}
		if c.hlsResume == nil || c.hlsResume == src || !c.hlsResume.RecordEligible ||
			c.hlsResume.SegmentRefURL != "https://seg.invalid/s0.ts" {
			t.Fatalf("repeated normalize corrupted the resume state: %+v", c.hlsResume)
		}
	})
}

// A .m3u8 direct add flows Surge-only end to end: preflight result becomes the
// resume checkpoint, the fMP4 container picks .mp4, the segment host owns
// classification, and Aria2 is never touched.
func TestAddUriFromExtension_HLSManifestURLRoutesSurgeOnly(t *testing.T) {
	for _, smartThread := range []bool{false, true} {
		name := "smart_off"
		if smartThread {
			name = "smart_on"
		}
		t.Run(name, func(t *testing.T) {
			aria2Cap := &capturingAria2Engine{}
			surgeCap := &capturingAddURIEngine{}
			hybrid := rpc.NewHybridEngine(aria2Cap, surgeCap)
			tr := admissionSwapTracker(t)
			svc := setupProbeHintsAddService(t, hybrid, smartThread)

			stubAddTaskHLSPreflight(t, successfulHLSPreflight(
				hls.ContainerFMP4, "https://seg-cdn.example.invalid/seg0.m4s", 20<<20))

			gid, err := svc.AddUriFromExtension(extension.DownloadRequest{
				Type: extension.MsgTypeDownload,
				URL:  fmt.Sprintf("https://media.example.invalid/master-%s.m3u8", name),
			})
			if err != nil {
				t.Fatalf("AddUriFromExtension: %v", err)
			}
			if !strings.HasPrefix(gid, "sg_") {
				t.Fatalf("gid = %q, want sg_ prefix", gid)
			}
			if got := len(aria2Cap.snapshotOptions()); got != 0 {
				t.Fatalf("aria2 AddUri calls = %d, want 0", got)
			}
			opts := surgeCap.snapshotOptions()
			if len(opts) != 1 {
				t.Fatalf("surge AddUri calls = %d, want 1", len(opts))
			}
			got := opts[0]
			if got.Strategy != surgetypes.StrategyHLS {
				t.Fatalf("Strategy = %q, want hls", got.Strategy)
			}
			if got.FileSize != 0 || !got.SkipServerProbe {
				t.Fatalf("probe hints leaked into HLS options: %+v", got)
			}
			if got.RangeAcquisitionMode != surgetypes.RangeAcquireProbeAtEnqueue {
				t.Fatalf("RangeAcquisitionMode = %q, want empty", got.RangeAcquisitionMode)
			}
			if got.HLSResume == nil || got.HLSResume.ContainerFormat != hls.ContainerFMP4 {
				t.Fatalf("HLSResume missing preflight result: %+v", got.HLSResume)
			}
			if !strings.HasSuffix(got.Out, ".mp4") {
				t.Fatalf("Out = %q, want .mp4 for fMP4 container", got.Out)
			}
			if scope, domain, _, ok := tr.GetScopeAndEnv(gid); !ok || domain != "seg-cdn.example.invalid" {
				t.Fatalf("classification = {scope:%q domain:%q ok:%v}, want segment host domain", scope, domain, ok)
			}
		})
	}
}

// finalURL .m3u8 detection covers the redirect-to-manifest shape.
func TestAddUriFromExtension_HLSFinalURLDetection(t *testing.T) {
	surgeCap := &capturingAddURIEngine{}
	hybrid := rpc.NewHybridEngine(&capturingAria2Engine{}, surgeCap)
	svc := setupProbeHintsAddService(t, hybrid, false)
	stubAddTaskHLSPreflight(t, successfulHLSPreflight(hls.ContainerTS, "https://seg.invalid/s0.ts", 0))

	gid, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:     extension.MsgTypeDownload,
		URL:      "https://site.invalid/watch?v=1",
		FinalURL: "https://site.invalid/hls/play.m3u8",
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension: %v", err)
	}
	if !strings.HasPrefix(gid, "sg_") {
		t.Fatalf("gid = %q, want sg_", gid)
	}
	opts := surgeCap.snapshotOptions()
	if len(opts) != 1 || opts[0].Strategy != surgetypes.StrategyHLS {
		t.Fatalf("final_url m3u8 not routed as HLS: %+v", opts)
	}
	if !strings.HasSuffix(opts[0].Out, ".ts") {
		t.Fatalf("Out = %q, want .ts", opts[0].Out)
	}
}

// An engine that cannot run Surge must be rejected before preflight, before
// group resolution and before any Aria2 preparation.
func TestAddTaskCandidate_HLSUnsupportedEngineFailsFast(t *testing.T) {
	for _, smartThread := range []bool{false, true} {
		name := "smart_off"
		if smartThread {
			name = "smart_on"
		}
		t.Run(name, func(t *testing.T) {
			stub := &capturingAria2Engine{} // plain engine: no Surge capability
			svc := setupProbeHintsAddService(t, stub, smartThread)

			var preflightCalls int
			stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
				preflightCalls++
				return nil, errors.New("must not be called")
			})

			_, err := svc.AddUriFromExtension(extension.DownloadRequest{
				Type: extension.MsgTypeDownload,
				URL:  fmt.Sprintf("https://cdn.example.invalid/v-%s.m3u8", name),
			})
			if !errors.Is(err, ErrHLSEngineUnsupported) {
				t.Fatalf("err = %v, want ErrHLSEngineUnsupported", err)
			}
			if preflightCalls != 0 {
				t.Fatalf("preflight ran %d times despite the capability gate", preflightCalls)
			}
			if got := len(stub.snapshotOptions()); got != 0 {
				t.Fatalf("AddUri calls = %d, want 0", got)
			}
		})
	}
}

// Transient preflight failures degrade to the fallback resume state instead of
// rejecting the add; permanent sentinels and caller cancellation fail fast.
func TestAddTaskCandidate_HLSPreflightDegradeAndFailFast(t *testing.T) {
	newSvc := func(t *testing.T) (*Service, *capturingAddURIEngine) {
		surgeCap := &capturingAddURIEngine{}
		hybrid := rpc.NewHybridEngine(&capturingAria2Engine{}, surgeCap)
		return setupProbeHintsAddService(t, hybrid, false), surgeCap
	}
	const manifestURL = "https://cdn.example.invalid/degrade.m3u8"

	t.Run("transient error degrades to fallback state", func(t *testing.T) {
		svc, surgeCap := newSvc(t)
		stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
			return nil, errors.New("connection refused")
		})
		gid, err := svc.AddUriFromExtension(extension.DownloadRequest{Type: extension.MsgTypeDownload, URL: manifestURL})
		if err != nil {
			t.Fatalf("transient preflight failure must not reject the add: %v", err)
		}
		if !strings.HasPrefix(gid, "sg_") {
			t.Fatalf("gid = %q, want sg_", gid)
		}
		opts := surgeCap.snapshotOptions()
		if len(opts) != 1 || opts[0].HLSResume == nil {
			t.Fatalf("missing fallback resume: %+v", opts)
		}
		res := opts[0].HLSResume
		if res.ContainerFormat != hls.ContainerTS || !res.RecordEligible {
			t.Fatalf("fallback resume = %+v, want ts + eligible", res)
		}
		if res.SegmentRefURL != manifestURL {
			t.Fatalf("fallback SegmentRefURL = %q, want manifest URL", res.SegmentRefURL)
		}
		if !strings.HasSuffix(opts[0].Out, ".ts") {
			t.Fatalf("Out = %q, want .ts", opts[0].Out)
		}
	})

	t.Run("permanent sentinel fails fast", func(t *testing.T) {
		svc, surgeCap := newSvc(t)
		stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
			return nil, fmt.Errorf("manifest: %w", surgetypes.ErrHLSLiveNotSupported)
		})
		_, err := svc.AddUriFromExtension(extension.DownloadRequest{Type: extension.MsgTypeDownload, URL: "https://cdn.example.invalid/live.m3u8"})
		if !errors.Is(err, surgetypes.ErrHLSLiveNotSupported) {
			t.Fatalf("err = %v, want ErrHLSLiveNotSupported", err)
		}
		if got := len(surgeCap.snapshotOptions()); got != 0 {
			t.Fatalf("AddUri calls = %d, want 0", got)
		}
	})

	t.Run("caller cancellation wins over transient error", func(t *testing.T) {
		svc, surgeCap := newSvc(t)
		stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
			return nil, errors.New("timeout")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		candidate := directAddTaskCandidate("https://cdn.example.invalid/cancelled.m3u8")
		_, err := svc.addTaskCandidate(ctx, candidate, svc.newAddTaskAuthBatchState(), smartthread.NewBandwidthLedger(nil))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if got := len(surgeCap.snapshotOptions()); got != 0 {
			t.Fatalf("AddUri calls = %d, want 0", got)
		}
	})
}

// Engine-side disk admission is the single authority for EstimatedBytes:
// when the Surge boundary refuses with ErrInsufficientDiskSpace the sentinel
// must reach the caller verbatim and the HLS add must never fall back to
// Aria2.
func TestAddTaskCandidate_HLSEstimatedBytesDiskAdmission(t *testing.T) {
	const url = "https://cdn.example.invalid/big.m3u8"

	t.Run("insufficient disk rejects before enqueue", func(t *testing.T) {
		aria2Stub := &diskSpaceStubEngine{}
		surgeStub := &diskSpaceStubEngine{rejectURLs: map[string]struct{}{url: {}}}
		svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Stub, surgeStub), false)
		stubAddTaskHLSPreflight(t, successfulHLSPreflight(hls.ContainerTS, "https://seg.invalid/s0.ts", 100<<20))

		_, err := svc.AddUriFromExtension(extension.DownloadRequest{Type: extension.MsgTypeDownload, URL: url})
		if !errors.Is(err, surgetypes.ErrInsufficientDiskSpace) {
			t.Fatalf("err = %v, want ErrInsufficientDiskSpace", err)
		}
		if got := surgeStub.callCount(); got != 1 {
			t.Fatalf("surge AddUri calls = %d, want 1 (engine boundary owns the refusal)", got)
		}
		if got := aria2Stub.callCount(); got != 0 {
			t.Fatalf("aria2 AddUri calls = %d, want 0 (HLS never falls back)", got)
		}
	})
}
