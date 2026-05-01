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
	"reflect"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
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
	mu       sync.Mutex
	methods  map[string]int
	addURIs  []string
	options  []map[string]any
	requests []batchAddRPCRequest
	failURIs map[string]bool
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

func setupAppTaskExtractorTest(t *testing.T, snapshots batchAddRPCSnapshots, dispatcher extractorAddTaskDispatcher) (*App, *extractorRPCRecorder) {
	return setupAppTaskExtractorTestWithRecorder(t, snapshots, dispatcher, newExtractorRPCRecorder())
}

func setupAppTaskExtractorTestWithRecorder(t *testing.T, snapshots batchAddRPCSnapshots, dispatcher extractorAddTaskDispatcher, recorder *extractorRPCRecorder) (*App, *extractorRPCRecorder) {
	t.Helper()

	originalConfig := config.Current
	originalSaveEnabled := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
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
	return extractor.AddTaskResolution{Matched: true, SourceURL: sourceURL, PackID: "fixturepack", Items: []extractor.ResolvedAddItem{item}}
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

var _ extractorAddTaskDispatcher = (*fakeAddTaskDispatcher)(nil)
