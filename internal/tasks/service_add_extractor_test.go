package tasks

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
)

type fakeAddTaskDispatcher struct {
	mu             sync.Mutex
	plans          map[string][]extractor.HostAuthRuntimeRequest
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

func (d *fakeAddTaskDispatcher) AuthRuntimeRequestsForSource(ctx context.Context, rawURL string) ([]extractor.HostAuthRuntimeRequest, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]extractor.HostAuthRuntimeRequest(nil), d.plans[rawURL]...), nil
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

func setupAppTaskExtractorTest(t *testing.T, snapshots batchAddRPCSnapshots, dispatcher ExtractorAddTaskDispatcher) (*Service, *extractorRPCRecorder) {
	return setupAppTaskExtractorTestWithRecorder(t, snapshots, dispatcher, newExtractorRPCRecorder())
}

func setupAppTaskExtractorTestWithRecorder(t *testing.T, snapshots batchAddRPCSnapshots, dispatcher ExtractorAddTaskDispatcher, recorder *extractorRPCRecorder) (*Service, *extractorRPCRecorder) {
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

	svc := &Service{
		Dispatcher: dispatcher,
		Engine:     &rpc.Aria2Engine{},
	}
	return svc, recorder
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
		SourceURL:    sourceURL,
		PackID:       appTaskFixtureIdentity.PackID,
		PackIdentity: appTaskFixtureIdentity,
		Manifest:     appTaskFixtureManifest(),
		URL:          directURL,
		Filename:     "file.bin",
		SizeBytes:    10 * 1024 * 1024,
		ID:           "item-1",
	}
}

func appTaskFixtureManifest() extractor.Manifest {
	return extractor.Manifest{
		PackID:       appTaskFixtureIdentity.PackID,
		PackVersion:  appTaskFixtureIdentity.PackVersion,
		ABIVersion:   extractor.CurrentABIVersion,
		Capabilities: []extractor.Capability{extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		Domains:      []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}, {Host: "download.fixture.invalid", IncludeSubdomains: true}},
		ResourceLimits: extractor.ResourceLimits{
			TimeoutMillis:    60000,
			MaxMemoryPages:   64,
			MaxHostCalls:     16,
			MaxResponseBytes: 1 << 20,
			MaxOutputItems:   16,
			MaxOutputBytes:   1 << 16,
		},
		PayloadSHA256: appTaskFixtureIdentity.PayloadSHA256,
	}
}

func singleItemResolution(sourceURL, directURL string) extractor.AddTaskResolution {
	item := resolvedItem(sourceURL, directURL)
	return extractor.AddTaskResolution{Matched: true, SourceURL: sourceURL, PackID: item.PackID, Items: []extractor.ResolvedAddItem{item}}
}

var appTaskFixtureIdentity = extractor.VerifiedPackIdentity{
	PackID:          "xpk-alpha001",
	PackVersion:     "opaque-1",
	AssetSHA256:     strings.Repeat("1", 64),
	ManifestSHA256:  strings.Repeat("2", 64),
	PayloadSHA256:   strings.Repeat("3", 64),
	SignatureSHA256: strings.Repeat("4", 64),
	PublicKeySHA256: strings.Repeat("5", 64),
}



func TestAddUri_NonExtractorURLUsesExistingDirectPath(t *testing.T) {
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, &fakeAddTaskDispatcher{})
	directURL := "https://example.com/file.zip"

	result := service.AddUri(" " + directURL + " ")

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
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	result := service.AddUri(shareURL)

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
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	baseDir := config.Current.DownloadDir

	result := service.AddUri(shareURL)

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
	items[0].ID = "one"
	items[0].Filename = "one.bin"
	items[1].ID = "two"
	items[1].Filename = "two.bin"
	dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: appTaskFixtureIdentity.PackID, Items: items}}}
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	recorder.saveSessionHook = func() {
		if got := monitor.GetStoredTaskGroup("gid-1"); got == nil {
			t.Fatalf("expected group persisted before first saveSession")
		}
	}

	result := service.AddUri(shareURL)

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
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	history.Add(history.HistoryEntry{GID: "gid-history", Source: directURL})

	result := service.BatchAddUri([]string{shareURL})

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
			service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

			result := service.BatchAddUri(urls)

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
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	baseDir := config.Current.DownloadDir

	result := service.BatchAddUri([]string{shareURL, directURLs[1], directURLs[2], directURLs[3], directURLs[4]})

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
	items[0].ID = "one"
	items[0].Filename = "one.bin"
	items[1].ID = "two"
	items[1].Filename = "two.bin"
	dispatcher := &fakeAddTaskDispatcher{resolutions: map[string]extractor.AddTaskResolution{collectionShare: {Matched: true, SourceURL: collectionShare, PackID: appTaskFixtureIdentity.PackID, Items: items}}}
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	input := append([]string{collectionShare}, directInputs...)
	result := service.BatchAddUri(input)

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

func TestBatchAddUri_ExtractorPartialSuccessAndErrorAreExplicit(t *testing.T) {
	shareURL := "https://fixture.invalid/d/abc"
	successURL := "https://download.fixture.invalid/success.bin"
	failURL := "https://download.fixture.invalid/fail.bin"
	items := []extractor.ResolvedAddItem{
		resolvedItem(shareURL, successURL),
		resolvedItem(shareURL, failURL),
	}
	items[0].Filename = "success.bin"
	items[0].ID = "ok"
	items[1].Filename = "fail.bin"
	items[1].HeaderProfileRef = "fail"
	items[1].ID = "bad"
	dispatcher := &fakeAddTaskDispatcher{
		resolutions:  map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: appTaskFixtureIdentity.PackID, Items: items}},
		headerErrors: map[string]error{"fail": errors.New("profile token=raw-secret failed")},
	}
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	result := service.BatchAddUri([]string{shareURL})

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
		resolvedItem(shareURL, directURL),
		resolvedItem(shareURL, directURL),
	}
	items[0].Filename = "retry-a.bin"
	items[0].ID = "first"
	items[1].Filename = "retry-b.bin"
	items[1].ID = "second"
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: appTaskFixtureIdentity.PackID, Items: items}},
	}
	recorder := newExtractorRPCRecorder()
	recorder.failURIs = map[string]bool{directURL: true}
	service, recorder := setupAppTaskExtractorTestWithRecorder(t, batchAddRPCSnapshots{}, dispatcher, recorder)

	result := service.BatchAddUri([]string{shareURL})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{})
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{})
	if len(result.Errors) != 1 {
		t.Fatalf("expected one per-item error key for repeated failed direct URL, got %#v", result.Errors)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL, directURL}) {
		t.Fatalf("expected second candidate to retry after first add failure, got %#v", got)
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
		resolutions: map[string]extractor.AddTaskResolution{shareURL: {Matched: true, SourceURL: shareURL, PackID: appTaskFixtureIdentity.PackID, Items: []extractor.ResolvedAddItem{item}}},
		headers:     map[string][]string{"download": {"Authorization: Bearer test-token"}},
	}
	service, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	config.Current.SmartThreadMode = true
	config.Current.MaxConnections = "4"

	result := service.AddUri(shareURL)

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

var (
	_ ExtractorAddTaskDispatcher        = (*fakeAddTaskDispatcher)(nil)
	_ ExtractorAuthRuntimeSourcePlanner = (*fakeAddTaskDispatcher)(nil)
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
