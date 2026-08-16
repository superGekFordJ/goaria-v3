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

func TestAddUriFromExtension_TrustedSizeFilenameSetsSequentialSkip(t *testing.T) {
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
				if got.SupportsRange == nil || *got.SupportsRange {
					t.Fatalf("SupportsRange = %v, want pointer to false", ptrBool(got.SupportsRange))
				}
			})
		}
	}
}

func TestAddUriFromExtension_MissingFilenameLeavesRangeNil(t *testing.T) {
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
		if opts[0].FileSize != 0 || opts[0].SupportsRange != nil {
			t.Fatalf("FileSize=%d SupportsRange=%v, want 0/nil", opts[0].FileSize, ptrBool(opts[0].SupportsRange))
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
		if err == nil {
			t.Fatal("expected sanitization error for whitespace filename")
		}
		if n := len(cap.snapshotOptions()); n != 0 {
			t.Fatalf("AddUri calls = %d, want 0", n)
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
	if opts[0].FileSize != 0 || opts[0].SupportsRange != nil {
		t.Fatalf("FileSize=%d SupportsRange=%v, want 0/nil", opts[0].FileSize, ptrBool(opts[0].SupportsRange))
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
	if opts[0].SupportsRange == nil || *opts[0].SupportsRange {
		t.Fatalf("SupportsRange = %v, want pointer to false", ptrBool(opts[0].SupportsRange))
	}
}

func TestAddUri_TrustedProbeExtractorLeavesRangeNil(t *testing.T) {
	cap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, cap, false)
	shareURL := "https://gofile.io/d/probe-hints"
	svc.Adapter = &fakePortAdapter{items: []ResolvedItem{{
		SourceURL: shareURL,
		URL:       "https://cdn.gofile.io/file.bin",
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
	if opts[0].FileSize != 0 || opts[0].SupportsRange != nil {
		t.Fatalf("extractor FileSize=%d SupportsRange=%v, want 0/nil", opts[0].FileSize, ptrBool(opts[0].SupportsRange))
	}
	if opts[0].Out != "file.bin" {
		t.Fatalf("Out = %q, want file.bin", opts[0].Out)
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
