package tasks

import "context"

type ResolutionStatus string

const (
	ResolutionStatusUnmatched ResolutionStatus = "unmatched"
	ResolutionStatusMatched   ResolutionStatus = "matched"
)

// ResolvedItem is a neutral DTO for a single resolved download item.
// Ref is an opaque adapter-internal key; tasks never inspects it and
// passes it back to adapter methods for full-item lookup.
type ResolvedItem struct {
	Ref              string
	ID               string
	SourceURL        string
	URL              string
	Filename         string
	SizeBytes        int64
	AuthProfileRef   string
	HeaderProfileRef string
	PackID           string
	PackVersion      string
	AssetSHA256      string
	ManifestSHA256   string
	PayloadSHA256    string
	SignatureSHA256  string
	PublicKeySHA256  string
}

// Resolution is the neutral result of resolving a URL through the extractor.
type Resolution struct {
	Status    ResolutionStatus
	SourceURL string
	Items     []ResolvedItem
}

// AuthRequest is a neutral preflight/refresh request carrying pack identity
// strings and URLs. Ref is an opaque adapter-internal key.
type AuthRequest struct {
	Ref             string
	PackID          string
	PackVersion     string
	AssetSHA256     string
	ManifestSHA256  string
	PayloadSHA256   string
	SignatureSHA256 string
	PublicKeySHA256 string
	SourceURL       string
	TargetURL       string
	ProfileRef      string
}

// PreflightResult is the neutral result of an auth preflight check.
// Available already incorporates the all-profiles-available check.
// NoRuntime is true when the adapter has no runtime at all; callers use
// it to distinguish "no runtime" from "runtime present but no match".
type PreflightResult struct {
	Matched     bool
	Required    bool
	Available   bool
	Refreshable bool
	NoRuntime   bool
}

// RefreshResult is the neutral result of an auth refresh attempt.
// Available already incorporates the all-profiles-available check.
type RefreshResult struct {
	Provisioned bool
	Available   bool
}

// RefreshGuard deduplicates refresh attempts within a batch at the runtime level.
type RefreshGuard interface {
	MarkRefreshed(key string) bool
}

// GenericAuthResolutionError is a typed error replacing string-comparison
// classification of generic auth resolution failures.
type GenericAuthResolutionError struct{}

func (e *GenericAuthResolutionError) Error() string {
	return "could not resolve this link; authentication may be required or the link is unsupported"
}

// ExtractorAdapter is the neutral capability boundary for extractor-based
// add-task resolution, auth preflight/refresh, header materialization,
// policy validation, and error redaction.
type ExtractorAdapter interface {
	Resolve(ctx context.Context, rawURL string) (Resolution, error)
	BuildHeaders(ctx context.Context, item ResolvedItem) ([]string, error)
	AuthRequestsForSource(ctx context.Context, rawURL string) ([]AuthRequest, error)
	Preflight(ctx context.Context, request AuthRequest) (PreflightResult, error)
	RefreshOnRecoverablePreflightFailure(ctx context.Context, request AuthRequest, guard RefreshGuard) (RefreshResult, error)
	RefreshOnGenericFailure(ctx context.Context, request AuthRequest, guard RefreshGuard) (RefreshResult, error)
	ValidateItemAuthPolicy(item ResolvedItem) error
	NewRefreshGuard() RefreshGuard
	RedactError(err error) string
}
