package types

import (
	"errors"
	"fmt"
	"net/http"
)

// Common errors
var (
	ErrPaused             = errors.New("download paused")
	ErrNotFound           = errors.New("download not found")
	ErrCompleted          = errors.New("download already completed")
	ErrPausing            = errors.New("download is still pausing, try again in a moment")
	ErrEngineNotInit      = errors.New("engine not initialized")
	ErrPoolNotInit        = errors.New("worker pool not initialized")
	ErrIDExists           = errors.New("download id already exists")
	ErrURLRequired        = errors.New("URL is required")
	ErrDestRequired       = errors.New("destination path is required")
	ErrServiceUnavailable = errors.New("service unavailable")
	ErrQueuedUpdate       = errors.New("cannot update URL for a queued download, please cancel or wait for it to start")
	ErrActiveUpdate       = errors.New("download is currently active, please pause it before updating the URL")
	ErrMaxRedirects       = errors.New("stopped after 10 redirects")
	ErrAlreadyActive      = errors.New("download is already active or queued")

	// ErrPermanentHTTP indicates the server returned a 4xx status (other than 429)
	// that makes retrying pointless (e.g. 403 Forbidden, 404 Not Found, 401 Unauthorized).
	ErrPermanentHTTP = errors.New("permanent HTTP error")

	// ErrInsufficientDiskSpace indicates the destination volume cannot hold
	// the download (or the process quota is exhausted). First-class producers:
	// enqueue precheck returns this bare sentinel when FileSize is known and
	// free space is insufficient; write/preallocate paths annotate the same
	// sentinel on ENOSPC / EDQUOT / ERROR_DISK_FULL / ERROR_DISK_QUOTA_EXCEEDED.
	// Retrying and Truncate fallbacks must not run when this sentinel matches.
	ErrInsufficientDiskSpace = errors.New("insufficient disk space")

	// FORK-PATCH: HTTP 200 that ignored Range with Content-Length == trusted size.
	// Zero payload write. Immediate return (no generic retry). Scheduler may
	// Truncate+single only while still payload-first at 0 verified bytes.
	ErrRangeUnsupported = errors.New("source ignored range request")

	// FORK-PATCH: Range metadata does not match the trusted size / requested
	// shard (bad/missing Content-Range, 206 total mismatch, 416, length wrong).
	// Zero payload write. No auto single fallback. Terminal at whole-download retry.
	ErrSourceMetadataMismatch = errors.New("source metadata mismatch")

	// FORK-PATCH: payload-first persist of RangeSupported failed. No body
	// write, no residual requeue, no whole-download retry.
	ErrPayloadFirstPersist = errors.New("payload-first persist failed")
)

// PayloadFirstMismatchKind classifies why payload-first headers failed the
// Range contract. Only 206_total and 416_star_total are size hypotheses.
type PayloadFirstMismatchKind string

const (
	MismatchKind206Total     PayloadFirstMismatchKind = "206_total"
	MismatchKind416StarTotal PayloadFirstMismatchKind = "416_star_total"
	MismatchKind200CL        PayloadFirstMismatchKind = "200_cl"
	MismatchKind200Chunked   PayloadFirstMismatchKind = "200_chunked"
	MismatchKind206Star      PayloadFirstMismatchKind = "206_star"
	MismatchKind416Bare      PayloadFirstMismatchKind = "416_bare"
	MismatchKindMultipart    PayloadFirstMismatchKind = "multipart"
)

// FORK-PATCH: SourceMetadataMismatchError wraps ErrSourceMetadataMismatch
// with Kind and ObservedSize so the scheduler can size-heal without scraping.
type SourceMetadataMismatchError struct {
	Kind         PayloadFirstMismatchKind
	ObservedSize int64
}

func NewSourceMetadataMismatch(kind PayloadFirstMismatchKind, observedSize int64) *SourceMetadataMismatchError {
	return &SourceMetadataMismatchError{Kind: kind, ObservedSize: observedSize}
}

func (e *SourceMetadataMismatchError) Error() string {
	if e == nil {
		return ErrSourceMetadataMismatch.Error()
	}
	if e.Kind == "" && e.ObservedSize == 0 {
		return ErrSourceMetadataMismatch.Error()
	}
	return fmt.Sprintf("%s: kind=%s observed=%d", ErrSourceMetadataMismatch.Error(), e.Kind, e.ObservedSize)
}

func (e *SourceMetadataMismatchError) Unwrap() error {
	return ErrSourceMetadataMismatch
}

// IsPermanentHTTPError reports whether err is a permanent HTTP error that
// should not be retried by the scheduler.
func IsPermanentHTTPError(err error) bool {
	return errors.Is(err, ErrPermanentHTTP)
}

// IsPermanentHTTPStatus reports whether the given HTTP status code represents
// a permanent failure that should not be retried.
// 429 Too Many Requests is explicitly excluded because it is transient.
func IsPermanentHTTPStatus(code int) bool {
	return code >= http.StatusBadRequest &&
		code < http.StatusInternalServerError &&
		code != http.StatusTooManyRequests
}
