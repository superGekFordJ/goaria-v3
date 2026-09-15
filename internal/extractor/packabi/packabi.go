package packabi

const CurrentABIVersion uint32 = 1

const (
	ABIExportVersion = "goaria_abi_version"
	ABIExportAlloc   = "goaria_alloc"
	ABIExportFree    = "goaria_free"
	ABIExportMatch   = "goaria_match"
	ABIExportExtract = "goaria_extract"
)

const (
	HostImportModule               = "goaria_host"
	HostImportHTTPFetch            = "http_fetch"
	HostImportAuthProfileStatus    = "auth_profile_status"
	HostImportRegisterDownloadAuth = "register_download_auth"
	HostImportHostTime             = "host_time"
)

type Capability string

const (
	CapabilityParseWASM         Capability = "cap.parse.wasm"
	CapabilityHTTPFetch         Capability = "cap.http.fetch"
	CapabilityHTTPFetchExtended Capability = "cap.http.fetch.extended"
	CapabilityAuthProfile       Capability = "cap.auth.profile"
	CapabilityDownloadAuth      Capability = "cap.download.auth"
)

type AuthSecretKind string

const (
	AuthSecretKindBearer AuthSecretKind = "bearer"
	AuthSecretKindCookie AuthSecretKind = "cookie"
)

type MatchInput struct {
	URL string `json:"url"`
}

type MatchOutput struct {
	Matched    bool   `json:"matched"`
	Confidence uint8  `json:"confidence,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ExtractInput struct {
	URL string `json:"url"`
}

type ExtractOutput struct {
	Items []ExtractedItemRef `json:"items"`
}

type ExtractedItemRef struct {
	ID               string            `json:"id,omitempty"`
	URL              string            `json:"url,omitempty"`
	Filename         string            `json:"filename,omitempty"`
	SizeBytes        int64             `json:"size_bytes,omitempty"`
	MimeType         string            `json:"mime_type,omitempty"`
	AuthProfileRef   string            `json:"auth_profile_ref,omitempty"`
	HeaderProfileRef string            `json:"header_profile_ref,omitempty"`
	DownloadAuthRef  string            `json:"download_auth_ref,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type HostHTTPFetchRequest struct {
	Method           string            `json:"method,omitempty"`
	URL              string            `json:"url,omitempty"`
	BrokerPolicyRef  string            `json:"broker_policy_ref,omitempty"`
	EndpointRef      string            `json:"endpoint_ref,omitempty"`
	Params           map[string]string `json:"params,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	BodyBase64       string            `json:"body_base64,omitempty"`
	AuthProfileRef   string            `json:"auth_profile_ref,omitempty"`
	TimeoutMillis    int               `json:"timeout_millis,omitempty"`
	MaxResponseBytes int64             `json:"max_response_bytes,omitempty"`
	// OmitBrowserContext opts a self-authenticated fetch out of browser
	// grant/cookie matching and typed browser fields.
	OmitBrowserContext bool `json:"omit_browser_context,omitempty"`
}

type HostHTTPFetchResponse struct {
	OK         bool                `json:"ok"`
	StatusCode int                 `json:"status_code,omitempty"`
	FinalURL   string              `json:"final_url,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	BodyBase64 string              `json:"body_base64,omitempty"`
	ErrorCode  string              `json:"error_code,omitempty"`
	Message    string              `json:"message,omitempty"`
}

type HostAuthProfileStatusRequest struct {
	AuthProfileRef  string            `json:"auth_profile_ref"`
	URL             string            `json:"url,omitempty"`
	BrokerPolicyRef string            `json:"broker_policy_ref,omitempty"`
	EndpointRef     string            `json:"endpoint_ref,omitempty"`
	Params          map[string]string `json:"params,omitempty"`
}

type HostAuthProfileStatusResponse struct {
	OK              bool           `json:"ok"`
	Available       bool           `json:"available,omitempty"`
	Kind            AuthSecretKind `json:"kind,omitempty"`
	RedactedDisplay string         `json:"redacted_display,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
	Message         string         `json:"message,omitempty"`
}

type HostRegisterDownloadAuthRequest struct {
	Kind  string `json:"kind"`
	Token string `json:"token"`
}

type HostRegisterDownloadAuthResponse struct {
	OK              bool   `json:"ok"`
	DownloadAuthRef string `json:"download_auth_ref,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	Message         string `json:"message,omitempty"`
}

type HostTimeResponse struct {
	OK        bool   `json:"ok"`
	UnixSecs  int64  `json:"unix_secs,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func PackResult(ptr, length uint32) uint64 {
	return uint64(ptr)<<32 | uint64(length)
}

func UnpackResult(result uint64) (uint32, uint32) {
	return uint32(result >> 32), uint32(result)
}
