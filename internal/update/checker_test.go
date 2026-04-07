package update

import (
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
	result, err := Check("dev")
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
