package tasks

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
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
