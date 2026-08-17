package types

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestSourceMetadataMismatchError(t *testing.T) {
	err := NewSourceMetadataMismatch(MismatchKind206Total, 8192)
	if !errors.Is(err, ErrSourceMetadataMismatch) {
		t.Fatal("typed mismatch must match ErrSourceMetadataMismatch")
	}
	if !strings.Contains(err.Error(), ErrSourceMetadataMismatch.Error()) {
		t.Fatalf("Error() = %q, want substring %q", err.Error(), ErrSourceMetadataMismatch.Error())
	}

	var got *SourceMetadataMismatchError
	if !errors.As(err, &got) || got == nil {
		t.Fatal("errors.As must recover Kind and ObservedSize")
	}
	if got.Kind != MismatchKind206Total || got.ObservedSize != 8192 {
		t.Fatalf("Kind=%q ObservedSize=%d", got.Kind, got.ObservedSize)
	}

	wrapped := fmt.Errorf("payload-first: %w", err)
	if !errors.Is(wrapped, ErrSourceMetadataMismatch) {
		t.Fatal("%w wrap must still match the sentinel")
	}
	var inner *SourceMetadataMismatchError
	if !errors.As(wrapped, &inner) || inner.Kind != MismatchKind206Total {
		t.Fatal("%w wrap must still expose Kind")
	}
}

func TestIsPermanentHTTPError(t *testing.T) {
	if !IsPermanentHTTPError(ErrPermanentHTTP) {
		t.Errorf("Expected IsPermanentHTTPError(ErrPermanentHTTP) to be true")
	}

	wrappedErr := errors.Join(errors.New("some error"), ErrPermanentHTTP)
	if !IsPermanentHTTPError(wrappedErr) {
		t.Errorf("Expected IsPermanentHTTPError to be true for wrapped ErrPermanentHTTP")
	}

	if IsPermanentHTTPError(errors.New("other error")) {
		t.Errorf("Expected IsPermanentHTTPError to be false for other errors")
	}
}

func TestIsInsufficientDiskSpace_JoinParity(t *testing.T) {
	joined := errors.Join(errors.New("write failed"), ErrInsufficientDiskSpace)
	if !IsInsufficientDiskSpace(joined) {
		t.Fatal("expected Join-wrapped ErrInsufficientDiskSpace to match")
	}
}

func TestIsPermanentHTTPStatus(t *testing.T) {
	tests := []struct {
		status   int
		expected bool
	}{
		{http.StatusOK, false},
		{http.StatusMovedPermanently, false},
		{http.StatusBadRequest, true},           // 400
		{http.StatusUnauthorized, true},         // 401
		{http.StatusForbidden, true},            // 403
		{http.StatusNotFound, true},             // 404
		{http.StatusTooManyRequests, false},     // 429 is explicitly transient
		{http.StatusInternalServerError, false}, // 500
		{http.StatusServiceUnavailable, false},  // 503
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			if got := IsPermanentHTTPStatus(tt.status); got != tt.expected {
				t.Errorf("IsPermanentHTTPStatus(%d) = %v, want %v", tt.status, got, tt.expected)
			}
		})
	}
}
