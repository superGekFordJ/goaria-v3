package extractor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
)

type fakeHeaderProfileResolver struct {
	headers map[string][]string
	err     error
}

func (r fakeHeaderProfileResolver) ResolveHeaderProfile(ctx context.Context, packID string, profileRef string, rawURL string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]string(nil), r.headers[profileRef]...), nil
}

type fakeAuthProfileResolver struct {
	secret ResolvedAuthSecret
	err    error
}

func (r fakeAuthProfileResolver) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}
	return r.secret, nil
}

func TestAddTaskDispatcherNoMatchFallsBack(t *testing.T) {
	registry, _ := NewRegistry(nil, DefaultTrustPolicy())
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   NewRunner(),
	})

	resolution, err := dispatcher.Resolve(context.Background(), "https://example.com/file.zip")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Matched {
		t.Fatalf("Resolve() matched = true, want false")
	}
	if len(resolution.Items) != 0 {
		t.Fatalf("Resolve() returned items for no-match: %#v", resolution.Items)
	}
}

func TestAddTaskDispatcherCandidateNoMatchFallsBack(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":false}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/unused.bin"}]}`,
		memoryMinPages: 1,
	}), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Matched {
		t.Fatalf("Resolve() matched = true, want false for candidate Match=false")
	}
	if len(resolution.Items) != 0 {
		t.Fatalf("Resolve() returned items for no-match candidate: %#v", resolution.Items)
	}
}

func TestAddTaskDispatcherRunsVerifiedFixturePack(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, validRunnerFixtureWASM(), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched {
		t.Fatal("Resolve() matched = false, want true")
	}
	if resolution.PackID != "fixturepack" {
		t.Fatalf("Resolve() PackID = %q, want fixturepack", resolution.PackID)
	}
	if len(resolution.Items) != 1 {
		t.Fatalf("Resolve() returned %d items, want 1", len(resolution.Items))
	}
	item := resolution.Items[0]
	if item.URL != "https://download.fixture.invalid/file.bin" {
		t.Fatalf("item.URL = %q", item.URL)
	}
	if item.Filename != "file.bin" || item.SizeBytes != 123 {
		t.Fatalf("item filename/size = %q/%d", item.Filename, item.SizeBytes)
	}
	if item.AuthProfileRef != "fixturepack-default" || item.HeaderProfileRef != "fixturepack-download" {
		t.Fatalf("item refs = auth:%q header:%q", item.AuthProfileRef, item.HeaderProfileRef)
	}
}

func TestAddTaskDispatcherRejectsInvalidExtractedItems(t *testing.T) {
	tests := []struct {
		name        string
		extractJSON string
		wantSafe    string
	}{
		{
			name:        "empty url",
			extractJSON: `{"items":[{"filename":"file.bin"}]}`,
			wantSafe:    "url",
		},
		{
			name:        "unsafe filename",
			extractJSON: `{"items":[{"url":"https://download.fixture.invalid/file.bin","filename":"../secret.txt"}]}`,
			wantSafe:    "filename",
		},
		{
			name:        "credential metadata",
			extractJSON: `{"items":[{"url":"https://download.fixture.invalid/file.bin","metadata":{"token":"secret-value"}}]}`,
			wantSafe:    "metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
				abiVersion:     CurrentABIVersion,
				matchJSON:      `{"matched":true}`,
				extractJSON:    tt.extractJSON,
				memoryMinPages: 1,
			}), nil)

			resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
			if err == nil {
				t.Fatalf("Resolve() error = nil, resolution = %#v, want error", resolution)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.wantSafe) {
				t.Fatalf("Resolve() error = %q, want to mention %q", err.Error(), tt.wantSafe)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("Resolve() error leaked secret: %q", err.Error())
			}
		})
	}
}

func TestAddTaskDispatcherMatchedEmptyExtractOutputReturnsGenericFailure(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
	}), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err == nil {
		t.Fatalf("Resolve() error = nil, resolution = %#v, want generic unsupported/auth failure", resolution)
	}
	if strings.Contains(err.Error(), "extract output must contain at least one item") || strings.Contains(err.Error(), "invalid add item") {
		t.Fatalf("Resolve() leaked internal empty-output validation error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "could not resolve this link") {
		t.Fatalf("Resolve() error = %q, want generic resolver failure", err.Error())
	}
}

func TestAddTaskDispatcherInvalidNonEmptyItemRemainsHardFailure(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"filename":"file.bin"}]}`,
		memoryMinPages: 1,
	}), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err == nil {
		t.Fatalf("Resolve() error = nil, resolution = %#v, want hard invalid-item failure", resolution)
	}
	if !strings.Contains(err.Error(), "invalid add item") || !strings.Contains(err.Error(), "item 0 url") {
		t.Fatalf("Resolve() error = %q, want hard invalid-item failure", err.Error())
	}
}

func TestAddTaskDispatcherContinuesAfterMatchedEmptyOutput(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(85)
	emptyPack := signedTestPack(t, privateKey, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
	}), func(values map[string]any) {
		values["pack_id"] = "fixturepack-empty"
	})
	resolvingPack := signedTestPack(t, privateKey, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/fallback.bin","filename":"fallback.bin"}]}`,
		memoryMinPages: 1,
	}), func(values map[string]any) {
		values["pack_id"] = "fixturepack-resolver"
	})
	registry, rejections := NewRegistry([]EmbeddedPack{emptyPack, resolvingPack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry, Runner: NewRunner()})

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched || resolution.PackID != "fixturepack-resolver" || len(resolution.Items) != 1 {
		t.Fatalf("resolution = %#v, want later resolving pack item", resolution)
	}
	if resolution.Items[0].URL != "https://download.fixture.invalid/fallback.bin" {
		t.Fatalf("resolved URL = %q", resolution.Items[0].URL)
	}
}

func TestAddTaskAria2HeaderExpansionUsesHostResolversOnly(t *testing.T) {
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		AuthResolver: fakeAuthProfileResolver{secret: ResolvedAuthSecret{
			HeaderName:  "Authorization",
			HeaderValue: "Bearer test-secret",
		}},
		HeaderResolver: fakeHeaderProfileResolver{headers: map[string][]string{
			"download": {"User-Agent: GoAria-Test", "Authorization: Bearer test-secret"},
		}},
	})
	item := ResolvedAddItem{
		PackID:           "fixturepack",
		URL:              "https://download.fixture.invalid/file.bin",
		AuthProfileRef:   "default",
		HeaderProfileRef: "download",
	}

	headers, err := dispatcher.BuildAria2Headers(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildAria2Headers() error = %v", err)
	}
	want := []string{"Authorization: Bearer test-secret", "User-Agent: GoAria-Test"}
	if strings.Join(headers, "\n") != strings.Join(want, "\n") {
		t.Fatalf("headers = %#v, want %#v", headers, want)
	}

	missing := NewAddTaskDispatcher(AddTaskDispatcherConfig{})
	if _, err := missing.BuildAria2Headers(context.Background(), item); err == nil {
		t.Fatal("BuildAria2Headers() with missing resolvers error = nil, want error")
	}

	failing := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		AuthResolver: fakeAuthProfileResolver{err: errors.New("raw-token-123 failed")},
	})
	_, err = failing.BuildAria2Headers(context.Background(), ResolvedAddItem{
		PackID:         "fixturepack",
		URL:            "https://download.fixture.invalid/file.bin?token=raw-token-123",
		AuthProfileRef: "default",
	})
	if err == nil {
		t.Fatal("BuildAria2Headers() missing profile error = nil, want error")
	}
	if strings.Contains(err.Error(), "raw-token-123") {
		t.Fatalf("BuildAria2Headers() leaked secret: %q", err.Error())
	}
}

func newFixtureAddTaskDispatcher(t *testing.T, payload []byte, mutate func(map[string]any)) *AddTaskDispatcher {
	t.Helper()

	publicKey, privateKey := deterministicKeyPair(84)
	registry, rejections := NewRegistry([]EmbeddedPack{signedTestPack(t, privateKey, payload, mutate)}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}

	return NewAddTaskDispatcher(AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   NewRunner(),
	})
}

var (
	_ AuthProfileResolver   = fakeAuthProfileResolver{}
	_ HeaderProfileResolver = fakeHeaderProfileResolver{}
	_                       = ed25519.PublicKey{}
)
