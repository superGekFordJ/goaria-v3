package extension

const (
	DefaultWSPort      = 16801
	PairPathPrefix     = "/__goaria_pair__/"
	PairPagePath       = "/__goaria_pair__/pair.html"
	MsgTypeAuth        = "auth"
	MsgTypeAuthAck     = "auth_ack"
	MsgTypeDownload    = "download"
	MsgTypeDownloadAck = "download_ack"
	MsgTypePing        = "ping"

	MsgTypeExtractorResolve    = "extractor_resolve"
	MsgTypeExtractorResolveAck = "extractor_resolve_ack"
	MsgTypeBatchDownload       = "batch_download"
	MsgTypeBatchDownloadAck    = "batch_download_ack"
	MsgTypeProtocolError       = "protocol_error"
	MsgTypeCapabilityUpdate    = "capability_update"

	ProtocolVersion = 2

	CapRequestID        = "request_id"
	CapExtractorResolve = "extractor.resolve"
	CapExtractorBatch   = "extractor.batch"

	ErrCodeUnsupported         = "unsupported"
	ErrCodeUnavailable         = "unavailable"
	ErrCodeBusy                = "busy"
	ErrCodeInvalidRequest      = "invalid_request"
	ErrCodeIdempotencyConflict = "idempotency_conflict"
)

var WSPortFallbacks = []int{16801, 16802, 16803}

// PairPortFallbacks are tried in order for the short-lived pairing HTTP server.
// Fixed ports avoid OS-random :0 landing on a browser-banned port (e.g. 6000/X11);
// 16810-16814 sit well above the Firefox/Chrome ceiling of 10080.
var PairPortFallbacks = []int{16810, 16811, 16812, 16813, 16814}

// AuthMessage is the first message an extension sends after connecting.
// Empty secret = MVP (server skips validation); non-empty = production.
// ClientVersion and ProtocolVersion are ignored for the allow/deny decision.
type AuthMessage struct {
	Type            string `json:"type"`
	Secret          string `json:"secret"`
	ClientVersion   string `json:"client_version,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
}

// AuthAck is sent after successful (or skipped) auth. Capabilities is never
// omitted so a missing field remains the legacy-host signal.
type AuthAck struct {
	Type            string   `json:"type"`
	ProtocolVersion int      `json:"protocol_version"`
	HostVersion     string   `json:"host_version"`
	Capabilities    []string `json:"capabilities"`
}

// DownloadRequest is sent by the extension to hand off a download.
// Headers follow the aria2 "name: value" line format, matching rpc.AddURIOptions.Headers.
type DownloadRequest struct {
	Type          string   `json:"type"`
	RequestID     string   `json:"request_id,omitempty"`
	URL           string   `json:"url"`
	FinalURL      string   `json:"final_url"`
	Headers       []string `json:"headers"`
	FileSize      int64    `json:"file_size"`
	SkipHeadProbe bool     `json:"skip_head_probe"`
	DedupKey      string   `json:"dedup_key"`
	Filename      string   `json:"filename"`
	DownloadPage  string   `json:"download_page"`
}

// DownloadResponse is the ack returned after a download request is processed.
type DownloadResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	GID       string `json:"gid"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// RequestEnvelope is the thin type+id prefix shared by post-auth messages.
type RequestEnvelope struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
}

// ProtocolError is the unknown-type / generic failure envelope. It must not
// reuse download_ack or auth_ack so legacy FIFO correlation cannot steal it.
type ProtocolError struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error,omitempty"`
}

// TypedAck is the 291-minimal ack for extractor_resolve / batch_download.
type TypedAck struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
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
