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

	MatchDigestVersion = 1

	CapRequestID        = "request_id"
	CapExtractorResolve = "extractor.resolve"
	CapExtractorBatch   = "extractor.batch"

	ErrCodeUnsupported         = "unsupported"
	ErrCodeUnavailable         = "unavailable"
	ErrCodeBusy                = "busy"
	ErrCodeInvalidRequest      = "invalid_request"
	ErrCodeIdempotencyConflict = "idempotency_conflict"
	ErrCodeAuthExpired         = "auth_expired"
	ErrCodeTimeout             = "timeout"
	ErrCodePackError           = "pack_error"
	ErrCodeSessionExpired      = "session_expired"

	MaxResolveSessionItems = 128

	CommitItemErrorNotAllowed = "item is not allowed"
	CommitItemErrorAddFailed  = "add failed"
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
	Type            string           `json:"type"`
	ProtocolVersion int              `json:"protocol_version"`
	HostVersion     string           `json:"host_version"`
	Capabilities    []string         `json:"capabilities"`
	Match           *MatchDigestWire `json:"match,omitempty"`
}

// MatchDigestWire is the optional nested match object on auth_ack.
// Slice fields omit omitempty so empty sets marshal as [] not null/absent.
type MatchDigestWire struct {
	DigestVersion    int      `json:"digest_version"`
	Salt             string   `json:"salt"`
	ExactDigests     []string `json:"exact_digests"`
	SubdomainDigests []string `json:"subdomain_digests"`
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

// TypedAck is the overlay ack for busy/conflict/unavailable and whole-request
// failures that do not carry item lists.
type TypedAck struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BatchDownloadRequest is the inbound batch_download payload. It must not
// carry output URLs, cookies, or profile refs.
type BatchDownloadRequest struct {
	Type        string   `json:"type"`
	RequestID   string   `json:"request_id,omitempty"`
	SessionID   string   `json:"session_id"`
	ItemIDs     []string `json:"item_ids"`
	CreateGroup bool     `json:"create_group,omitempty"`
	FolderName  string   `json:"folder_name,omitempty"`
}

// BatchDownloadAck is the batch_download success/partial wire ack.
// Whole-request errors omit item id arrays and errors_by_item_id.
type BatchDownloadAck struct {
	Type             string            `json:"type"`
	RequestID        string            `json:"request_id,omitempty"`
	Success          bool              `json:"success"`
	GroupKey         string            `json:"group_key,omitempty"`
	SucceededItemIDs []string          `json:"succeeded_item_ids,omitempty"`
	DuplicateItemIDs []string          `json:"duplicate_item_ids,omitempty"`
	ErrorsByItemID   map[string]string `json:"errors_by_item_id,omitempty"`
	ErrorCode        string            `json:"error_code,omitempty"`
	Error            string            `json:"error,omitempty"`
}

// BrowserCookie is the structured cookie DTO on extractor_resolve.
// HostOnly and Secure are required; JSON null or missing is invalid_request.
type BrowserCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   *bool  `json:"secure"`
	HostOnly *bool  `json:"host_only"`
}

// ExtractorResolveRequest is the inbound extractor_resolve payload.
type ExtractorResolveRequest struct {
	Type           string          `json:"type"`
	RequestID      string          `json:"request_id,omitempty"`
	SourceURL      string          `json:"source_url"`
	Cookies        []BrowserCookie `json:"cookies"`
	UserAgent      string          `json:"user_agent,omitempty"`
	AcceptLanguage string          `json:"accept_language,omitempty"`
	Referer        string          `json:"referer,omitempty"`
}

// ExtractorResolveAckItem is display-only; it must never carry URLs or refs.
type ExtractorResolveAckItem struct {
	ItemID    string `json:"item_id"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type"`
}

// ExtractorResolveAck is the resolve wire ack. Matched is omitted on errors.
type ExtractorResolveAck struct {
	Type       string                    `json:"type"`
	RequestID  string                    `json:"request_id,omitempty"`
	Matched    *bool                     `json:"matched,omitempty"`
	SessionID  string                    `json:"session_id,omitempty"`
	TotalCount int                       `json:"total_count,omitempty"`
	TotalBytes int64                     `json:"total_bytes,omitempty"`
	Items      []ExtractorResolveAckItem `json:"items"`
	ErrorCode  string                    `json:"error_code,omitempty"`
	Error      string                    `json:"error,omitempty"`
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
