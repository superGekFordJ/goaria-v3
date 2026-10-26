package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/strategy/hls"
	surgetypes "goaria-v3/internal/surge/types"
)

// expectedHLSOut mirrors the production naming expression so each test
// asserts the real pipeline output, not a re-derived guess.
func expectedHLSOut(t *testing.T, rawURL, userOut, pageTitle, container string) string {
	t.Helper()
	out, err := SafeAria2OutFilename(hls.ResolveHLSOutputFilename(rawURL, userOut, pageTitle, container, time.Now()))
	if err != nil {
		t.Fatalf("fixture Out resolution failed: %v", err)
	}
	return out
}

// An explicit type:"hls" pins the Surge strategy even when neither url nor
// finalURL carries a manifest suffix — the MIME-only interception shape.
func TestAddUriFromExtension_HLSExplicitTypePinsExtensionless(t *testing.T) {
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
				hls.ContainerTS, "https://seg.invalid/s0.ts", 0))

			const url = "https://cdn.example.invalid/live/edge"
			gid, err := svc.AddUriFromExtension(extension.DownloadRequest{
				Type:      extension.MsgTypeHLS,
				URL:       url,
				PageTitle: "My Show",
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
				t.Fatalf("HLS options must zero size and skip probe: %+v", got)
			}
			if got.HLSResume == nil || got.HLSResume.ContainerFormat != hls.ContainerTS {
				t.Fatalf("HLSResume missing: %+v", got.HLSResume)
			}
			if want := expectedHLSOut(t, url, "", "My Show", hls.ContainerTS); got.Out != want {
				t.Fatalf("Out = %q, want %q", got.Out, want)
			}
			if _, domain, _, ok := tr.GetScopeAndEnv(gid); !ok || domain != "seg.invalid" {
				t.Fatalf("classification domain = %q ok=%v, want seg.invalid", domain, ok)
			}
		})
	}
}

// PageTitle feeds the existing naming branch: generic manifest stems yield
// to the title, a concrete Filename still wins, and missing titles keep the
// URL/host fallback. The container extension follows the preflight result.
func TestAddUriFromExtension_HLSPageTitleNaming(t *testing.T) {
	const base = "https://cdn.example.invalid"
	cases := []struct {
		name      string
		url       string
		filename  string
		title     string
		container string
	}{
		{"generic stem uses title ts", base + "/index.m3u8", "", "Live Show", hls.ContainerTS},
		{"generic stem uses title fmp4", base + "/manifest.m3u8", "", "Live Show", hls.ContainerFMP4},
		{"explicit filename beats title", base + "/index.m3u8", "explicit.mp4", "Live Show", hls.ContainerTS},
		{"missing title falls back to url stem", base + "/showcase.m3u8", "", "", hls.ContainerTS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aria2Cap := &capturingAria2Engine{}
			surgeCap := &capturingAddURIEngine{}
			svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)
			stubAddTaskHLSPreflight(t, successfulHLSPreflight(tc.container, "https://seg.invalid/s0", 0))

			_, err := svc.AddUriFromExtension(extension.DownloadRequest{
				Type:      extension.MsgTypeHLS,
				URL:       tc.url,
				Filename:  tc.filename,
				PageTitle: tc.title,
			})
			if err != nil {
				t.Fatalf("AddUriFromExtension: %v", err)
			}
			opts := surgeCap.snapshotOptions()
			if len(opts) != 1 {
				t.Fatalf("surge AddUri calls = %d, want 1", len(opts))
			}
			want := expectedHLSOut(t, tc.url, tc.filename, tc.title, tc.container)
			if opts[0].Out != want {
				t.Fatalf("Out = %q, want %q", opts[0].Out, want)
			}
			ext := ".ts"
			if tc.container == hls.ContainerFMP4 {
				ext = ".mp4"
			}
			if !strings.HasSuffix(opts[0].Out, ext) {
				t.Fatalf("Out = %q, want %s suffix", opts[0].Out, ext)
			}
		})
	}
}

// The preflight and the final AddURIOptions both receive the extension's
// Cookie/custom headers plus the derived Referer; url and final_url are
// never swapped.
func TestAddUriFromExtension_HLSContextPassthrough(t *testing.T) {
	aria2Cap := &capturingAria2Engine{}
	surgeCap := &capturingAddURIEngine{}
	svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)

	var preflightURL string
	var preflightHeaders map[string]string
	stubAddTaskHLSPreflight(t, func(ctx context.Context, client *http.Client, url string, headers map[string]string, runCfg *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
		preflightURL = url
		preflightHeaders = make(map[string]string, len(headers))
		for k, v := range headers {
			preflightHeaders[k] = v
		}
		return successfulHLSPreflight(hls.ContainerTS, "https://seg.invalid/s0.ts", 0)(ctx, client, url, headers, runCfg)
	})

	const manifestURL = "https://cdn.example.invalid/play.m3u8"
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:         extension.MsgTypeHLS,
		URL:          manifestURL,
		FinalURL:     "https://site.example.invalid/watch?v=1",
		Headers:      []string{"Cookie: sid=1", "X-Custom: v"},
		DownloadPage: "https://page.example.invalid/watch",
	})
	if err != nil {
		t.Fatalf("AddUriFromExtension: %v", err)
	}
	if preflightURL != manifestURL {
		t.Fatalf("preflight url = %q, want the manifest URL (no swap)", preflightURL)
	}
	if preflightHeaders["Cookie"] != "sid=1" || preflightHeaders["X-Custom"] != "v" {
		t.Fatalf("preflight headers = %v", preflightHeaders)
	}
	if preflightHeaders["Referer"] != "https://page.example.invalid/watch" {
		t.Fatalf("preflight headers missing Referer: %v", preflightHeaders)
	}
	opts := surgeCap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("surge AddUri calls = %d", len(opts))
	}
	wantLines := []string{"Cookie: sid=1", "X-Custom: v", "Referer: https://page.example.invalid/watch"}
	for _, line := range wantLines {
		found := false
		for _, h := range opts[0].Headers {
			if h == line {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("options headers missing %q: %v", line, opts[0].Headers)
		}
	}
	if got := len(aria2Cap.snapshotOptions()); got != 0 {
		t.Fatalf("aria2 AddUri calls = %d, want 0", got)
	}
}

// Fail-closed rules for a pinned HLS request: unsupported engines reject
// before preflight, permanent manifest errors reject without any engine add,
// and transient errors still submit only to Surge via the fallback state.
func TestAddUriFromExtension_HLSFailClosed(t *testing.T) {
	t.Run("extensionless pin on unsupported engine", func(t *testing.T) {
		stub := &capturingAria2Engine{}
		svc := setupProbeHintsAddService(t, stub, false)
		var preflightCalls int
		stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
			preflightCalls++
			return nil, errors.New("must not run")
		})
		_, err := svc.AddUriFromExtension(extension.DownloadRequest{
			Type: extension.MsgTypeHLS,
			URL:  "https://cdn.example.invalid/live/edge",
		})
		if !errors.Is(err, ErrHLSEngineUnsupported) {
			t.Fatalf("err = %v, want ErrHLSEngineUnsupported", err)
		}
		if preflightCalls != 0 {
			t.Fatalf("preflight ran %d times on an unsupported engine", preflightCalls)
		}
		if got := len(stub.snapshotOptions()); got != 0 {
			t.Fatalf("AddUri calls = %d, want 0", got)
		}
	})

	t.Run("permanent manifest error rejects before enqueue", func(t *testing.T) {
		aria2Cap := &capturingAria2Engine{}
		surgeCap := &capturingAddURIEngine{}
		svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)
		stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
			return nil, fmt.Errorf("preflight: %w", surgetypes.ErrHLSInvalidManifest)
		})
		_, err := svc.AddUriFromExtension(extension.DownloadRequest{
			Type: extension.MsgTypeHLS,
			URL:  "https://cdn.example.invalid/live/edge",
		})
		if !errors.Is(err, surgetypes.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
		if got := len(surgeCap.snapshotOptions()); got != 0 {
			t.Fatalf("surge AddUri calls = %d, want 0", got)
		}
		if got := len(aria2Cap.snapshotOptions()); got != 0 {
			t.Fatalf("aria2 AddUri calls = %d, want 0", got)
		}
	})

	t.Run("transient preflight error still submits Surge only", func(t *testing.T) {
		aria2Cap := &capturingAria2Engine{}
		surgeCap := &capturingAddURIEngine{}
		svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)
		stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
			return nil, errors.New("connection reset")
		})
		const url = "https://cdn.example.invalid/live/edge2"
		gid, err := svc.AddUriFromExtension(extension.DownloadRequest{
			Type: extension.MsgTypeHLS,
			URL:  url,
		})
		if err != nil {
			t.Fatalf("transient failure must degrade, not reject: %v", err)
		}
		if !strings.HasPrefix(gid, "sg_") {
			t.Fatalf("gid = %q, want sg_", gid)
		}
		opts := surgeCap.snapshotOptions()
		if len(opts) != 1 {
			t.Fatalf("surge AddUri calls = %d, want 1", len(opts))
		}
		if opts[0].HLSResume == nil || !opts[0].HLSResume.RecordEligible {
			t.Fatalf("fallback resume state missing: %+v", opts[0].HLSResume)
		}
		if got := len(aria2Cap.snapshotOptions()); got != 0 {
			t.Fatalf("aria2 AddUri calls = %d, want 0", got)
		}
	})
}

// A plain download request — or a direct Go call with no Type — keeps .m3u
// ordinary even when a valid page_title arrives; the title is naming input
// for HLS only, never a strategy trigger.
func TestAddUriFromExtension_HLSNonHLSBoundary(t *testing.T) {
	for _, reqType := range []string{extension.MsgTypeDownload, ""} {
		name := "type_" + reqType
		if reqType == "" {
			name = "type_empty"
		}
		t.Run(name, func(t *testing.T) {
			aria2Cap := &capturingAria2Engine{}
			surgeCap := &capturingAddURIEngine{}
			svc := setupProbeHintsAddService(t, rpc.NewHybridEngine(aria2Cap, surgeCap), false)
			var preflightCalls int
			stubAddTaskHLSPreflight(t, func(context.Context, *http.Client, string, map[string]string, *surgetypes.RuntimeConfig) (*hls.HLSPreflightResult, error) {
				preflightCalls++
				return nil, errors.New("must not run for .m3u")
			})
			const url = "https://cdn.example.invalid/audio.m3u"
			_, err := svc.AddUriFromExtension(extension.DownloadRequest{
				Type:      reqType,
				URL:       url,
				PageTitle: "Valid Show Title",
			})
			if err != nil {
				t.Fatalf("AddUriFromExtension: %v", err)
			}
			if preflightCalls != 0 {
				t.Fatalf(".m3u triggered %d preflights", preflightCalls)
			}
			opts := surgeCap.snapshotOptions()
			if len(opts) != 1 {
				t.Fatalf("surge AddUri calls = %d, want 1", len(opts))
			}
			got := opts[0]
			if got.Strategy == surgetypes.StrategyHLS || got.HLSResume != nil {
				t.Fatalf(".m3u promoted to HLS: %+v", got)
			}
			if got.Out != "audio.m3u" {
				t.Fatalf("Out = %q, want the ordinary URL-derived name", got.Out)
			}
			if got := len(aria2Cap.snapshotOptions()); got != 0 {
				t.Fatalf("aria2 AddUri calls = %d, want 0 (surge accepted)", got)
			}
		})
	}
}
