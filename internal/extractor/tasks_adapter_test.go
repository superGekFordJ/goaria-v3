package extractor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/tasks"
)

type fakeDispatcherForAdapter struct {
	mu           sync.Mutex
	resolveFn    func(ctx context.Context, rawURL string) (AddTaskResolution, error)
	headersFn    func(ctx context.Context, item ResolvedAddItem) ([]string, error)
	plansFn      func(ctx context.Context, rawURL string) ([]HostAuthRuntimeRequest, error)
	resolveCalls int
	headerCalls  int
}

func (d *fakeDispatcherForAdapter) Resolve(ctx context.Context, rawURL string) (AddTaskResolution, error) {
	d.mu.Lock()
	d.resolveCalls++
	d.mu.Unlock()
	if d.resolveFn != nil {
		return d.resolveFn(ctx, rawURL)
	}
	return AddTaskResolution{}, nil
}

func (d *fakeDispatcherForAdapter) BuildAria2Headers(ctx context.Context, item ResolvedAddItem) ([]string, error) {
	d.mu.Lock()
	d.headerCalls++
	d.mu.Unlock()
	if d.headersFn != nil {
		return d.headersFn(ctx, item)
	}
	return nil, nil
}

func (d *fakeDispatcherForAdapter) AuthRuntimeRequestsForSource(ctx context.Context, rawURL string) ([]HostAuthRuntimeRequest, error) {
	if d.plansFn != nil {
		return d.plansFn(ctx, rawURL)
	}
	return nil, nil
}

func TestTasksAdapter_NilRuntimePreflightReturnsAvailable(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	result, err := adapter.Preflight(context.Background(), tasks.AuthRequest{})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !result.Available {
		t.Fatalf("Preflight() = %#v, want Available=true for nil runtime", result)
	}
	if result.Matched {
		t.Fatalf("Preflight() = %#v, want Matched=false for nil runtime", result)
	}
	if !result.NoRuntime {
		t.Fatalf("Preflight() = %#v, want NoRuntime=true for nil runtime", result)
	}
}

func TestTasksAdapter_ResolveConvertsToNeutral(t *testing.T) {
	dispatcher := &fakeDispatcherForAdapter{
		resolveFn: func(ctx context.Context, rawURL string) (AddTaskResolution, error) {
			return AddTaskResolution{
				Matched:   true,
				SourceURL: rawURL,
				Items: []ResolvedAddItem{
					{ID: "item-1", URL: "https://example.com/file.bin", Filename: "file.bin", SourceURL: rawURL},
				},
			}, nil
		},
	}
	adapter := NewTasksAdapter(dispatcher, nil)

	resolution, err := adapter.Resolve(context.Background(), "https://example.com/share")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Status != tasks.ResolutionStatusMatched {
		t.Fatalf("Status = %q, want %q", resolution.Status, tasks.ResolutionStatusMatched)
	}
	if len(resolution.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resolution.Items))
	}
	if resolution.Items[0].URL != "https://example.com/file.bin" {
		t.Fatalf("URL = %q, want https://example.com/file.bin", resolution.Items[0].URL)
	}
	if resolution.Items[0].Ref == "" {
		t.Fatal("Ref = empty, want non-empty opaque key")
	}
}

func TestTasksAdapter_ResolveUnmatchedReturnsUnmatched(t *testing.T) {
	dispatcher := &fakeDispatcherForAdapter{
		resolveFn: func(ctx context.Context, rawURL string) (AddTaskResolution, error) {
			return AddTaskResolution{Matched: false, SourceURL: rawURL}, nil
		},
	}
	adapter := NewTasksAdapter(dispatcher, nil)

	resolution, err := adapter.Resolve(context.Background(), "https://example.com/direct")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Status != tasks.ResolutionStatusUnmatched {
		t.Fatalf("Status = %q, want %q", resolution.Status, tasks.ResolutionStatusUnmatched)
	}
}

func TestTasksAdapter_ResolveGenericAuthErrorIsWrapped(t *testing.T) {
	dispatcher := &fakeDispatcherForAdapter{
		resolveFn: func(ctx context.Context, rawURL string) (AddTaskResolution, error) {
			return AddTaskResolution{}, errors.New(emptyExtractOutputError)
		},
	}
	adapter := NewTasksAdapter(dispatcher, nil)

	_, err := adapter.Resolve(context.Background(), "https://example.com/protected")
	if err == nil {
		t.Fatal("Resolve() error = nil, want generic auth error")
	}
	var genericErr *tasks.GenericAuthResolutionError
	if !errors.As(err, &genericErr) {
		t.Fatalf("Resolve() error = %v, want *tasks.GenericAuthResolutionError", err)
	}
}

func TestTasksAdapter_BuildHeadersReturnsDefensiveCopy(t *testing.T) {
	originalHeaders := []string{"Authorization: Bearer token"}
	dispatcher := &fakeDispatcherForAdapter{
		resolveFn: func(ctx context.Context, rawURL string) (AddTaskResolution, error) {
			return AddTaskResolution{
				Matched:   true,
				SourceURL: rawURL,
				Items: []ResolvedAddItem{
					{ID: "item-1", URL: "https://example.com/file.bin", SourceURL: rawURL},
				},
			}, nil
		},
		headersFn: func(ctx context.Context, item ResolvedAddItem) ([]string, error) {
			return originalHeaders, nil
		},
	}
	adapter := NewTasksAdapter(dispatcher, nil)

	resolution, _ := adapter.Resolve(context.Background(), "https://example.com/share")
	headers, err := adapter.BuildHeaders(context.Background(), resolution.Items[0])
	if err != nil {
		t.Fatalf("BuildHeaders() error = %v", err)
	}
	if len(headers) != 1 || headers[0] != "Authorization: Bearer token" {
		t.Fatalf("BuildHeaders() = %#v, want [Authorization: Bearer token]", headers)
	}
	headers[0] = "mutated"
	if originalHeaders[0] == "mutated" {
		t.Fatal("BuildHeaders() returned alias, want defensive copy")
	}
}

func TestTasksAdapter_NewRefreshGuardReturnsValidGuard(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	guard := adapter.NewRefreshGuard()
	if guard == nil {
		t.Fatal("NewRefreshGuard() = nil, want non-nil")
	}
	if !guard.MarkRefreshed("key-1") {
		t.Fatal("MarkRefreshed(key-1) = false, want true on first call")
	}
}

func TestTasksAdapter_RedactErrorDelegatesToRedactSensitive(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	got := adapter.RedactError(errors.New("Authorization: Bearer secret-token token=raw-value"))
	if strings.Contains(got, "secret-token") || strings.Contains(got, "raw-value") {
		t.Fatalf("RedactError() = %q, leaked secret", got)
	}
}

func TestTasksAdapter_ValidateItemAuthPolicyForUnknownRefReturnsError(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	err := adapter.ValidateItemAuthPolicy(tasks.ResolvedItem{Ref: "unknown-ref"})
	if err == nil {
		t.Fatal("ValidateItemAuthPolicy() = nil, want error for unknown ref")
	}
}

func TestTasksAdapter_BuildHeadersForUnknownRefReturnsError(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	_, err := adapter.BuildHeaders(context.Background(), tasks.ResolvedItem{Ref: "unknown-ref"})
	if err == nil {
		t.Fatal("BuildHeaders() = nil, want error for unknown ref")
	}
}

func TestTasksAdapter_AuthRequestsForSourceConvertsToNeutral(t *testing.T) {
	identity := VerifiedPackIdentity{PackID: "xpk-test"}
	manifest := Manifest{PackID: "xpk-test"}
	dispatcher := &fakeDispatcherForAdapter{
		plansFn: func(ctx context.Context, rawURL string) ([]HostAuthRuntimeRequest, error) {
			return []HostAuthRuntimeRequest{
				{PackIdentity: identity, Manifest: manifest, SourceURL: rawURL, ProfileRef: "apr-test"},
			}, nil
		},
	}
	adapter := NewTasksAdapter(dispatcher, nil)

	requests, err := adapter.AuthRequestsForSource(context.Background(), "https://example.com/share")
	if err != nil {
		t.Fatalf("AuthRequestsForSource() error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("Requests = %d, want 1", len(requests))
	}
	if requests[0].PackID != "xpk-test" {
		t.Fatalf("PackID = %q, want xpk-test", requests[0].PackID)
	}
	if requests[0].ProfileRef != "apr-test" {
		t.Fatalf("ProfileRef = %q, want apr-test", requests[0].ProfileRef)
	}
	if requests[0].Ref == "" {
		t.Fatal("Ref = empty, want non-empty opaque key")
	}
}

func TestTasksAdapter_RefreshOnRecoverablePreflightFailureNilRuntimeReturnsZero(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	result, err := adapter.RefreshOnRecoverablePreflightFailure(context.Background(), tasks.AuthRequest{}, nil)
	if err != nil {
		t.Fatalf("RefreshOnRecoverablePreflightFailure() error = %v", err)
	}
	if result.Provisioned || result.Available {
		t.Fatalf("RefreshOnRecoverablePreflightFailure() = %#v, want zero result for nil runtime", result)
	}
}

func TestTasksAdapter_RefreshOnGenericFailureNilRuntimeReturnsZero(t *testing.T) {
	adapter := NewTasksAdapter(&fakeDispatcherForAdapter{}, nil)
	result, err := adapter.RefreshOnGenericFailure(context.Background(), tasks.AuthRequest{}, nil)
	if err != nil {
		t.Fatalf("RefreshOnGenericFailure() error = %v", err)
	}
	if result.Provisioned || result.Available {
		t.Fatalf("RefreshOnGenericFailure() = %#v, want zero result for nil runtime", result)
	}
}
