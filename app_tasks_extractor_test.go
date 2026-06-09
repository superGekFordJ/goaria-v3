package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
)

type fakeAddTaskDispatcher struct {
	mu             sync.Mutex
	resolutions    map[string]extractor.AddTaskResolution
	resolveErrors  map[string]error
	headers        map[string][]string
	headerErrors   map[string]error
	resolvedInputs []string
}

func (d *fakeAddTaskDispatcher) Resolve(ctx context.Context, rawURL string) (extractor.AddTaskResolution, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolvedInputs = append(d.resolvedInputs, rawURL)
	if err := d.resolveErrors[rawURL]; err != nil {
		return extractor.AddTaskResolution{}, err
	}
	resolution := d.resolutions[rawURL]
	return resolution, nil
}

func (d *fakeAddTaskDispatcher) BuildAria2Headers(ctx context.Context, item extractor.ResolvedAddItem) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := item.URL
	if item.HeaderProfileRef != "" {
		key = item.HeaderProfileRef
	}
	if err := d.headerErrors[key]; err != nil {
		return nil, err
	}
	return append([]string(nil), d.headers[key]...), nil
}

type extractorRPCRecorder struct {
	mu              sync.Mutex
	methods         map[string]int
	addURIs         []string
	options         []map[string]any
	requests        []batchAddRPCRequest
	failURIs        map[string]bool
	saveSessionHook func()
}

func newExtractorRPCRecorder() *extractorRPCRecorder {
	return &extractorRPCRecorder{methods: make(map[string]int)}
}

func (r *extractorRPCRecorder) record(req batchAddRPCRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods[req.Method]++
	r.requests = append(r.requests, req)
	if req.Method == "aria2.addUri" {
		uri, options := decodeExtractorAddParams(req.Params)
		r.addURIs = append(r.addURIs, uri)
		r.options = append(r.options, options)
	}
}

func (r *extractorRPCRecorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.methods[method]
}

func (r *extractorRPCRecorder) addURIsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.addURIs))
	copy(out, r.addURIs)
	return out
}

func (r *extractorRPCRecorder) optionsSnapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, len(r.options))
	copy(out, r.options)
	return out
}

func setupAppTaskExtractorTest(t *testing.T, snapshots batchAddRPCSnapshots, dispatcher tasks.ExtractorAddTaskDispatcher) (*App, *extractorRPCRecorder) {
	return setupAppTaskExtractorTestWithRecorder(t, snapshots, dispatcher, newExtractorRPCRecorder())
}

func setupAppTaskExtractorTestWithRecorder(t *testing.T, snapshots batchAddRPCSnapshots, dispatcher tasks.ExtractorAddTaskDispatcher, recorder *extractorRPCRecorder) (*App, *extractorRPCRecorder) {
	t.Helper()

	originalConfig := config.Current
	originalSaveEnabled := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	config.Current = &config.AppConfig{
		DownloadDir:            t.TempDir(),
		SmartThreadMode:        false,
		MaxConnections:         "8",
		MinThreadLife:          5,
		ShowHistory:            true,
		MaxConcurrentDownloads: "3",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var req batchAddRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.record(req)

		switch req.Method {
		case "aria2.tellActive":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(batchAddTaskListResult(snapshots.active)))
		case "aria2.tellWaiting":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(batchAddTaskListResult(snapshots.waiting)))
		case "aria2.tellStopped":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(batchAddTaskListResult(snapshots.stopped)))
		case "aria2.addUri":
			uri, _ := decodeExtractorAddParams(req.Params)
			if recorder.failURIs[uri] {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "error": map[string]any{"code": 1, "message": "mock add failure"}})
				return
			}
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(fmt.Sprintf("gid-%d", recorder.count("aria2.addUri"))))
		case "aria2.saveSession":
			if recorder.saveSessionHook != nil {
				recorder.saveSessionHook()
			}
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse("OK"))
		default:
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse("OK"))
		}
	}))

	parsedURL, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("failed to parse httptest server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(parsedURL.Host)
	if err != nil {
		server.Close()
		t.Fatalf("failed to parse httptest server port: %v", err)
	}
	rpc.Init(port, "secret")

	t.Cleanup(func() {
		server.Close()
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		config.Current = originalConfig
	})

	app := NewApp()
	app.extractorDispatcher = dispatcher
	return app, recorder
}

func decodeExtractorAddParams(params []json.RawMessage) (string, map[string]any) {
	for i, param := range params {
		var urls []string
		if err := json.Unmarshal(param, &urls); err == nil && len(urls) > 0 {
			options := map[string]any{}
			if i+1 < len(params) {
				_ = json.Unmarshal(params[i+1], &options)
			}
			return urls[0], options
		}
	}
	return "", nil
}

func resolvedItem(sourceURL, directURL string) extractor.ResolvedAddItem {
	return extractor.ResolvedAddItem{
		SourceURL: sourceURL,
		PackID:    "fixturepack",
		URL:       directURL,
		Filename:  "file.bin",
		SizeBytes: 10 * 1024 * 1024,
		ID:        "item-1",
	}
}

func singleItemResolution(sourceURL, directURL string) extractor.AddTaskResolution {
	item := resolvedItem(sourceURL, directURL)
	return extractor.AddTaskResolution{Matched: true, SourceURL: sourceURL, PackID: item.PackID, Items: []extractor.ResolvedAddItem{item}}
}

type appExtractorStaticTransport struct {
	body string
}

func (t appExtractorStaticTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
	}, nil
}

func appGoFileEmptyOutputDispatcher(t *testing.T) tasks.ExtractorAddTaskDispatcher {
	t.Helper()
	assets, err := packbuilder.BuildSignedGoFileCandidate(packbuilder.GoFileCandidateOptions{CandidateTestKey: true})
	if err != nil {
		t.Fatalf("BuildSignedGoFileCandidate() error = %v", err)
	}
	return appCandidateDispatcher(t, assets.PublicKey, extractor.EmbeddedPack{ManifestJSON: assets.ManifestJSON, Payload: assets.Payload, Signature: assets.Signature}, appExtractorStaticTransport{
		body: `{"status":"auth_required","data":{"children":{}}}`,
	})
}

func appGoFileStoredAuthDispatcher(t *testing.T, store extractor.AuthProfileStore, body string) tasks.ExtractorAddTaskDispatcher {
	t.Helper()
	assets, err := packbuilder.BuildSignedGoFileCandidate(packbuilder.GoFileCandidateOptions{CandidateTestKey: true})
	if err != nil {
		t.Fatalf("BuildSignedGoFileCandidate() error = %v", err)
	}
	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{assets.PublicKey}
	resolver := appCandidateHostPolicyResolver{}
	registry, rejections := extractor.NewRegistryWithHostPolicyResolver([]extractor.EmbeddedPack{{ManifestJSON: assets.ManifestJSON, Payload: assets.Payload, Signature: assets.Signature}}, policy, resolver)
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	broker := extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: appExtractorStaticTransport{body: body}, AuthResolver: store, HostPolicyResolver: resolver})

	return extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry:     registry,
		Runner:       extractor.NewRunnerWithConfig(extractor.RunnerConfig{HTTPBroker: broker, AuthResolver: store, HostPolicyResolver: resolver}),
		AuthResolver: store,
	})
}

func gofileAppSingleFileFixture(directURL string) string {
	return `{"status":"ok","data":{"children":{"one":{"type":"file","name":"app-auth-profile.bin","link":"` + directURL + `","size":67108864,"mimeType":"application/octet-stream"}}}}`
}

func appIbbEmptyOutputDispatcher(t *testing.T) tasks.ExtractorAddTaskDispatcher {
	t.Helper()
	assets, err := packbuilder.BuildSignedIbbCandidate(packbuilder.IbbCandidateOptions{CandidateTestKey: true})
	if err != nil {
		t.Fatalf("BuildSignedIbbCandidate() error = %v", err)
	}
	return appCandidateDispatcher(t, assets.PublicKey, extractor.EmbeddedPack{ManifestJSON: assets.ManifestJSON, Payload: assets.Payload, Signature: assets.Signature}, appExtractorStaticTransport{
		body: `<html><head><title>no image</title></head></html>`,
	})
}

func appCandidateDispatcher(t *testing.T, publicKey ed25519.PublicKey, pack extractor.EmbeddedPack, transport http.RoundTripper) tasks.ExtractorAddTaskDispatcher {
	t.Helper()
	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{publicKey}
	resolver := appCandidateHostPolicyResolver{}
	registry, rejections := extractor.NewRegistryWithHostPolicyResolver([]extractor.EmbeddedPack{pack}, policy, resolver)
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	broker := extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: transport, HostPolicyResolver: resolver})
	return extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   extractor.NewRunnerWithConfig(extractor.RunnerConfig{HTTPBroker: broker, HostPolicyResolver: resolver}),
	})
}

type appCandidateHostPolicyResolver struct{}

func (appCandidateHostPolicyResolver) ResolveHostPolicy(_ context.Context, request extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	policy := extractor.ResolvedHostPolicy{
		PolicyVersion:    "2026.5.10",
		PolicySHA256:     strings.Repeat("c", 64),
		PackIdentity:     request.PackIdentity,
		DomainPolicyRefs: append([]string(nil), request.Manifest.DomainPolicyRefs...),
		BrokerPolicyRefs: append([]string(nil), request.Manifest.BrokerPolicyRefs...),
	}
	switch request.Manifest.PackID {
	case packbuilder.GoFilePackID:
		policy.PolicyID = "hpr-h7m2q9rv1p"
		policy.AllowedCapabilities = []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile}
		policy.IngressDomains = []extractor.DomainRule{{Host: "gofile.io", IncludeSubdomains: true}}
		policy.BrokerDomains = []extractor.DomainRule{{Host: "api.gofile.io"}, {Host: "download.gofile.io", IncludeSubdomains: true}, {Host: "gofile.io", IncludeSubdomains: true}}
		policy.OutputDomains = []extractor.HostPolicyOutputRule{{Host: "gofile.io", IncludeSubdomains: true, PathPrefixes: []string{"/files/", "/download/"}}}
		policy.AuthProfiles = []extractor.HostPolicyAuthProfileScope{{ProfileID: extractor.AuthProfileID(packbuilder.GoFileAuthProfileRef), Domains: []extractor.DomainRule{{Host: "gofile.io", IncludeSubdomains: true}}}}
		policy.Endpoints = []extractor.HostPolicyEndpoint{{BrokerPolicyRef: packbuilder.GoFileBrokerPolicyRef, EndpointRef: packbuilder.GoFileEndpointRef, URLTemplate: "https://api.gofile.io/contents/{id}", Methods: []string{http.MethodGet}, AuthProfileRefs: []extractor.AuthProfileID{extractor.AuthProfileID(packbuilder.GoFileAuthProfileRef)}, TimeoutMillis: 3000, MaxResponseBytes: 65536}}
	case packbuilder.IbbPackID:
		policy.PolicyID = "hpr-k4n8t2wa6s"
		policy.AllowedCapabilities = []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch}
		policy.IngressDomains = []extractor.DomainRule{{Host: "ibb.co", IncludeSubdomains: true}}
		policy.BrokerDomains = []extractor.DomainRule{{Host: "ibb.co", IncludeSubdomains: true}, {Host: "i.ibb.co", IncludeSubdomains: true}}
		policy.OutputDomains = []extractor.HostPolicyOutputRule{{Host: "i.ibb.co", PathPrefixes: []string{"/"}}}
		policy.Endpoints = []extractor.HostPolicyEndpoint{{BrokerPolicyRef: packbuilder.IbbBrokerPolicyRef, EndpointRef: packbuilder.IbbEndpointRef, URLTemplate: "https://ibb.co/{id}", Methods: []string{http.MethodGet}, TimeoutMillis: 3000, MaxResponseBytes: 65536}}
	default:
		policy.PolicyID = "hpr-appfixture"
		policy.AllowedCapabilities = append([]extractor.Capability(nil), request.Manifest.Capabilities...)
		policy.IngressDomains = []extractor.DomainRule{{Host: "example.invalid"}}
	}

	return policy, nil
}

func TestAddUri_NonExtractorURLUsesExistingDirectPath(t *testing.T) {
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, &fakeAddTaskDispatcher{})
	directURL := "https://example.com/file.zip"

	result := app.AddUri(" " + directURL + " ")

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL}) {
		t.Fatalf("expected direct URL add, got %#v", got)
	}
	if got := recorder.count("aria2.tellActive"); got != 1 {
		t.Fatalf("expected duplicate checks to call tellActive once, got %d", got)
	}
}

func TestAddUri_ExtractorSubmitsResolvedItemWithOutAndHeaders(t *testing.T) {
	shareURL := "https://fixture.invalid/d/abc"
	directURL := "https://download.fixture.invalid/file.bin"
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{shareURL: singleItemResolution(shareURL, directURL)},
		headers:     map[string][]string{directURL: {"Authorization: Bearer test-token", "User-Agent: GoAria-Test"}},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	result := app.AddUri(shareURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL}) {
		t.Fatalf("expected resolved direct URL add, got %#v", got)
	}
	options := recorder.optionsSnapshot()[0]
	if options["out"] != "file.bin" {
		t.Fatalf("expected out=file.bin, got %#v", options["out"])
	}
	if got := options["header"]; !reflect.DeepEqual(got, []any{"Authorization: Bearer test-token", "User-Agent: GoAria-Test"}) {
		t.Fatalf("expected header list, got %#v", got)
	}
}

func TestAddUri_MultiItemExtractorCreatesCollectionGroupFolder(t *testing.T) {
	shareURL := "https://fixture.invalid/d/collection-secret?token=synthetic"
	directOne := "https://download.fixture.invalid/one.bin"
	directTwo := "https://download.fixture.invalid/two.bin"
	items := []extractor.ResolvedAddItem{
		{SourceURL: shareURL, PackID: "fixturepack", URL: directOne, Filename: "one.bin", ID: "one"},
		{SourceURL: shareURL, PackID: "fixturepack", URL: directTwo, Filename: "two.bin", ID: "two"},
	}
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: "fixturepack", Items: items}},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	baseDir := config.Current.DownloadDir

	result := app.AddUri(shareURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directOne, directTwo}) {
		t.Fatalf("expected resolved direct URL adds, got %#v", got)
	}
	options := recorder.optionsSnapshot()
	if len(options) != 2 {
		t.Fatalf("expected two options, got %#v", options)
	}
	groupDir, ok := options[0]["dir"].(string)
	if !ok || groupDir == "" || groupDir == baseDir {
		t.Fatalf("expected grouped dir under %q, got %#v", baseDir, options[0]["dir"])
	}
	if options[1]["dir"] != groupDir {
		t.Fatalf("expected same group dir, got %#v", options)
	}
	if filepath.Dir(groupDir) != baseDir {
		t.Fatalf("expected group dir under %q, got %q", baseDir, groupDir)
	}
	if info, err := os.Stat(groupDir); err != nil || !info.IsDir() {
		t.Fatalf("expected group dir to exist, info=%#v err=%v", info, err)
	}
	if options[0]["out"] != "one.bin" || options[1]["out"] != "two.bin" {
		t.Fatalf("expected basename-only out options, got %#v", options)
	}
	assertNoPathOut(t, options[0]["out"])
	assertGroupPathGeneric(t, groupDir)
}

func TestAddUri_GroupPersistsBeforeSaveSessionForFastCompleteRace(t *testing.T) {
	shareURL := "https://fixture.invalid/d/fast-complete"
	directOne := "https://download.fixture.invalid/fast-one.bin"
	directTwo := "https://download.fixture.invalid/fast-two.bin"
	items := []extractor.ResolvedAddItem{
		{SourceURL: shareURL, PackID: "fixturepack", URL: directOne, Filename: "one.bin", ID: "one"},
		{SourceURL: shareURL, PackID: "fixturepack", URL: directTwo, Filename: "two.bin", ID: "two"},
	}
	dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: "fixturepack", Items: items}}}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	recorder.saveSessionHook = func() {
		if got := monitor.GetStoredTaskGroup("gid-1"); got == nil {
			t.Fatalf("expected group persisted before first saveSession")
		}
	}

	result := app.AddUri(shareURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if got := monitor.GetStoredTaskGroup("gid-1"); got == nil || got.Kind != "collection" {
		t.Fatalf("expected persisted collection group, got %#v", got)
	}
}

func TestBatchAddUri_ExtractorDeduplicatesResolvedDirectURLAgainstHistory(t *testing.T) {
	shareURL := "https://fixture.invalid/d/abc"
	directURL := "https://download.fixture.invalid/file.bin"
	dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{shareURL: singleItemResolution(shareURL, directURL)}}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	history.Add(history.HistoryEntry{GID: "gid-history", Source: directURL})

	result := app.BatchAddUri([]string{shareURL})

	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{directURL})
	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{})
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("expected no addUri for resolved history duplicate, got %d", got)
	}
}

func TestBatchAddUri_ExtractorDeduplicatesDirectAndShareWithinBatch(t *testing.T) {
	shareURL := "https://fixture.invalid/d/abc"
	directURL := "https://download.fixture.invalid/file.bin"

	for _, urls := range [][]string{{directURL, shareURL}, {shareURL, directURL}} {
		t.Run(strings.Join(urls, ","), func(t *testing.T) {
			dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{shareURL: singleItemResolution(shareURL, directURL)}}
			app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

			result := app.BatchAddUri(urls)

			assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{directURL})
			assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{directURL})
			if len(result.Errors) != 0 {
				t.Fatalf("expected no errors, got %#v", result.Errors)
			}
			if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL}) {
				t.Fatalf("expected one addUri for %q, got %#v", directURL, got)
			}
		})
	}
}

func TestBatchAddUri_ExtractorPartialSuccessAndErrorAreExplicit(t *testing.T) {
	shareURL := "https://fixture.invalid/d/abc"
	successURL := "https://download.fixture.invalid/success.bin"
	failURL := "https://download.fixture.invalid/fail.bin"
	items := []extractor.ResolvedAddItem{
		{SourceURL: shareURL, PackID: "fixturepack", URL: successURL, Filename: "success.bin", ID: "ok"},
		{SourceURL: shareURL, PackID: "fixturepack", URL: failURL, Filename: "fail.bin", HeaderProfileRef: "fail", ID: "bad"},
	}
	dispatcher := &fakeAddTaskDispatcher{
		resolutions:  map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: "fixturepack", Items: items}},
		headerErrors: map[string]error{"fail": errors.New("profile token=raw-secret failed")},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	result := app.BatchAddUri([]string{shareURL})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{successURL})
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{})
	if _, ok := result.Errors[failURL]; !ok {
		t.Fatalf("expected per-item error keyed by %q, got %#v", failURL, result.Errors)
	}
	if strings.Contains(result.Errors[failURL], "raw-secret") {
		t.Fatalf("error leaked secret: %q", result.Errors[failURL])
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{successURL}) {
		t.Fatalf("expected only successful item add, got %#v", got)
	}
}

func TestBatchAddUri_ExtractorFailedAddDoesNotPoisonResolvedSeenSet(t *testing.T) {
	shareURL := "https://fixture.invalid/d/abc"
	directURL := "https://download.fixture.invalid/retry.bin"
	items := []extractor.ResolvedAddItem{
		{SourceURL: shareURL, PackID: "fixturepack", URL: directURL, Filename: "retry-a.bin", ID: "first"},
		{SourceURL: shareURL, PackID: "fixturepack", URL: directURL, Filename: "retry-b.bin", ID: "second"},
	}
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: "fixturepack", Items: items}},
	}
	recorder := newExtractorRPCRecorder()
	recorder.failURIs = map[string]bool{directURL: true}
	app, recorder := setupAppTaskExtractorTestWithRecorder(t, batchAddRPCSnapshots{}, dispatcher, recorder)

	result := app.BatchAddUri([]string{shareURL})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{})
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{})
	if len(result.Errors) != 1 {
		t.Fatalf("expected one per-item error key for repeated failed direct URL, got %#v", result.Errors)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL, directURL}) {
		t.Fatalf("expected second candidate to retry after first add failure, got %#v", got)
	}
}

func TestBatchAddUri_SingleItemExtractorCanUseAdHocBatchGroup(t *testing.T) {
	shareURL := "https://fixture.invalid/d/single"
	directURLs := []string{
		"https://download.fixture.invalid/single.bin",
		"https://example.com/a.bin",
		"https://example.com/b.bin",
		"https://example.com/c.bin",
		"https://example.com/d.bin",
	}
	dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{shareURL: singleItemResolution(shareURL, directURLs[0])}}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	baseDir := config.Current.DownloadDir

	result := app.BatchAddUri([]string{shareURL, directURLs[1], directURLs[2], directURLs[3], directURLs[4]})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, directURLs)
	if len(result.Groups) != 1 || result.Groups[0].Kind != "batch" {
		t.Fatalf("expected one batch group, got %#v", result.Groups)
	}
	options := recorder.optionsSnapshot()
	if len(options) != 5 {
		t.Fatalf("expected five add options, got %#v", options)
	}
	groupDir := result.Groups[0].Dir
	if filepath.Dir(groupDir) != baseDir {
		t.Fatalf("expected group dir under %q, got %q", baseDir, groupDir)
	}
	for _, option := range options {
		if option["dir"] != groupDir {
			t.Fatalf("expected all candidates in batch group dir %q, got %#v", groupDir, options)
		}
	}
}

func TestBatchAddUri_MixedCollectionAndBatchGroupsDoNotPollute(t *testing.T) {
	collectionShare := "https://fixture.invalid/d/collection"
	directOne := "https://download.fixture.invalid/collection-one.bin"
	directTwo := "https://download.fixture.invalid/collection-two.bin"
	directInputs := []string{
		"https://example.com/a.bin",
		"https://example.com/b.bin",
		"https://example.com/c.bin",
		"https://example.com/d.bin",
		"https://example.com/e.bin",
	}
	items := []extractor.ResolvedAddItem{
		{SourceURL: collectionShare, PackID: "fixturepack", URL: directOne, Filename: "one.bin", ID: "one"},
		{SourceURL: collectionShare, PackID: "fixturepack", URL: directTwo, Filename: "two.bin", ID: "two"},
	}
	dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{collectionShare: {Matched: true, SourceURL: collectionShare, PackID: "fixturepack", Items: items}}}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	input := append([]string{collectionShare}, directInputs...)
	result := app.BatchAddUri(input)

	if len(result.Groups) != 2 {
		t.Fatalf("expected collection and batch groups, got %#v", result.Groups)
	}
	groupsByKind := map[string]rpc.DownloadGroup{}
	for _, group := range result.Groups {
		groupsByKind[group.Kind] = group
	}
	collectionGroup := groupsByKind["collection"]
	batchGroup := groupsByKind["batch"]
	if collectionGroup.Dir == "" || batchGroup.Dir == "" || collectionGroup.Dir == batchGroup.Dir {
		t.Fatalf("expected distinct collection/batch groups, got %#v", result.Groups)
	}
	options := recorder.optionsSnapshot()
	if options[0]["dir"] != collectionGroup.Dir || options[1]["dir"] != collectionGroup.Dir {
		t.Fatalf("expected collection items in collection dir, got %#v", options[:2])
	}
	for _, option := range options[2:] {
		if option["dir"] != batchGroup.Dir {
			t.Fatalf("expected direct items in batch dir %q, got %#v", batchGroup.Dir, options)
		}
	}
}

func TestAddUri_ExtractorSmartThreadDoesNotUnauthenticatedHEADHeaderedItem(t *testing.T) {
	headRequests := 0
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headRequests++
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer fileServer.Close()

	shareURL := "https://fixture.invalid/d/abc"
	directURL := fileServer.URL + "/file.bin"
	item := resolvedItem(shareURL, directURL)
	item.HeaderProfileRef = "download"
	item.SizeBytes = 64 * 1024 * 1024
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: "fixturepack", Items: []extractor.ResolvedAddItem{item}}},
		headers:     map[string][]string{"download": {"Authorization: Bearer test-token"}},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	config.Current.SmartThreadMode = true
	config.Current.MaxConnections = "4"

	result := app.AddUri(shareURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if headRequests != 0 {
		t.Fatalf("expected no unauthenticated HEAD requests for headered item, got %d", headRequests)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL}) {
		t.Fatalf("expected resolved URL add, got %#v", got)
	}
	options := recorder.optionsSnapshot()[0]
	if _, ok := options["split"]; !ok {
		t.Fatalf("expected smartthread split option, got %#v", options)
	}
}

func TestAddUri_ExtractorSmartThreadDoesNotUnauthenticatedHEADAuthProfileItem(t *testing.T) {
	headRequests := 0
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headRequests++
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer fileServer.Close()

	shareURL := "https://gofile.io/d/auth-profile"
	directURL := fileServer.URL + "/file.bin"
	item := resolvedItem(shareURL, directURL)
	item.AuthProfileRef = "apr-alpha001"
	item.SizeBytes = 64 * 1024 * 1024
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: appTaskFixtureIdentity.PackID, Items: []extractor.ResolvedAddItem{item}}},
		headers:     map[string][]string{directURL: {"Authorization: Bearer test-token"}},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	config.Current.SmartThreadMode = true
	config.Current.MaxConnections = "4"

	result := app.AddUri(shareURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if headRequests != 0 {
		t.Fatalf("expected no unauthenticated HEAD requests for auth-profile item, got %d", headRequests)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL}) {
		t.Fatalf("expected resolved URL add, got %#v", got)
	}
	options := recorder.optionsSnapshot()[0]
	if options["out"] != "file.bin" {
		t.Fatalf("expected out=file.bin, got %#v", options["out"])
	}
	if got := options["header"]; !reflect.DeepEqual(got, []any{"Authorization: Bearer test-token"}) {
		t.Fatalf("expected auth-profile header list, got %#v", got)
	}
	if _, ok := options["split"]; !ok {
		t.Fatalf("expected smartthread split option, got %#v", options)
	}
}

func TestAddUri_ExtractorAuthProfileHeaderBuildErrorIsRedacted(t *testing.T) {
	shareURL := "https://gofile.io/d/auth-error"
	directURL := "https://cdn.gofile.io/file.bin"
	item := resolvedItem(shareURL, directURL)
	item.AuthProfileRef = "apr-alpha001"
	dispatcher := &fakeAddTaskDispatcher{
		resolutions:  map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: appTaskFixtureIdentity.PackID, Items: []extractor.ResolvedAddItem{item}}},
		headerErrors: map[string]error{directURL: errors.New("resolve auth profile default failed: token=raw-secret-value")},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	result := app.AddUri(shareURL)

	if strings.Contains(result, "raw-secret-value") {
		t.Fatalf("AddUri() leaked raw secret: %q", result)
	}
	if !strings.Contains(result, "token=[REDACTED]") && !strings.Contains(result, "token=") {
		t.Fatalf("AddUri() = %q, want redacted token marker", result)
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("expected no aria2.addUri when auth-profile headers fail, got %d", got)
	}
}

var (
	_ tasks.ExtractorAddTaskDispatcher        = (*fakeAddTaskDispatcher)(nil)
	_ tasks.ExtractorAuthRuntimeSourcePlanner = (*fakeAddTaskDispatcher)(nil)
)

func assertNoPathOut(t *testing.T, value any) {
	t.Helper()
	out, ok := value.(string)
	if !ok || out == "" {
		t.Fatalf("expected non-empty string out, got %#v", value)
	}
	if out != filepath.Base(out) || strings.Contains(out, "..") || filepath.IsAbs(out) || strings.ContainsAny(out, `/\\`) {
		t.Fatalf("expected basename-only out, got %q", out)
	}
}

func assertGroupPathGeneric(t *testing.T, dir string) {
	t.Helper()
	name := filepath.Base(dir)
	lower := strings.ToLower(name)
	for _, forbidden := range []string{"provider", "private", "collection-secret", "token", "synthetic", "cdn", "example"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("group folder %q contains forbidden marker %q", name, forbidden)
		}
	}
}
