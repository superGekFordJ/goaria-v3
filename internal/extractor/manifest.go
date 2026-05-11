package extractor

const CurrentABIVersion uint32 = 1

type Capability string

const (
	CapabilityParseWASM   Capability = "cap.parse.wasm"
	CapabilityHTTPFetch   Capability = "cap.http.fetch"
	CapabilityAuthProfile Capability = "cap.auth.profile"
)

type Manifest struct {
	PackID           string         `json:"pack_id"`
	PackVersion      string         `json:"pack_version"`
	ABIVersion       uint32         `json:"abi_version"`
	Capabilities     []Capability   `json:"capabilities"`
	Domains          []DomainRule   `json:"domains"`
	DomainPolicyRefs []string       `json:"domain_policy_refs,omitempty"`
	BrokerPolicyRefs []string       `json:"broker_policy_refs,omitempty"`
	ResourceLimits   ResourceLimits `json:"resource_limits"`
	PayloadSHA256    string         `json:"payload_sha256"`
	Description      string         `json:"description,omitempty"`
}

type DomainRule struct {
	Host              string `json:"host"`
	IncludeSubdomains bool   `json:"include_subdomains,omitempty"`
}

type ResourceLimits struct {
	TimeoutMillis    int    `json:"timeout_millis"`
	MaxMemoryPages   uint32 `json:"max_memory_pages"`
	MaxHostCalls     uint32 `json:"max_host_calls"`
	MaxResponseBytes int64  `json:"max_response_bytes"`
	MaxOutputItems   uint32 `json:"max_output_items"`
	MaxOutputBytes   int64  `json:"max_output_bytes"`
}
