package extension

import (
	"context"
	"encoding/json"
)

// StubAck is the 291-minimal handler result. Later slices add fields additively
// on the typed wire acks, not by putting URLs or profile refs here.
type StubAck struct {
	ErrorCode string
	Error     string
}

// ExtractorResolver is the host-side resolve seam. Ready is the capability gate.
// HandleResolve does not receive secret generation; adapters that need it
// close over SecretStore.
type ExtractorResolver interface {
	Ready() bool
	HandleResolve(ctx context.Context, env RequestEnvelope, raw json.RawMessage) StubAck
}

// BatchCommitter is the host-side batch-commit seam.
type BatchCommitter interface {
	Ready() bool
	HandleCommit(ctx context.Context, env RequestEnvelope, raw json.RawMessage) StubAck
}

// MatchDigestProvider is reserved for a later digest snapshot. Ready is the
// only method in this slice so the interface can grow without a signature fight.
type MatchDigestProvider interface {
	Ready() bool
}

// ErrorRedactor redacts diagnostic strings for new envelopes only.
// Legacy download_ack.error is never passed through this interface.
type ErrorRedactor interface {
	Redact(err error) string
}

// Linkage is the optional DI bundle injected via SetLinkage. Production in
// this slice leaves it zero-value so extractor caps stay off.
type Linkage struct {
	Resolver  ExtractorResolver
	Digests   MatchDigestProvider
	Committer BatchCommitter
	Redactor  ErrorRedactor
}
