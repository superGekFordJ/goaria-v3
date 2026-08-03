package scheduler

import (
	"errors"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

func TestEventError_AttachesPendingResumeState(t *testing.T) {
	ps := progress.New("err-state", 1000)
	ps.SetDestPath("/tmp/err.bin")
	ps.SetFilename("err.bin")
	pending := &types.DownloadRecord{
		ID:         "err-state",
		URL:        "http://example.com/err.bin",
		DestPath:   "/tmp/err.bin",
		Downloaded: 420,
		Tasks:      []types.Task{{Offset: 420, Length: 580}},
	}
	ps.SetPendingResumeState(pending)

	localCfg := types.DownloadRecord{
		ID:            "err-state",
		Filename:      "err.bin",
		ProgressState: ps,
		ProgressCh:    make(chan types.DownloadEvent, 1),
	}

	errEvent := &types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: localCfg.ID,
		Filename:   localCfg.Filename,
		DestPath:   progress.CfgProgress(&localCfg).GetDestPath(),
		Err:        errors.New("boom"),
	}
	if took := progress.CfgProgress(&localCfg).TakePendingResumeState(); took != nil {
		errEvent.State = took
		if took.Downloaded > 0 {
			errEvent.Downloaded = took.Downloaded
		}
	}

	if errEvent.State == nil {
		t.Fatal("expected EventError.State from pending snapshot")
	}
	if len(errEvent.State.Tasks) != 1 {
		t.Fatalf("State.Tasks len=%d, want 1", len(errEvent.State.Tasks))
	}
	if errEvent.Downloaded != 420 {
		t.Fatalf("Downloaded=%d, want 420", errEvent.Downloaded)
	}
	if progress.CfgProgress(&localCfg).TakePendingResumeState() != nil {
		t.Fatal("pending must be cleared after Take")
	}
}

func TestEventError_NoPendingLeavesStateNil(t *testing.T) {
	ps := progress.New("err-nil", 1000)
	localCfg := types.DownloadRecord{
		ID:            "err-nil",
		ProgressState: ps,
	}
	var state *types.DownloadRecord
	if took := progress.CfgProgress(&localCfg).TakePendingResumeState(); took != nil {
		state = took
	}
	if state != nil {
		t.Fatal("expected nil State when no pending snapshot")
	}
}
