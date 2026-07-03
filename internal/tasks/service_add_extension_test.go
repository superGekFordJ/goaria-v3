package tasks

import (
	"testing"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

// TestAddUriFromExtension_CRLFInDownloadPage verifies that a DownloadPage
// containing CRLF is rejected by header validation after ensureRefererHeader
// synthesizes the Referer line.
func TestAddUriFromExtension_CRLFInDownloadPage(t *testing.T) {
	svc := &Service{Engine: &rpc.Aria2Engine{}}
	req := extension.DownloadRequest{
		Type:         extension.MsgTypeDownload,
		URL:          "https://example.com/file.zip",
		DownloadPage: "https://x.com\r\nX-Injected: evil",
	}
	_, err := svc.AddUriFromExtension(req)
	if err == nil {
		t.Fatal("expected error for CRLF in DownloadPage, got nil")
	}
}

// TestAddUriFromExtension_EmptyURL verifies empty URL is rejected.
func TestAddUriFromExtension_EmptyURL(t *testing.T) {
	svc := &Service{Engine: &rpc.Aria2Engine{}}
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{})
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

// TestAddUriFromExtension_Duplicate_Rejected verifies that adding a URL already
// present in history is rejected as a duplicate.
func TestAddUriFromExtension_Duplicate_Rejected(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	t.Cleanup(func() {
		history.Clear()
	})

	dupURL := "https://example.com/dup.zip"
	history.Add(history.HistoryEntry{GID: "gid-dup", Source: dupURL})

	svc := &Service{Engine: &rpc.Aria2Engine{}}
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type: extension.MsgTypeDownload,
		URL:  dupURL,
	})
	if err == nil || err.Error() != "duplicate" {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

// TestAddUriFromExtension_DedupKeyOverride verifies that two different URLs
// sharing the same DedupKey are treated as duplicates.
func TestAddUriFromExtension_DedupKeyOverride(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	t.Cleanup(func() {
		history.Clear()
	})

	dedupKey := "shared-key-123"
	history.Add(history.HistoryEntry{GID: "gid-first", Source: dedupKey})

	svc := &Service{Engine: &rpc.Aria2Engine{}}
	_, err := svc.AddUriFromExtension(extension.DownloadRequest{
		Type:     extension.MsgTypeDownload,
		URL:      "https://example.com/different.zip",
		DedupKey: dedupKey,
	})
	if err == nil || err.Error() != "duplicate" {
		t.Fatalf("expected duplicate error via DedupKey, got %v", err)
	}
}
