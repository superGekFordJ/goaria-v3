package tasks_test

import (
	"testing"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/tasks"
)

func TestAddUri_ExtractorDownloadAuthItemReleasesRefAfterSubmit(t *testing.T) {
	shareURL := "https://share.fixture.invalid/s/dlauth"
	directURL := "https://download.fixture.invalid/dlauth.bin"
	item := resolvedItem(shareURL, directURL)
	item.DownloadAuthRef = "dar-0123456789abcdef0123456789abcdef"
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{
			shareURL: {Matched: true, SourceURL: shareURL, PackID: item.PackID, Items: []extractor.ResolvedAddItem{item}},
		},
		headers: map[string][]string{directURL: {"Authorization: Bearer materialized-token"}},
	}
	service, recorder := setupAppTaskExtractorTest(t, tasks.BatchAddRPCSnapshots{}, dispatcher)

	result := service.AddUri(shareURL)
	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	options := recorder.optionsSnapshot()
	if len(options) != 1 {
		t.Fatalf("options = %#v, want one add", options)
	}
	if got := options[0]["header"]; len(got.([]any)) != 1 || got.([]any)[0] != "Authorization: Bearer materialized-token" {
		t.Fatalf("header = %#v, want materialized bearer", got)
	}
	released := dispatcher.releasedRefsSnapshot()
	if len(released) != 1 {
		t.Fatalf("releasedRefs = %#v, want exactly one release after submission settled", released)
	}
	if len(dispatcher.itemRefs) != 0 {
		t.Fatalf("itemRefs still holds %d entries, want released", len(dispatcher.itemRefs))
	}
}

func TestAddUri_ExtractorDownloadAuthItemReleasesRefAfterFailure(t *testing.T) {
	shareURL := "https://share.fixture.invalid/s/dlauth-fail"
	directURL := "https://download.fixture.invalid/dlauth-fail.bin"
	item := resolvedItem(shareURL, directURL)
	item.DownloadAuthRef = "dar-0123456789abcdef0123456789abcdef"
	dispatcher := &fakeAddTaskDispatcher{
		resolutions: map[string]extractor.AddTaskResolution{
			shareURL: {Matched: true, SourceURL: shareURL, PackID: item.PackID, Items: []extractor.ResolvedAddItem{item}},
		},
		headers: map[string][]string{directURL: {"Authorization: Bearer materialized-token"}},
	}
	recorder := newExtractorRPCRecorder()
	recorder.failURIs = map[string]bool{directURL: true}
	service, _ := setupAppTaskExtractorTestWithRecorder(t, tasks.BatchAddRPCSnapshots{}, dispatcher, recorder)

	result := service.AddUri(shareURL)
	if result == "success" {
		t.Fatal("AddUri() = success, want mocked add failure")
	}
	released := dispatcher.releasedRefsSnapshot()
	if len(released) != 1 {
		t.Fatalf("releasedRefs = %#v, want release even on submission failure", released)
	}
}
