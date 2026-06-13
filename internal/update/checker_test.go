package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
)

func TestSemVerComparison(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		remote    string
		wantNewer bool // remote > current
	}{
		{
			name:      "prerelease increment",
			current:   "1.3.0-beta.4",
			remote:    "1.3.0-beta.5",
			wantNewer: true,
		},
		{
			name:      "prerelease less than release",
			current:   "1.3.0-beta.5",
			remote:    "1.3.0",
			wantNewer: true,
		},
		{
			name:      "patch increment",
			current:   "1.3.0",
			remote:    "1.3.1",
			wantNewer: true,
		},
		{
			name:      "same version no update",
			current:   "1.3.0",
			remote:    "1.3.0",
			wantNewer: false,
		},
		{
			name:      "current higher no update",
			current:   "1.4.0",
			remote:    "1.3.0",
			wantNewer: false,
		},
		{
			name:      "minor increment",
			current:   "1.3.0",
			remote:    "1.4.0",
			wantNewer: true,
		},
		{
			name:      "major increment",
			current:   "1.3.0",
			remote:    "2.0.0",
			wantNewer: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, err := semver.NewVersion(tt.current)
			if err != nil {
				t.Fatalf("failed to parse current version %q: %v", tt.current, err)
			}
			rem, err := semver.NewVersion(tt.remote)
			if err != nil {
				t.Fatalf("failed to parse remote version %q: %v", tt.remote, err)
			}

			got := rem.GreaterThan(cur)
			if got != tt.wantNewer {
				t.Errorf("GreaterThan(%s, %s) = %v, want %v", tt.remote, tt.current, got, tt.wantNewer)
			}
		})
	}
}

func TestDevVersionHandling(t *testing.T) {
	// "dev" is not a valid semver — Check should gracefully return an error message, not panic
	result, err := Check("dev", false)
	if err != nil {
		t.Fatalf("Check(\"dev\") returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Check(\"dev\") returned nil result")
	}
	if result.Error == "" {
		t.Error("Check(\"dev\") should have a non-empty Error field for invalid version")
	}
	if result.Available {
		t.Error("Check(\"dev\") should not report an available update")
	}
}

func TestCheckReleases(t *testing.T) {
	// Mock releases response
	mockReleases := []githubRelease{
		{
			TagName:    "v1.3.1",
			Name:       "v1.3.1 Stable",
			PreRelease: false,
			Assets: []githubAsset{
				{Name: "goaria-v1.3.1-windows-amd64.zip", BrowserDownloadURL: "https://example.com/131-amd64.zip", Size: 1000},
				{Name: "goaria-v1.3.1-windows-arm64.zip", BrowserDownloadURL: "https://example.com/131-arm64.zip", Size: 2000},
			},
		},
		{
			TagName:    "v1.4.0-beta.1",
			Name:       "v1.4.0 Beta 1",
			PreRelease: true,
			Assets: []githubAsset{
				{Name: "goaria-v1.4.0-beta.1-windows-amd64.zip", BrowserDownloadURL: "https://example.com/140beta1-amd64.zip", Size: 1500},
			},
		},
		{
			TagName:    "v1.2.0",
			Name:       "v1.2.0 Old Stable",
			PreRelease: false,
			Assets: []githubAsset{
				{Name: "goaria-v1.2.0-windows-amd64.zip", BrowserDownloadURL: "https://example.com/120-amd64.zip", Size: 900},
			},
		},
		{
			TagName:    "invalid-tag-format",
			Name:       "Invalid Tag",
			PreRelease: false,
			Assets: []githubAsset{
				{Name: "goaria-invalid-windows-amd64.zip", BrowserDownloadURL: "https://example.com/invalid.zip", Size: 900},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/repos/superGekFordJ/goaria-v3/releases") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github.v3+json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockReleases)
	}))
	defer server.Close()

	// Backup original baseURL and client, and restore after test
	origBaseURL := apiBaseURL
	origClient := httpClient
	defer func() {
		apiBaseURL = origBaseURL
		httpClient = origClient
	}()

	apiBaseURL = server.URL
	httpClient = server.Client()

	// 1. Check with includePreRelease = false, current = 1.3.0
	res, err := Check("1.3.0", false)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !res.Available {
		t.Error("expected update to be available")
	}
	if res.Latest != "1.3.1" {
		t.Errorf("expected latest version to be 1.3.1, got %q", res.Latest)
	}
	if len(res.Releases) != 1 {
		t.Errorf("expected exactly 1 release, got %d", len(res.Releases))
	} else {
		rel := res.Releases[0]
		if rel.TagName != "v1.3.1" {
			t.Errorf("expected tag v1.3.1, got %q", rel.TagName)
		}
		if rel.PreRelease {
			t.Error("expected non-prerelease tag")
		}
	}

	// 2. Check with includePreRelease = true, current = 1.3.0
	resWithPre, err := Check("1.3.0", true)
	if err != nil {
		t.Fatalf("Check with pre-releases failed: %v", err)
	}
	if !resWithPre.Available {
		t.Error("expected update to be available")
	}
	if resWithPre.Latest != "1.3.1" {
		t.Errorf("expected latest version to be 1.3.1, got %q", resWithPre.Latest)
	}
	if len(resWithPre.Releases) != 2 {
		t.Errorf("expected exactly 2 releases (v1.3.1, v1.4.0-beta.1), got %d", len(resWithPre.Releases))
	}

	// 3. Test rate limiting handling
	rateLimitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // 403 Forbidden indicates rate limit
	}))
	defer rateLimitServer.Close()

	apiBaseURL = rateLimitServer.URL
	httpClient = rateLimitServer.Client()

	resRate, err := Check("1.3.0", false)
	if err != nil {
		t.Fatalf("Check during rate limit failed with unexpected error: %v", err)
	}
	if !strings.Contains(resRate.Error, "rate limit") {
		t.Errorf("expected rate limit error message, got %q", resRate.Error)
	}
}

func TestMatchAsset(t *testing.T) {
	assets := []githubAsset{
		{Name: "goaria-v1.3.1-windows-amd64.zip", BrowserDownloadURL: "https://example.com/amd64.zip", Size: 1000},
		{Name: "goaria-v1.3.1-windows-arm64.zip", BrowserDownloadURL: "https://example.com/arm64.zip", Size: 2000},
		{Name: "goaria-v1.3.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux.tar.gz", Size: 3000},
	}

	t.Run("match amd64", func(t *testing.T) {
		url, size := matchAsset(assets, "amd64")
		if url != "https://example.com/amd64.zip" {
			t.Errorf("expected amd64 URL, got %s", url)
		}
		if size != 1000 {
			t.Errorf("expected size 1000, got %d", size)
		}
	})

	t.Run("match arm64", func(t *testing.T) {
		url, size := matchAsset(assets, "arm64")
		if url != "https://example.com/arm64.zip" {
			t.Errorf("expected arm64 URL, got %s", url)
		}
		if size != 2000 {
			t.Errorf("expected size 2000, got %d", size)
		}
	})

	t.Run("no match for unknown arch", func(t *testing.T) {
		url, size := matchAsset(assets, "mips")
		// Should fallback to any windows zip
		if url == "" {
			t.Error("expected fallback to a windows zip asset")
		}
		_ = size
	})

	t.Run("empty assets", func(t *testing.T) {
		url, size := matchAsset([]githubAsset{}, "amd64")
		if url != "" || size != 0 {
			t.Error("expected empty result for empty assets")
		}
	})
}
