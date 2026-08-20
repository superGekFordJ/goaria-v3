package tasks

import (
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/rpc"
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
	for _, h := range headers {
		if h == want {
			return true
		}
	}
	return false
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
