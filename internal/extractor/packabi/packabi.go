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
	HostImportModule            = "goaria_host"
	HostImportHTTPFetch         = "http_fetch"
	HostImportAuthProfileStatus = "auth_profile_status"
)

type Capability string

const (
	CapabilityParseWASM   Capability = "cap.parse.wasm"
	CapabilityHTTPFetch   Capability = "cap.http.fetch"
	CapabilityAuthProfile Capability = "cap.auth.profile"
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
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type HostHTTPFetchRequest struct {
	Method           string            `json:"method,omitempty"`
	URL              string            `json:"url"`
	Headers          map[string]string `json:"headers,omitempty"`
	AuthProfileRef   string            `json:"auth_profile_ref,omitempty"`
	TimeoutMillis    int               `json:"timeout_millis,omitempty"`
	MaxResponseBytes int64             `json:"max_response_bytes,omitempty"`
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
	AuthProfileRef string `json:"auth_profile_ref"`
	URL            string `json:"url"`
}

type HostAuthProfileStatusResponse struct {
	OK              bool           `json:"ok"`
	Available       bool           `json:"available,omitempty"`
	Kind            AuthSecretKind `json:"kind,omitempty"`
	RedactedDisplay string         `json:"redacted_display,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
	Message         string         `json:"message,omitempty"`
}

func PackResult(ptr, length uint32) uint64 {
	return uint64(ptr)<<32 | uint64(length)
}

func UnpackResult(result uint64) (uint32, uint32) {
	return uint32(result >> 32), uint32(result)
}
