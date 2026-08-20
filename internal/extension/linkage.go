package extension

import (
	"context"
	"encoding/json"
)

// ResolveDisplayItem is the URL-free item shown on a successful resolve ack.
type ResolveDisplayItem struct {
	ItemID    string
	Filename  string
	SizeBytes int64
	MimeType  string
}

// ResolveResult is the host-side resolve outcome. Display fields only.
type ResolveResult struct {
	ErrorCode  string
	Error      string
	Matched    bool
	SessionID  string
	TotalCount int
	TotalBytes int64
	Items      []ResolveDisplayItem
}

// ExtractorResolver is the host-side resolve seam. Ready is the capability gate.
// HandleResolve does not receive secret generation; adapters that need it
// close over SecretStore.
type ExtractorResolver interface {
	Ready() bool
	HandleResolve(ctx context.Context, env RequestEnvelope, raw json.RawMessage) ResolveResult
	Invalidate()
	RewriteCachedResolve(cached []byte) []byte
}

// CommitResult is the host-side batch-commit outcome. Keys are item_id only.
type CommitResult struct {
	ErrorCode        string
	Error            string
	Success          bool
	GroupKey         string
	SucceededItemIDs []string
	DuplicateItemIDs []string
	ErrorsByItemID   map[string]string
	SkipIdempotency  bool `json:"-"`
}

// BatchCommitter is the host-side batch-commit seam.
type BatchCommitter interface {
	Ready() bool
	HandleCommit(ctx context.Context, env RequestEnvelope, raw json.RawMessage) CommitResult
}

// MatchDigestProvider supplies a per-connection salted ingress digest snapshot.
// Ready is independent of computeCapabilities; Snapshot is invoked only when
// extractor.resolve is already in the computed cap list.
type MatchDigestProvider interface {
	Ready() bool
	Snapshot() (MatchDigestSnapshot, bool)
}

// MatchDigestSnapshot is the host-free digest payload mapped onto MatchDigestWire.
type MatchDigestSnapshot struct {
	Version          int
	Salt             string
	ExactDigests     []string
	SubdomainDigests []string
}

// ErrorRedactor redacts diagnostic strings for new envelopes only.
// Legacy download_ack.error is never passed through this interface.
type ErrorRedactor interface {
	Redact(err error) string
}

// Linkage is the optional DI bundle injected via SetLinkage after NewServer.
type Linkage struct {
	Resolver  ExtractorResolver
	Digests   MatchDigestProvider
	Committer BatchCommitter
	Redactor  ErrorRedactor
}
