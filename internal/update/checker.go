package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// GitHubRepo is the owner/repo for GitHub Releases API
const GitHubRepo = "superGekFordJ/goaria-v3"

var (
	apiBaseURL = "https://api.github.com"
	httpClient = &http.Client{Timeout: 10 * time.Second}
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

	// Only support Windows for now
	if runtime.GOOS != "windows" {
		result.Latest = currentVersion
		result.Error = "unsupported platform: " + runtime.GOOS
		return result, nil
	}

	// Fetch up to 30 releases from GitHub
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", apiBaseURL, GitHubRepo)
	req, err := http.NewRequest("GET", url, nil)
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

	arch := runtime.GOARCH
	var matchedReleases []ReleaseInfo

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

		// Find matching windows asset
		assetURL, assetSize := matchAsset(release.Assets, arch)
		if assetURL == "" {
			// Skip if no matching windows installer asset is found
			continue
		}

		matchedReleases = append(matchedReleases, ReleaseInfo{
			TagName:    release.TagName,
			Name:       release.Name,
			Body:       release.Body,
			HTMLURL:    release.HTMLURL,
			AssetURL:   assetURL,
			AssetSize:  assetSize,
			PreRelease: release.PreRelease,
		})
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

// matchAsset finds the matching asset for the given architecture.
// Expected naming: goaria-v{version}-windows-{arch}.zip
func matchAsset(assets []githubAsset, arch string) (url string, size int64) {
	pattern := fmt.Sprintf("windows-%s.zip", arch)
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, pattern) {
			return a.BrowserDownloadURL, a.Size
		}
	}
	// Fallback: try any .zip asset containing "windows"
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, "windows") && strings.HasSuffix(lower, ".zip") {
			return a.BrowserDownloadURL, a.Size
		}
	}
	return "", 0
}
