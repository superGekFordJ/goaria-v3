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
	// With descending SemVer sort, v1.4.0-beta.1 must be latest (ahead of v1.3.1)
	if resWithPre.Latest != "1.4.0-beta.1" {
		t.Errorf("expected latest version to be 1.4.0-beta.1, got %q", resWithPre.Latest)
	}
	if len(resWithPre.Releases) != 2 {
		t.Errorf("expected exactly 2 releases (v1.4.0-beta.1, v1.3.1), got %d", len(resWithPre.Releases))
	} else {
		if resWithPre.Releases[0].TagName != "v1.4.0-beta.1" {
			t.Errorf("expected first release to be v1.4.0-beta.1, got %q", resWithPre.Releases[0].TagName)
		}
		if resWithPre.Releases[1].TagName != "v1.3.1" {
			t.Errorf("expected second release to be v1.3.1, got %q", resWithPre.Releases[1].TagName)
		}
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

func TestMatchAssetMatrix(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		arch     string
		assets   []githubAsset
		wantURL  string
		wantSize int64
	}{
		// Windows tests
		{
			name: "windows amd64 exact match",
			goos: "windows",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-windows-amd64.zip", BrowserDownloadURL: "https://example.com/win-amd64.zip", Size: 1000},
				{Name: "goaria-v1.3.1-windows-arm64.zip", BrowserDownloadURL: "https://example.com/win-arm64.zip", Size: 2000},
			},
			wantURL:  "https://example.com/win-amd64.zip",
			wantSize: 1000,
		},
		{
			name: "windows arm64 exact match",
			goos: "windows",
			arch: "arm64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-windows-amd64.zip", BrowserDownloadURL: "https://example.com/win-amd64.zip", Size: 1000},
				{Name: "goaria-v1.3.1-windows-arm64.zip", BrowserDownloadURL: "https://example.com/win-arm64.zip", Size: 2000},
			},
			wantURL:  "https://example.com/win-arm64.zip",
			wantSize: 2000,
		},
		{
			name: "windows fallback to generic windows zip",
			goos: "windows",
			arch: "mips",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux.tar.gz", Size: 3000},
				{Name: "goaria-v1.3.1-windows-universal.zip", BrowserDownloadURL: "https://example.com/win-universal.zip", Size: 4000},
			},
			wantURL:  "https://example.com/win-universal.zip",
			wantSize: 4000,
		},
		{
			name: "windows amd64 rejects conflicting arm64 asset",
			goos: "windows",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-windows-arm64.zip", BrowserDownloadURL: "https://example.com/win-arm64.zip", Size: 2000},
			},
			wantURL:  "",
			wantSize: 0,
		},

		// macOS (darwin) tests
		{
			name: "darwin arm64 zip priority match",
			goos: "darwin",
			arch: "arm64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-darwin-arm64.zip", BrowserDownloadURL: "https://example.com/darwin-arm64.zip", Size: 1100},
				{Name: "goaria-v1.3.1-darwin-amd64.zip", BrowserDownloadURL: "https://example.com/darwin-amd64.zip", Size: 1200},
			},
			wantURL:  "https://example.com/darwin-arm64.zip",
			wantSize: 1100,
		},
		{
			name: "darwin amd64 tar.gz match",
			goos: "darwin",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-darwin-amd64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-amd64.tar.gz", Size: 1300},
				{Name: "goaria-v1.3.1-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-arm64.tar.gz", Size: 1400},
			},
			wantURL:  "https://example.com/darwin-amd64.tar.gz",
			wantSize: 1300,
		},
		{
			name: "darwin macos naming convention match",
			goos: "darwin",
			arch: "arm64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-macos-arm64.zip", BrowserDownloadURL: "https://example.com/macos-arm64.zip", Size: 1500},
			},
			wantURL:  "https://example.com/macos-arm64.zip",
			wantSize: 1500,
		},
		{
			name: "darwin fallback to generic macos zip",
			goos: "darwin",
			arch: "riscv64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-macos-universal.zip", BrowserDownloadURL: "https://example.com/macos-universal.zip", Size: 1600},
			},
			wantURL:  "https://example.com/macos-universal.zip",
			wantSize: 1600,
		},
		{
			name: "darwin amd64 rejects conflicting arm64 asset",
			goos: "darwin",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-darwin-arm64.zip", BrowserDownloadURL: "https://example.com/darwin-arm64.zip", Size: 1100},
			},
			wantURL:  "",
			wantSize: 0,
		},

		// Linux tests
		{
			name: "linux amd64 prefers AppImage over tar.gz",
			goos: "linux",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux-amd64.tar.gz", Size: 2100},
				{Name: "goaria-v1.3.1-linux-amd64.AppImage", BrowserDownloadURL: "https://example.com/linux-amd64.AppImage", Size: 2200},
			},
			wantURL:  "https://example.com/linux-amd64.AppImage",
			wantSize: 2200,
		},
		{
			name: "linux arm64 tar.gz secondary match when no AppImage",
			goos: "linux",
			arch: "arm64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-linux-arm64.tar.gz", BrowserDownloadURL: "https://example.com/linux-arm64.tar.gz", Size: 2300},
			},
			wantURL:  "https://example.com/linux-arm64.tar.gz",
			wantSize: 2300,
		},
		{
			name: "linux fallback to generic linux archive",
			goos: "linux",
			arch: "loong64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-linux-bundle.tar.gz", BrowserDownloadURL: "https://example.com/linux-bundle.tar.gz", Size: 2400},
			},
			wantURL:  "https://example.com/linux-bundle.tar.gz",
			wantSize: 2400,
		},
		{
			name: "linux arm64 rejects conflicting amd64 AppImage",
			goos: "linux",
			arch: "arm64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-linux-amd64.AppImage", BrowserDownloadURL: "https://example.com/linux-amd64.AppImage", Size: 2200},
			},
			wantURL:  "",
			wantSize: 0,
		},
		{
			name: "linux rejects deb or rpm packages",
			goos: "linux",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-linux-amd64.deb", BrowserDownloadURL: "https://example.com/linux-amd64.deb", Size: 2500},
				{Name: "goaria-v1.3.1-linux-amd64.rpm", BrowserDownloadURL: "https://example.com/linux-amd64.rpm", Size: 2600},
			},
			wantURL:  "",
			wantSize: 0,
		},

		// Edge cases
		{
			name:     "empty assets",
			goos:     "linux",
			arch:     "amd64",
			assets:   []githubAsset{},
			wantURL:  "",
			wantSize: 0,
		},
		{
			name: "unsupported OS",
			goos: "freebsd",
			arch: "amd64",
			assets: []githubAsset{
				{Name: "goaria-v1.3.1-freebsd-amd64.tar.gz", BrowserDownloadURL: "https://example.com/freebsd.tar.gz", Size: 5000},
			},
			wantURL:  "",
			wantSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotSize := matchAsset(tt.assets, tt.goos, tt.arch)
			if gotURL != tt.wantURL {
				t.Errorf("matchAsset() URL = %q, want %q", gotURL, tt.wantURL)
			}
			if gotSize != tt.wantSize {
				t.Errorf("matchAsset() size = %d, want %d", gotSize, tt.wantSize)
			}
		})
	}
}

func TestCheckCrossPlatform(t *testing.T) {
	mockReleases := []githubRelease{
		{
			TagName:    "v1.4.0",
			Name:       "v1.4.0 Release",
			PreRelease: false,
			Assets: []githubAsset{
				{Name: "goaria-v1.4.0-windows-amd64.zip", BrowserDownloadURL: "https://example.com/win.zip", Size: 1000},
				{Name: "goaria-v1.4.0-darwin-arm64.zip", BrowserDownloadURL: "https://example.com/darwin.zip", Size: 2000},
				{Name: "goaria-v1.4.0-linux-amd64.AppImage", BrowserDownloadURL: "https://example.com/linux.AppImage", Size: 3000},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.github.v3+json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockReleases)
	}))
	defer server.Close()

	origBaseURL := apiBaseURL
	origClient := httpClient
	origGOOS := targetGOOS
	origGOARCH := targetGOARCH
	defer func() {
		apiBaseURL = origBaseURL
		httpClient = origClient
		targetGOOS = origGOOS
		targetGOARCH = origGOARCH
	}()

	apiBaseURL = server.URL
	httpClient = server.Client()

	// 1. Test Darwin check does NOT return unsupported platform
	targetGOOS = "darwin"
	targetGOARCH = "arm64"
	resDarwin, err := Check("1.3.0", false)
	if err != nil {
		t.Fatalf("Darwin Check failed: %v", err)
	}
	if resDarwin.Error != "" {
		t.Fatalf("Darwin Check returned error: %s", resDarwin.Error)
	}
	if !resDarwin.Available {
		t.Fatal("Darwin Check expected update available")
	}
	if len(resDarwin.Releases) != 1 || resDarwin.Releases[0].AssetURL != "https://example.com/darwin.zip" {
		t.Errorf("Darwin Check unexpected releases: %+v", resDarwin.Releases)
	}

	// 2. Test Linux check does NOT return unsupported platform
	targetGOOS = "linux"
	targetGOARCH = "amd64"
	resLinux, err := Check("1.3.0", false)
	if err != nil {
		t.Fatalf("Linux Check failed: %v", err)
	}
	if resLinux.Error != "" {
		t.Fatalf("Linux Check returned error: %s", resLinux.Error)
	}
	if !resLinux.Available {
		t.Fatal("Linux Check expected update available")
	}
	if len(resLinux.Releases) != 1 || resLinux.Releases[0].AssetURL != "https://example.com/linux.AppImage" {
		t.Errorf("Linux Check unexpected releases: %+v", resLinux.Releases)
	}
}

func TestCheckSemVerDescendingOrder(t *testing.T) {
	// Releases intentionally in non-SemVer order
	mockReleases := []githubRelease{
		{
			TagName: "v1.2.5",
			Assets: []githubAsset{
				{Name: "goaria-v1.2.5-windows-amd64.zip", BrowserDownloadURL: "https://example.com/125.zip", Size: 100},
			},
		},
		{
			TagName: "v1.5.0",
			Assets: []githubAsset{
				{Name: "goaria-v1.5.0-windows-amd64.zip", BrowserDownloadURL: "https://example.com/150.zip", Size: 100},
			},
		},
		{
			TagName: "v1.3.1",
			Assets: []githubAsset{
				{Name: "goaria-v1.3.1-windows-amd64.zip", BrowserDownloadURL: "https://example.com/131.zip", Size: 100},
			},
		},
		{
			TagName: "v1.4.0",
			Assets: []githubAsset{
				{Name: "goaria-v1.4.0-windows-amd64.zip", BrowserDownloadURL: "https://example.com/140.zip", Size: 100},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.github.v3+json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockReleases)
	}))
	defer server.Close()

	origBaseURL := apiBaseURL
	origClient := httpClient
	origGOOS := targetGOOS
	origGOARCH := targetGOARCH
	defer func() {
		apiBaseURL = origBaseURL
		httpClient = origClient
		targetGOOS = origGOOS
		targetGOARCH = origGOARCH
	}()

	apiBaseURL = server.URL
	httpClient = server.Client()
	targetGOOS = "windows"
	targetGOARCH = "amd64"

	res, err := Check("1.2.0", false)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !res.Available {
		t.Fatal("expected update available")
	}
	if res.Latest != "1.5.0" {
		t.Errorf("expected latest to be 1.5.0, got %q", res.Latest)
	}
	if len(res.Releases) != 4 {
		t.Fatalf("expected 4 releases, got %d", len(res.Releases))
	}

	expectedOrder := []string{"v1.5.0", "v1.4.0", "v1.3.1", "v1.2.5"}
	for i, expected := range expectedOrder {
		if res.Releases[i].TagName != expected {
			t.Errorf("release at index %d: expected %s, got %s", i, expected, res.Releases[i].TagName)
		}
	}
}
