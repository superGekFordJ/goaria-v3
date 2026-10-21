package tasks

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	surgetypes "goaria-v3/internal/surge/types"
)

// diskSpaceStubEngine rejects URLs in rejectURLs with ErrInsufficientDiskSpace;
// all other URLs succeed. Behavior is URL-keyed so concurrent submit stays deterministic.
type diskSpaceStubEngine struct {
	mu         sync.Mutex
	rejectURLs map[string]struct{}
	calls      []string
}

func (e *diskSpaceStubEngine) AddUri(url string, options rpc.AddURIOptions) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, url)
	if _, reject := e.rejectURLs[url]; reject {
		return "", surgetypes.ErrInsufficientDiskSpace
	}
	return fmt.Sprintf("gid-%d", len(e.calls)), nil
}

func (e *diskSpaceStubEngine) Pause(string) error         { return nil }
func (e *diskSpaceStubEngine) Resume(string) error        { return nil }
func (e *diskSpaceStubEngine) PauseMulti([]string) error  { return nil }
func (e *diskSpaceStubEngine) ResumeMulti([]string) error { return nil }
func (e *diskSpaceStubEngine) Remove(string, bool) error  { return nil }
func (e *diskSpaceStubEngine) TellStatus(string, []string) (rpc.Task, error) {
	return rpc.Task{}, nil
}

func (e *diskSpaceStubEngine) TellStatusMulti([]string, []string) ([]rpc.Task, error) {
	return nil, nil
}
func (e *diskSpaceStubEngine) TellActive() ([]rpc.Task, error)                 { return nil, nil }
func (e *diskSpaceStubEngine) TellActiveLite() ([]rpc.Task, error)             { return nil, nil }
func (e *diskSpaceStubEngine) TellActiveProgress() ([]rpc.TaskProgress, error) { return nil, nil }
func (e *diskSpaceStubEngine) TellWaiting(int, int) ([]rpc.Task, error)        { return nil, nil }
func (e *diskSpaceStubEngine) TellWaitingLite(int, int) ([]rpc.Task, error)    { return nil, nil }
func (e *diskSpaceStubEngine) TellStopped(int, int) ([]rpc.Task, error)        { return nil, nil }
func (e *diskSpaceStubEngine) TellStoppedLite(int, int) ([]rpc.Task, error)    { return nil, nil }
func (e *diskSpaceStubEngine) GetGlobalStat() (rpc.GlobalStat, error) {
	return rpc.GlobalStat{}, nil
}
func (e *diskSpaceStubEngine) SaveSession() error { return nil }
func (e *diskSpaceStubEngine) ChangeGlobalOption(map[string]string) error {
	return nil
}

func (e *diskSpaceStubEngine) StreamEvents(context.Context) (<-chan any, func(), error) {
	ch := make(chan any)
	close(ch)
	return ch, func() {}, nil
}
func (e *diskSpaceStubEngine) IsSurgeActive() bool { return true }

func (e *diskSpaceStubEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func setupDiskSpaceAddService(t *testing.T, engine rpc.DownloadEngine) *Service {
	t.Helper()

	originalConfig := config.Get()
	originalSaveEnabled := history.SaveEnabled
	monitor.ResetDownloadGroupNamerForTest()
	restoreNamer := monitor.ConfigureDownloadGroupNamerForTest(10*time.Second, 10*time.Second, 1)
	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	history.DisableSaveForTest()
	history.Clear()
	config.SetTestConfig(&config.AppConfig{
		DownloadDir:     t.TempDir(),
		SmartThreadMode: false,
	})

	t.Cleanup(func() {
		monitor.ResetDownloadGroupNamerForTest()
		restoreNamer()
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		config.SetTestConfig(originalConfig)
	})

	return &Service{Engine: engine}
}

func TestRedactAddTaskError_InsufficientDiskSpace(t *testing.T) {
	t.Parallel()
	svc := &Service{}

	got := svc.redactAddTaskError(surgetypes.ErrInsufficientDiskSpace)
	if got != surgetypes.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("redactAddTaskError(bare) = %q, want sentinel", got)
	}

	wrapped := fmt.Errorf("enqueue: %w", surgetypes.ErrInsufficientDiskSpace)
	got = svc.redactAddTaskError(wrapped)
	if got != surgetypes.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("redactAddTaskError(wrapped) = %q, want sentinel", got)
	}
}

func TestBatchAddUri_DiskSpaceErrorRecorded(t *testing.T) {
	url := "https://example.com/too-big.bin"
	stub := &diskSpaceStubEngine{rejectURLs: map[string]struct{}{url: {}}}
	service := setupDiskSpaceAddService(t, stub)

	result := service.BatchAddUri([]string{url})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{})
	if result.Errors[url] != surgetypes.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("Errors[%q] = %q, want sentinel", url, result.Errors[url])
	}
	if stub.callCount() != 1 {
		t.Fatalf("AddUri calls = %d, want 1", stub.callCount())
	}
}

func TestBatchAddUri_PartialDiskRejectKeepsSuccesses(t *testing.T) {
	okURL := "https://example.com/ok.bin"
	failURL := "https://example.com/fail.bin"
	stub := &diskSpaceStubEngine{rejectURLs: map[string]struct{}{failURL: {}}}
	service := setupDiskSpaceAddService(t, stub)

	result := service.BatchAddUri([]string{okURL, failURL})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{okURL})
	if result.Errors[failURL] != surgetypes.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("Errors[%q] = %q, want sentinel", failURL, result.Errors[failURL])
	}
	if _, ok := result.Errors[okURL]; ok {
		t.Fatalf("ok URL should not be in Errors: %#v", result.Errors)
	}
	if stub.callCount() != 2 {
		t.Fatalf("AddUri calls = %d, want 2", stub.callCount())
	}
}

func TestAddUri_InsufficientDiskSpaceMessage(t *testing.T) {
	url := "https://example.com/huge.bin"
	stub := &diskSpaceStubEngine{rejectURLs: map[string]struct{}{url: {}}}
	service := setupDiskSpaceAddService(t, stub)

	got := service.AddUri(url)
	if got != surgetypes.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("AddUri() = %q, want sentinel", got)
	}
}

// HLS sentinel errors must redact to the bare sentinel string whether they
// arrive bare or wrapped in manifest/URL context — the frontend key matches
// on exact equality, and any URL residue would also leak signed params.
func TestRedactAddTaskError_HLSSentinels(t *testing.T) {
	t.Parallel()
	svc := &Service{}

	sentinels := []error{
		surgetypes.ErrHLSLiveNotSupported,
		surgetypes.ErrHLSDRMNotSupported,
		surgetypes.ErrHLSDemuxedAudioNotSupported,
		surgetypes.ErrHLSDiscontinuityMapNotSupported,
		surgetypes.ErrHLSIFramesOnlyNotSupported,
		surgetypes.ErrHLSInvalidManifest,
		surgetypes.ErrHLSInvalidSegmentResponse,
		surgetypes.ErrHLSNotImplemented,
		ErrHLSEngineUnsupported,
	}
	for _, sentinel := range sentinels {
		bare := svc.redactAddTaskError(sentinel)
		if bare != sentinel.Error() {
			t.Fatalf("redactAddTaskError(%v) = %q, want bare sentinel", sentinel, bare)
		}
		wrapped := fmt.Errorf("preflight https://hls.example.invalid/master.m3u8?token=secret: %w", sentinel)
		if got := svc.redactAddTaskError(wrapped); got != sentinel.Error() {
			t.Fatalf("redactAddTaskError(wrapped %v) = %q, want bare sentinel", sentinel, got)
		}
	}
}

func TestRedactAddTaskError_HLSEngineUnsupportedThroughWrap(t *testing.T) {
	t.Parallel()
	svc := &Service{}
	err := fmt.Errorf("add candidate: %w", ErrHLSEngineUnsupported)
	if got := svc.redactAddTaskError(err); got != ErrHLSEngineUnsupported.Error() {
		t.Fatalf("redactAddTaskError = %q, want %q", got, ErrHLSEngineUnsupported)
	}
}
