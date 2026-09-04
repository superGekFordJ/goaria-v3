package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// GitHubRepo is the owner/repo for GitHub Releases API
const GitHubRepo = "superGekFordJ/goaria-v3"

var (
	apiBaseURL = "https://api.github.com"
	httpClient = &http.Client{Timeout: 10 * time.Second}
)

func defaultPlatform() string {
	switch {
	case application.System.IsPlatform(application.PlatformWindows):
		return "windows"
	case application.System.IsPlatform(application.PlatformMacOS):
		return "darwin"
	case application.System.IsPlatform(application.PlatformLinux):
		return "linux"
	default:
		return runtime.GOOS
	}
}

var (
	targetGOOS   = defaultPlatform()
	targetGOARCH = runtime.GOARCH
)

// githubAsset represents a single asset in a GitHub Release
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// githubRelease represents the GitHub Releases API response
type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Name       string        `json:"name"`
	Body       string        `json:"body"`
	HTMLURL    string        `json:"html_url"`
	PreRelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

// Check queries GitHub Releases API and compares versions.
// Returns UpdateResult with availability info. Non-nil error only for unexpected failures.
func Check(currentVersion string, includePreRelease bool) (*UpdateResult, error) {
	result := &UpdateResult{
		Current: currentVersion,
	}

	// Parse current version; "dev" builds always report no update
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		result.Latest = currentVersion
		result.Error = "invalid current version: " + currentVersion
		return result, nil
	}

	// Fetch up to 30 releases from GitHub
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", apiBaseURL, GitHubRepo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	req.Header.Set("User-Agent", "GoAria/"+currentVersion)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		result.Error = "network error: " + err.Error()
		return result, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			result.Error = "GitHub API rate limit exceeded, please try again later"
		} else {
			result.Error = fmt.Sprintf("GitHub API returned %d", resp.StatusCode)
		}
		return result, nil
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		result.Error = "failed to parse response: " + err.Error()
		return result, nil
	}

	goos := targetGOOS
	arch := targetGOARCH

	type parsedRelease struct {
		info ReleaseInfo
		ver  *semver.Version
	}
	var parsedList []parsedRelease

	for _, release := range releases {
		// Skip prerelease if not included
		if release.PreRelease && !includePreRelease {
			continue
		}

		// Parse remote version (strip leading 'v')
		remoteTag := strings.TrimPrefix(release.TagName, "v")
		remote, err := semver.NewVersion(remoteTag)
		if err != nil {
			// Skip releases with invalid tag name formatting rather than failing the whole check
			continue
		}

		// Only look for versions strictly newer than current
		if !remote.GreaterThan(current) {
			continue
		}

		// Find matching platform asset
		assetURL, assetSize := matchAsset(release.Assets, goos, arch)
		if assetURL == "" {
			// Skip if no matching asset is found for current OS and architecture
			continue
		}

		parsedList = append(parsedList, parsedRelease{
			info: ReleaseInfo{
				TagName:    release.TagName,
				Name:       release.Name,
				Body:       release.Body,
				HTMLURL:    release.HTMLURL,
				AssetURL:   assetURL,
				AssetSize:  assetSize,
				PreRelease: release.PreRelease,
			},
			ver: remote,
		})
	}

	// Sort matched releases in descending SemVer order (newest first)
	sort.Slice(parsedList, func(i, j int) bool {
		return parsedList[i].ver.GreaterThan(parsedList[j].ver)
	})

	matchedReleases := make([]ReleaseInfo, len(parsedList))
	for i, pr := range parsedList {
		matchedReleases[i] = pr.info
	}

	result.Releases = matchedReleases
	result.Available = len(matchedReleases) > 0

	if len(matchedReleases) > 0 {
		result.Latest = strings.TrimPrefix(matchedReleases[0].TagName, "v")
	} else {
		result.Latest = currentVersion
	}

	return result, nil
}

// hasConflictingArch checks whether assetName explicitly targets an architecture different from currentArch.
func hasConflictingArch(assetName, currentArch string) bool {
	lower := strings.ToLower(assetName)
	currentArch = strings.ToLower(currentArch)

	var conflicts []string
	switch currentArch {
	case "amd64", "x86_64", "x64":
		conflicts = []string{"arm64", "aarch64", "armv8", "armv7", "armv6", "386", "i386"}
	case "arm64", "aarch64", "armv8":
		conflicts = []string{"amd64", "x86_64", "x64", "386", "i386", "armv7", "armv6"}
	default:
		known := []string{"amd64", "x86_64", "x64", "arm64", "aarch64", "386", "i386"}
		for _, k := range known {
			if k != currentArch {
				conflicts = append(conflicts, k)
			}
		}
	}

	for _, c := range conflicts {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

// matchAsset finds the matching asset for the given OS and architecture.
func matchAsset(assets []githubAsset, goos, arch string) (url string, size int64) {
	goos = strings.ToLower(goos)
	arch = strings.ToLower(arch)

	switch goos {
	case "windows":
		pattern := fmt.Sprintf("windows-%s.zip", arch)
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, pattern) ||
				(strings.Contains(lower, "windows") && strings.Contains(lower, arch) && strings.HasSuffix(lower, ".zip")) {
				return a.BrowserDownloadURL, a.Size
			}
		}
		// Fallback: try any .zip asset containing "windows", excluding conflicting architectures
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, "windows") && strings.HasSuffix(lower, ".zip") {
				if !hasConflictingArch(lower, arch) {
					return a.BrowserDownloadURL, a.Size
				}
			}
		}

	case "darwin":
		targetZip := fmt.Sprintf("darwin-%s.zip", arch)
		targetTar := fmt.Sprintf("darwin-%s.tar.gz", arch)
		macosZip := fmt.Sprintf("macos-%s.zip", arch)
		macosTar := fmt.Sprintf("macos-%s.tar.gz", arch)
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, targetZip) || strings.Contains(lower, targetTar) ||
				strings.Contains(lower, macosZip) || strings.Contains(lower, macosTar) {
				return a.BrowserDownloadURL, a.Size
			}
		}
		// Fallback: try any zip or tar.gz containing "darwin" or "macos", excluding conflicting architectures
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if (strings.Contains(lower, "darwin") || strings.Contains(lower, "macos")) &&
				(strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")) {
				if !hasConflictingArch(lower, arch) {
					return a.BrowserDownloadURL, a.Size
				}
			}
		}

	case "linux":
		targetAppImage := fmt.Sprintf("linux-%s.appimage", arch)
		targetTar := fmt.Sprintf("linux-%s.tar.gz", arch)
		// 1. Priority: linux-{arch}.AppImage
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, targetAppImage) ||
				(strings.Contains(lower, "linux") && strings.Contains(lower, arch) && strings.HasSuffix(lower, ".appimage")) {
				return a.BrowserDownloadURL, a.Size
			}
		}
		// 2. Priority: linux-{arch}.tar.gz
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, targetTar) ||
				(strings.Contains(lower, "linux") && strings.Contains(lower, arch) && (strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"))) {
				return a.BrowserDownloadURL, a.Size
			}
		}
		// 3. Fallback: linux AppImage without conflicting architecture
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, "linux") && strings.HasSuffix(lower, ".appimage") {
				if !hasConflictingArch(lower, arch) {
					return a.BrowserDownloadURL, a.Size
				}
			}
		}
		// 4. Fallback: linux tar.gz / tgz without conflicting architecture
		for _, a := range assets {
			lower := strings.ToLower(a.Name)
			if strings.Contains(lower, "linux") && (strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")) {
				if !hasConflictingArch(lower, arch) {
					return a.BrowserDownloadURL, a.Size
				}
			}
		}
	}

	return "", 0
}
