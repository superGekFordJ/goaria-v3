package wailsapp

import (
	"goaria-v3/internal/tasks"
)

type ExtractorSource struct {
	SourceID          string `json:"source_id"`
	Kind              string `json:"kind"`
	DisplayName       string `json:"display_name"`
	PackID            string `json:"pack_id"`
	PackVersion       string `json:"pack_version"`
	SignerFingerprint string `json:"signer_fingerprint"`
	Status            string `json:"status"`
	ErrorCode         string `json:"error_code,omitempty"`
}

type ExtractorState struct {
	Available      bool              `json:"available"`
	Sources        []ExtractorSource `json:"sources"`
	RecoveryErrors []string          `json:"recovery_errors"`
}

type ExtractorOperationResult struct {
	Success   bool           `json:"success"`
	Cancelled bool           `json:"cancelled"`
	ErrorCode string         `json:"error_code,omitempty"`
	State     ExtractorState `json:"state"`
}

const (
	ExtractorSourceKindLocalZip       = "local_zip"
	ExtractorSourceKindLocalDirectory = "local_directory"
	ExtractorSourceKindRemoteLock     = "remote_lock"

	ExtractorSourceStatusReady       = "ready"
	ExtractorSourceStatusUnavailable = "unavailable"

	ExtractorErrorCodeUnavailable            = "unavailable"
	ExtractorErrorCodeInvalidSourceKind      = "invalid_source_kind"
	ExtractorErrorCodeInvalidSourceSpec      = "invalid_source_spec"
	ExtractorErrorCodeInvalidSourceID        = "invalid_source_id"
	ExtractorErrorCodeSourceLimitReached     = "source_limit_reached"
	ExtractorErrorCodeSourceUnreadable       = "source_unreadable"
	ExtractorErrorCodeSourceShapeInvalid     = "source_shape_invalid"
	ExtractorErrorCodeLockMissing            = "lock_missing"
	ExtractorErrorCodeLockInvalid            = "lock_invalid"
	ExtractorErrorCodeHashMismatch           = "hash_mismatch"
	ExtractorErrorCodeSignatureInvalid       = "signature_invalid"
	ExtractorErrorCodeManifestInvalid        = "manifest_invalid"
	ExtractorErrorCodeWASMInvalid            = "wasm_invalid"
	ExtractorErrorCodeRemoteDenied           = "remote_denied"
	ExtractorErrorCodeRemoteFailed           = "remote_failed"
	ExtractorErrorCodePackIDConflict         = "pack_id_conflict"
	ExtractorErrorCodeSignerChanged          = "signer_changed"
	ExtractorErrorCodePackIdentityChanged    = "pack_identity_changed"
	ExtractorErrorCodePolicyUnavailable      = "policy_unavailable"
	ExtractorErrorCodeAuthRuntimeUnavailable = "auth_runtime_unavailable"
	ExtractorErrorCodePersistFailed          = "persist_failed"
	ExtractorErrorCodeConcurrentChange       = "concurrent_change"
	ExtractorErrorCodeStateInvalid           = "state_invalid"
	ExtractorErrorCodeCancelled              = "cancelled"
)

func newEmptyExtractorState(available bool) ExtractorState {
	return ExtractorState{
		Available:      available,
		Sources:        []ExtractorSource{},
		RecoveryErrors: []string{},
	}
}

func newGenericUnavailableResult() ExtractorOperationResult {
	return ExtractorOperationResult{
		Success:   false,
		Cancelled: false,
		ErrorCode: ExtractorErrorCodeUnavailable,
		State:     newEmptyExtractorState(false),
	}
}

type extractorRuntimeProvider interface {
	currentTasksAdapter() tasks.ExtractorAdapter
}

type simpleAdapterProvider struct { //nolint:unused,nolintlint
	adapter tasks.ExtractorAdapter
}

func (p simpleAdapterProvider) currentTasksAdapter() tasks.ExtractorAdapter { //nolint:unused,nolintlint
	return p.adapter
}
