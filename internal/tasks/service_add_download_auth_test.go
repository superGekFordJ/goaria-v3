package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type downloadAuthFakeAdapter struct {
	fakePortAdapter
	validateErr error
	builtItems  []ResolvedItem
}

func (a *downloadAuthFakeAdapter) ValidateItemAuthPolicy(item ResolvedItem) error {
	return a.validateErr
}

func (a *downloadAuthFakeAdapter) BuildHeaders(_ context.Context, item ResolvedItem) ([]string, error) {
	a.builtItems = append(a.builtItems, item)
	return []string{"Authorization: Bearer materialized-token"}, nil
}

func downloadAuthCandidate(external []string) addTaskCandidate {
	item := ResolvedItem{
		Ref:             "r-dlauth-1",
		URL:             "https://files.fixture.invalid/file.bin",
		DownloadAuthRef: "dar-" + strings.Repeat("a", 32),
		PackID:          "xpk-alpha001",
	}
	candidate := extractorAddTaskCandidate(item)
	candidate.externalHeaders = external
	return candidate
}

func TestExtractorAddTaskCandidateProtectsDownloadAuthItem(t *testing.T) {
	candidate := downloadAuthCandidate(nil)
	if !candidate.protected {
		t.Fatal("candidate.protected = false, want download-auth item to skip unauthenticated HEAD probe")
	}
}

func TestBuildCandidateHeadersRejectsExternalAuthConflict(t *testing.T) {
	service := &Service{Adapter: &downloadAuthFakeAdapter{}}
	cases := []struct {
		name    string
		headers []string
	}{
		{name: "authorization", headers: []string{"Authorization: Bearer external"}},
		{name: "authorization case-insensitive", headers: []string{"AUTHORIZATION: Bearer external"}},
		{name: "cookie", headers: []string{"Cookie: sid=external"}},
		{name: "cookie2", headers: []string{"cookie2: sid=external"}},
		{name: "mixed benign and conflict", headers: []string{"Referer: https://ok", "authorization: Bearer x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.buildCandidateHeaders(context.Background(), downloadAuthCandidate(tc.headers))
			if err == nil {
				t.Fatal("buildCandidateHeaders() error = nil, want external auth conflict rejection")
			}
		})
	}
}

func TestBuildCandidateHeadersAllowsBenignExternalHeaders(t *testing.T) {
	service := &Service{Adapter: &downloadAuthFakeAdapter{}}
	headers, err := service.buildCandidateHeaders(context.Background(), downloadAuthCandidate([]string{"Referer: https://ok"}))
	if err != nil {
		t.Fatalf("buildCandidateHeaders() error = %v", err)
	}
	if len(headers) != 2 {
		t.Fatalf("headers = %#v, want merged referer + materialized bearer", headers)
	}
}

func TestPreflightCandidateAuthFailsClosedWithoutAdapter(t *testing.T) {
	service := &Service{}
	cases := []struct {
		name   string
		mutate func(*ResolvedItem)
	}{
		{name: "download auth ref", mutate: func(item *ResolvedItem) {
			item.DownloadAuthRef = "dar-" + strings.Repeat("a", 32)
		}},
		{name: "auth profile ref", mutate: func(item *ResolvedItem) {
			item.AuthProfileRef = "apr-fixture01"
		}},
		{name: "header profile ref", mutate: func(item *ResolvedItem) {
			item.HeaderProfileRef = "hpr-fixture01"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := ResolvedItem{Ref: "r-1", URL: "https://files.fixture.invalid/file.bin", PackID: "xpk-alpha001"}
			tc.mutate(&item)
			candidate := extractorAddTaskCandidate(item)
			if err := service.preflightCandidateAuth(context.Background(), candidate, nil); err == nil {
				t.Fatal("preflightCandidateAuth() error = nil, want auth-unavailable failure without adapter")
			}
		})
	}

	// An extracted candidate carrying no credential refs is unaffected.
	plain := extractorAddTaskCandidate(ResolvedItem{Ref: "r-2", URL: "https://files.fixture.invalid/file.bin"})
	if err := service.preflightCandidateAuth(context.Background(), plain, nil); err != nil {
		t.Fatalf("preflightCandidateAuth() error = %v, want plain extracted item unaffected", err)
	}
}

// releaseTrackingAdapter records Release calls for claim-leak assertions.
type releaseTrackingAdapter struct {
	fakePortAdapter
	released []string
}

func (a *releaseTrackingAdapter) Release(ref string) {
	a.released = append(a.released, ref)
}

func TestReleaseResolutionClaimsDropsMintedRefs(t *testing.T) {
	adapter := &releaseTrackingAdapter{}
	service := &Service{Adapter: adapter}
	resolution := Resolution{
		Status: ResolutionStatusMatched,
		Items: []ResolvedItem{
			{Ref: "r-1", URL: "https://files.fixture.invalid/a.bin"},
			{Ref: "r-2", URL: "https://files.fixture.invalid/b.bin"},
			{URL: "https://files.fixture.invalid/c.bin"}, // no minted ref
		},
	}
	service.releaseResolutionClaims(resolution)
	if len(adapter.released) != 2 || adapter.released[0] != "r-1" || adapter.released[1] != "r-2" {
		t.Fatalf("released = %#v, want [r-1 r-2]", adapter.released)
	}
}

func TestPreflightCandidateAuthValidatesDownloadAuthBinding(t *testing.T) {
	service := &Service{Adapter: &downloadAuthFakeAdapter{}}
	candidate := downloadAuthCandidate(nil)
	state := service.newAddTaskAuthBatchState()

	if err := service.preflightCandidateAuth(context.Background(), candidate, state); err != nil {
		t.Fatalf("preflightCandidateAuth() error = %v", err)
	}

	service.Adapter = &downloadAuthFakeAdapter{validateErr: errors.New("stale ref")}
	if err := service.preflightCandidateAuth(context.Background(), candidate, state); err == nil {
		t.Fatal("preflightCandidateAuth() error = nil, want binding validation failure")
	}
}
