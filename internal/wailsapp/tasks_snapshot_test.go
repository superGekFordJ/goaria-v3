package wailsapp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
)

type barrierExtractorAdapter struct {
	id             string
	onResolve      func(ctx context.Context, rawURL string)
	onBuildHeaders func(ctx context.Context, item tasks.ResolvedItem)
	resolveCount   atomic.Int32
	buildCount     atomic.Int32
}

func (b *barrierExtractorAdapter) Resolve(ctx context.Context, rawURL string) (tasks.Resolution, error) {
	b.resolveCount.Add(1)
	if b.onResolve != nil {
		b.onResolve(ctx, rawURL)
	}
	return tasks.Resolution{
		Status:    tasks.ResolutionStatusMatched,
		SourceURL: rawURL,
		Items: []tasks.ResolvedItem{
			{
				URL:      rawURL + "/resolved",
				Filename: "test.bin",
			},
		},
	}, nil
}

func (b *barrierExtractorAdapter) BuildHeaders(ctx context.Context, item tasks.ResolvedItem) ([]string, error) {
	b.buildCount.Add(1)
	if b.onBuildHeaders != nil {
		b.onBuildHeaders(ctx, item)
	}
	return []string{"User-Agent: test"}, nil
}

func (b *barrierExtractorAdapter) AuthRequestsForSource(context.Context, string) ([]tasks.AuthRequest, error) {
	return nil, nil
}

func (b *barrierExtractorAdapter) Preflight(context.Context, tasks.AuthRequest) (tasks.PreflightResult, error) {
	return tasks.PreflightResult{Available: true}, nil
}

func (b *barrierExtractorAdapter) RefreshOnRecoverablePreflightFailure(context.Context, tasks.AuthRequest, tasks.RefreshGuard) (tasks.RefreshResult, error) {
	return tasks.RefreshResult{}, nil
}

func (b *barrierExtractorAdapter) RefreshOnGenericFailure(context.Context, tasks.AuthRequest, tasks.RefreshGuard) (tasks.RefreshResult, error) {
	return tasks.RefreshResult{}, nil
}

func (b *barrierExtractorAdapter) ValidateItemAuthPolicy(tasks.ResolvedItem) error {
	return nil
}

func (b *barrierExtractorAdapter) NewRefreshGuard() tasks.RefreshGuard {
	return nil
}

func (b *barrierExtractorAdapter) RedactError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type fakeRuntimeProvider struct {
	mu      sync.Mutex
	adapter tasks.ExtractorAdapter
}

func (p *fakeRuntimeProvider) currentTasksAdapter() tasks.ExtractorAdapter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.adapter
}

func (p *fakeRuntimeProvider) setAdapter(adapter tasks.ExtractorAdapter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.adapter = adapter
}

type fakeDownloadEngineForSnapshotTest struct {
	rpc.Aria2Engine
	addedURLs []string
	mu        sync.Mutex
}

func (e *fakeDownloadEngineForSnapshotTest) AddUri(uri string, options rpc.AddURIOptions) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addedURLs = append(e.addedURLs, uri)
	return "gid-123", nil
}

func TestTaskServiceCapturesSnapshotAdapter(t *testing.T) {
	app := NewApp(Options{})
	adapter1 := &barrierExtractorAdapter{id: "adapter-1"}
	provider := &fakeRuntimeProvider{adapter: adapter1}
	app.setExtractorRuntime(provider)

	svc := app.taskService()
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if svc.Adapter != adapter1 {
		t.Fatalf("expected adapter1, got %#v", svc.Adapter)
	}

	adapter2 := &barrierExtractorAdapter{id: "adapter-2"}
	provider.setAdapter(adapter2)

	// svc captured adapter1
	if svc.Adapter != adapter1 {
		t.Fatalf("svc adapter should remain adapter1, got %#v", svc.Adapter)
	}

	// new taskService() gets adapter2
	svc2 := app.taskService()
	if svc2.Adapter != adapter2 {
		t.Fatalf("new svc adapter should be adapter2, got %#v", svc2.Adapter)
	}
}

func TestTaskServiceAddUriPreservesInitialAdapterAcrossRuntimeUpdateSnapshot(t *testing.T) {
	app := NewApp(Options{})
	engine := &fakeDownloadEngineForSnapshotTest{}
	app.downloadEngine = engine

	enteredResolve := make(chan struct{})
	proceedResolve := make(chan struct{})

	adapter1 := &barrierExtractorAdapter{
		id: "adapter-1",
		onResolve: func(ctx context.Context, rawURL string) {
			close(enteredResolve)
			<-proceedResolve
		},
	}
	adapter2 := &barrierExtractorAdapter{id: "adapter-2"}

	provider := &fakeRuntimeProvider{adapter: adapter1}
	app.setExtractorRuntime(provider)

	done := make(chan string)
	go func() {
		done <- app.AddUri("https://example.com/test-pack-url")
	}()

	<-enteredResolve
	// While AddUri is in-flight on adapter1, publish adapter2 to runtime provider
	provider.setAdapter(adapter2)
	close(proceedResolve)

	res := <-done
	if res != "success" {
		t.Fatalf("expected success, got %q", res)
	}

	if adapter1.resolveCount.Load() != 1 {
		t.Fatalf("expected adapter1 resolveCount == 1, got %d", adapter1.resolveCount.Load())
	}
	if adapter1.buildCount.Load() != 1 {
		t.Fatalf("expected adapter1 buildCount == 1, got %d", adapter1.buildCount.Load())
	}
	if adapter2.resolveCount.Load() != 0 || adapter2.buildCount.Load() != 0 {
		t.Fatalf("adapter2 should not be called by in-flight AddUri")
	}

	// Subsequent AddUri uses adapter2
	app.AddUri("https://example.com/second-url")
	if adapter2.resolveCount.Load() != 1 {
		t.Fatalf("expected adapter2 resolveCount == 1 for second AddUri, got %d", adapter2.resolveCount.Load())
	}
}

func TestTaskServiceBatchAddUriPreservesInitialAdapterAcrossRuntimeUpdateSnapshot(t *testing.T) {
	app := NewApp(Options{})
	engine := &fakeDownloadEngineForSnapshotTest{}
	app.downloadEngine = engine

	enteredResolve := make(chan struct{})
	proceedResolve := make(chan struct{})

	adapter1 := &barrierExtractorAdapter{
		id: "adapter-1",
		onResolve: func(ctx context.Context, rawURL string) {
			select {
			case <-enteredResolve:
			default:
				close(enteredResolve)
			}
			<-proceedResolve
		},
	}
	adapter2 := &barrierExtractorAdapter{id: "adapter-2"}

	provider := &fakeRuntimeProvider{adapter: adapter1}
	app.setExtractorRuntime(provider)

	done := make(chan tasks.BatchAddResult)
	go func() {
		done <- app.BatchAddUri([]string{"https://example.com/url1", "https://example.com/url2"})
	}()

	<-enteredResolve
	// Mid-batch, publish adapter2
	provider.setAdapter(adapter2)
	close(proceedResolve)

	batchRes := <-done
	if len(batchRes.Succeeded) != 2 {
		t.Fatalf("expected 2 successful adds, got %d (errors: %v)", len(batchRes.Succeeded), batchRes.Errors)
	}

	if adapter2.resolveCount.Load() != 0 {
		t.Fatalf("adapter2 should not be called by in-flight BatchAddUri, got %d", adapter2.resolveCount.Load())
	}
}

func TestTaskServiceZeroPackNilAdapterFallbackSnapshot(t *testing.T) {
	app := NewApp(Options{})
	engine := &fakeDownloadEngineForSnapshotTest{}
	app.downloadEngine = engine

	provider := &fakeRuntimeProvider{adapter: nil}
	app.setExtractorRuntime(provider)

	svc := app.taskService()
	if svc.Adapter != nil {
		t.Fatalf("expected nil adapter for zero-pack, got %#v", svc.Adapter)
	}

	res := app.AddUri("https://example.com/direct-file.zip")
	if res != "success" {
		t.Fatalf("expected direct download success, got %q", res)
	}
	if len(engine.addedURLs) != 1 || engine.addedURLs[0] != "https://example.com/direct-file.zip" {
		t.Fatalf("expected direct URL added to engine, got %#v", engine.addedURLs)
	}
}
