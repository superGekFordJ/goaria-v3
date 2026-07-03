package extension

const (
	DefaultWSPort      = 16801
	PairPathPrefix     = "/__goaria_pair__/"
	PairPagePath       = "/__goaria_pair__/pair.html"
	MsgTypeAuth        = "auth"
	MsgTypeDownload    = "download"
	MsgTypeDownloadAck = "download_ack"
)

var WSPortFallbacks = []int{16801, 16802, 16803}

// AuthMessage is the first message an extension sends after connecting.
// Empty secret = MVP (server skips validation); non-empty = production.
type AuthMessage struct {
	Type   string `json:"type"`
	Secret string `json:"secret"`
}

// DownloadRequest is sent by the extension to hand off a download.
// Headers follow the aria2 "name: value" line format, matching rpc.AddURIOptions.Headers.
type DownloadRequest struct {
	Type          string   `json:"type"`
	URL           string   `json:"url"`
	Headers       []string `json:"headers"`
	FileSize      int64    `json:"file_size"`
	SkipHeadProbe bool     `json:"skip_head_probe"`
	DedupKey      string   `json:"dedup_key"`
	Filename      string   `json:"filename"`
	DownloadPage  string   `json:"download_page"`
}

// DownloadResponse is the ack returned after a download request is processed.
type DownloadResponse struct {
	Type    string `json:"type"`
	GID     string `json:"gid"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// TaskAdder is the access-layer contract: extension downloads must go through
// tasks.Service, never directly to rpc.DownloadEngine.
type TaskAdder interface {
	AddUriFromExtension(req DownloadRequest) (string, error)
}

// ExtensionStatus is the status payload returned to the frontend.
type ExtensionStatus struct {
	Status           string `json:"status"`
	WSPort           int    `json:"ws_port"`
	ConnectedClients int    `json:"connected_clients"`
	Paired           bool   `json:"paired"`
}
