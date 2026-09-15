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
