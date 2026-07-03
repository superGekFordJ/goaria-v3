package tasks

import (
	"testing"

	"goaria-v3/internal/extension"
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
