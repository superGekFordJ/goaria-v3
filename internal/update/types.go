package update

// ReleaseInfo holds metadata from a GitHub Release
type ReleaseInfo struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`       // Release notes (Markdown)
	HTMLURL    string `json:"html_url"`   // Browser URL
	AssetURL   string `json:"asset_url"`  // Direct download URL for the matching asset
	AssetSize  int64  `json:"asset_size"` // Asset size in bytes
	PreRelease bool   `json:"prerelease"`
}

// UpdateResult is returned to the frontend via Wails binding
type UpdateResult struct {
	Available bool          `json:"available"`
	Current   string        `json:"current"`
	Latest    string        `json:"latest"`
	Releases  []ReleaseInfo `json:"releases,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// UpdateStatus constants for event emission
const (
	StatusIdle        = "idle"
	StatusChecking    = "checking"
	StatusAvailable   = "available"
	StatusDownloading = "downloading"
	StatusReady       = "ready"
	StatusError       = "error"
)
