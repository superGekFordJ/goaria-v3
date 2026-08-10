package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakePortAdapter struct {
	items []ResolvedItem
}

func (a *fakePortAdapter) Resolve(_ context.Context, rawURL string) (Resolution, error) {
	items := make([]ResolvedItem, len(a.items))
	copy(items, a.items)
	return Resolution{
		Status:    ResolutionStatusMatched,
		SourceURL: rawURL,
		Items:     items,
	}, nil
}

func (a *fakePortAdapter) BuildHeaders(_ context.Context, _ ResolvedItem) ([]string, error) {
	return nil, nil
}

func (a *fakePortAdapter) AuthRequestsForSource(_ context.Context, _ string) ([]AuthRequest, error) {
	return nil, nil
}

func (a *fakePortAdapter) Preflight(_ context.Context, _ AuthRequest) (PreflightResult, error) {
	return PreflightResult{Available: true, NoRuntime: true}, nil
}

func (a *fakePortAdapter) RefreshOnRecoverablePreflightFailure(_ context.Context, _ AuthRequest, _ RefreshGuard) (RefreshResult, error) {
	return RefreshResult{}, nil
}

func (a *fakePortAdapter) RefreshOnGenericFailure(_ context.Context, _ AuthRequest, _ RefreshGuard) (RefreshResult, error) {
	return RefreshResult{}, nil
}

func (a *fakePortAdapter) ValidateItemAuthPolicy(_ ResolvedItem) error {
	return nil
}

func (a *fakePortAdapter) NewRefreshGuard() RefreshGuard {
	return nil
}

func (a *fakePortAdapter) RedactError(err error) string {
	if err == nil {
		return ""
	}
	return redactAssignmentValues(err.Error())
}

func TestPort_DefensiveCopyOfResolutionItems(t *testing.T) {
	adapter := &fakePortAdapter{
		items: []ResolvedItem{{ID: "a", URL: "https://example.com/a"}},
	}
	res, err := adapter.Resolve(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(res.Items))
	}
	res.Items[0] = ResolvedItem{ID: "mutated", URL: "https://example.com/mutated"}
	if adapter.items[0].ID != "a" {
		t.Fatalf("internal state mutated: ID = %q, want %q", adapter.items[0].ID, "a")
	}
}

func TestPort_NilAdapterShortCircuitReturnsDirectCandidate(t *testing.T) {
	s := &Service{Adapter: nil}
	candidates, err := s.resolveAddCandidates(context.Background(), "https://example.com/direct", nil)
	if err != nil {
		t.Fatalf("resolveAddCandidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	if candidates[0].extracted {
		t.Fatal("candidate.extracted = true, want false for direct candidate")
	}
	if candidates[0].url != "https://example.com/direct" {
		t.Fatalf("candidate.url = %q, want %q", candidates[0].url, "https://example.com/direct")
	}
}

func TestPort_TypedErrorClassification(t *testing.T) {
	var genericErr *GenericAuthResolutionError
	if !errors.As(&GenericAuthResolutionError{}, &genericErr) {
		t.Fatal("errors.As(*GenericAuthResolutionError) = false, want true")
	}
	plainErr := errors.New("network timeout")
	if errors.As(plainErr, &genericErr) {
		t.Fatal("errors.As(plain error) = true, want false")
	}
}

func TestPort_KeyConstructionIdenticalAndDifferent(t *testing.T) {
	req := AuthRequest{
		PackID:          "xpk-1",
		PackVersion:     "v1",
		AssetSHA256:     "aaa",
		ManifestSHA256:  "bbb",
		PayloadSHA256:   "ccc",
		SignatureSHA256: "ddd",
		PublicKeySHA256: "eee",
		SourceURL:       "https://example.com/src",
		TargetURL:       "https://example.com/dst",
		ProfileRef:      "apr-1",
	}
	key1 := addTaskAuthRuntimeKey(req)
	key2 := addTaskAuthRuntimeKey(req)
	if key1 != key2 {
		t.Fatal("addTaskAuthRuntimeKey not deterministic for identical requests")
	}
	preflightKey1 := addTaskAuthRuntimePreflightKey(req)
	preflightKey2 := addTaskAuthRuntimePreflightKey(req)
	if preflightKey1 != preflightKey2 {
		t.Fatal("addTaskAuthRuntimePreflightKey not deterministic for identical requests")
	}

	req2 := req
	req2.ProfileRef = "apr-2"
	if addTaskAuthRuntimeKey(req) == addTaskAuthRuntimeKey(req2) {
		t.Fatal("addTaskAuthRuntimeKey identical for different ProfileRef")
	}

	req3 := req
	req3.SourceURL = "https://example.com/other"
	if addTaskAuthRuntimePreflightKey(req) == addTaskAuthRuntimePreflightKey(req3) {
		t.Fatal("addTaskAuthRuntimePreflightKey identical for different SourceURL")
	}
}

func TestPort_KeyConstructionEmptyProfileRefMapsToWildcard(t *testing.T) {
	emptyReq := AuthRequest{
		PackID:         "xpk-1",
		PackVersion:    "v1",
		AssetSHA256:    "aaa",
		ManifestSHA256: "bbb",
		PayloadSHA256:  "ccc",
		ProfileRef:     "",
	}
	wildcardReq := emptyReq
	wildcardReq.ProfileRef = "*"
	if addTaskAuthRuntimeKey(emptyReq) != addTaskAuthRuntimeKey(wildcardReq) {
		t.Fatalf("empty ProfileRef key = %q, wildcard key = %q, want equal",
			addTaskAuthRuntimeKey(emptyReq), addTaskAuthRuntimeKey(wildcardReq))
	}
	if !strings.Contains(addTaskAuthRuntimeKey(emptyReq), "*") {
		t.Fatalf("empty ProfileRef key = %q, want it to contain %q",
			addTaskAuthRuntimeKey(emptyReq), "*")
	}
}
