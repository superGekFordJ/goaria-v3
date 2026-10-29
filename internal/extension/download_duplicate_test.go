package extension

import (
	"errors"
	"testing"
)

// handleDownload is the shared single-item exit for both "download" and "hls"
// requests: the duplicate failure ack carries back the known existing GID
// while keeping Success:false and the "duplicate" error text; other failures
// never leak a GID.

func TestDownloadDuplicate_AckCarriesKnownGID(t *testing.T) {
	adder := &fakeTaskAdder{gid: "gid-existing-1", err: errors.New("duplicate")}
	srv := newTestServer(t, adder, nil)

	for _, reqType := range []string{MsgTypeDownload, MsgTypeHLS} {
		resp := srv.handleDownload(DownloadRequest{
			Type: reqType,
			URL:  "http://127.0.0.1/files/existing.bin",
		})
		if resp.Type != MsgTypeDownloadAck {
			t.Fatalf("type %s: ack type = %q, want %q", reqType, resp.Type, MsgTypeDownloadAck)
		}
		if resp.Success {
			t.Fatalf("type %s: duplicate ack must keep Success=false", reqType)
		}
		if resp.Error != "duplicate" {
			t.Fatalf("type %s: ack error = %q, want duplicate", reqType, resp.Error)
		}
		if resp.GID != "gid-existing-1" {
			t.Fatalf("type %s: ack gid = %q, want gid-existing-1", reqType, resp.GID)
		}
	}
}

func TestDownloadDuplicate_UnknownGIDStaysEmpty(t *testing.T) {
	adder := &fakeTaskAdder{gid: "", err: errors.New("duplicate")}
	srv := newTestServer(t, adder, nil)

	resp := srv.handleDownload(DownloadRequest{
		Type: MsgTypeDownload,
		URL:  "http://127.0.0.1/files/history-only.bin",
	})
	if resp.Success || resp.Error != "duplicate" {
		t.Fatalf("ack = %+v, want duplicate failure", resp)
	}
	if resp.GID != "" {
		t.Fatalf("history-only duplicate gid = %q, want empty", resp.GID)
	}
}

func TestDownloadDuplicate_NonDuplicateFailureKeepsGIDEmpty(t *testing.T) {
	// Even if the adder returned a gid alongside a non-duplicate error, the
	// failure ack must not carry it.
	adder := &fakeTaskAdder{gid: "gid-should-not-leak", err: errors.New("disk full")}
	srv := newTestServer(t, adder, nil)

	resp := srv.handleDownload(DownloadRequest{
		Type: MsgTypeDownload,
		URL:  "http://127.0.0.1/files/fails.bin",
	})
	if resp.Success || resp.Error != "disk full" {
		t.Fatalf("ack = %+v, want generic failure", resp)
	}
	if resp.GID != "" {
		t.Fatalf("non-duplicate failure gid = %q, want empty", resp.GID)
	}
}

func TestDownloadDuplicate_SuccessAckUnchanged(t *testing.T) {
	adder := &fakeTaskAdder{gid: "gid-new-9"}
	srv := newTestServer(t, adder, nil)

	for _, reqType := range []string{MsgTypeDownload, MsgTypeHLS} {
		resp := srv.handleDownload(DownloadRequest{
			Type: reqType,
			URL:  "http://127.0.0.1/files/fresh.bin",
		})
		if !resp.Success || resp.Error != "" || resp.GID != "gid-new-9" {
			t.Fatalf("type %s: success ack = %+v, want success gid-new-9", reqType, resp)
		}
	}
}
