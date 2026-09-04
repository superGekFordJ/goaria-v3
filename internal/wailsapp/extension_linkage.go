//go:build extractor

package wailsapp

import (
	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
)

type ingressDigestAdapter struct {
	src *extractor.IngressDigestSource
}

func (a *ingressDigestAdapter) Ready() bool {
	return a != nil && a.src != nil && a.src.Ready()
}

func (a *ingressDigestAdapter) Snapshot() (extension.MatchDigestSnapshot, bool) {
	if a == nil || a.src == nil {
		return extension.MatchDigestSnapshot{}, false
	}
	snap, ok := a.src.Snapshot()
	if !ok {
		return extension.MatchDigestSnapshot{}, false
	}
	exact := snap.ExactDigests
	if exact == nil {
		exact = []string{}
	}
	sub := snap.SubdomainDigests
	if sub == nil {
		sub = []string{}
	}
	return extension.MatchDigestSnapshot{
		Version:          snap.Version,
		Salt:             snap.Salt,
		ExactDigests:     exact,
		SubdomainDigests: sub,
	}, true
}

func buildExtensionLinkageFromSnapshot(snap *extractor.RuntimeSnapshot, engine rpc.DownloadEngine) extension.Linkage {
	if snap == nil || snap.Dispatcher() == nil || snap.TasksAdapter() == nil || snap.IngressDigests() == nil {
		return extension.Linkage{}
	}
	resolver := newExtensionResolveAdapter(snap.Dispatcher())
	digests := &ingressDigestAdapter{src: snap.IngressDigests()}
	minter := snap.TasksAdapter()
	service := &tasks.Service{
		Adapter: minter,
		Engine:  engine,
	}
	committer := &extensionBatchAdapter{
		lease:   resolver,
		minter:  minter,
		service: service,
	}
	return extension.Linkage{
		Resolver:  resolver,
		Digests:   digests,
		Committer: committer,
	}
}

func pendingLinkageFromDispatcher(d *extractor.AddTaskDispatcher) extension.Linkage {
	return extension.Linkage{
		Resolver: newExtensionResolveAdapter(d),
		Digests:  &ingressDigestAdapter{src: extractor.NewIngressDigestSource(d.Registry())},
	}
}

func attachBatchCommitter(l extension.Linkage, minter *extractor.TasksAdapter, app *App) extension.Linkage {
	lease, ok := l.Resolver.(*extensionResolveAdapter)
	if !ok || minter == nil {
		return l
	}
	var svc tasksPreparedAdder
	if app != nil {
		svc = app.taskService()
	}
	l.Committer = &extensionBatchAdapter{lease: lease, minter: minter, service: svc}

	return l
}

func ConfigureExtensionLinkage(app *App, srv *extension.Server) {
	if app == nil || srv == nil {
		return
	}
	app.pendingMu.Lock()
	pending := app.pendingExtensionLinkage
	app.pendingMu.Unlock()
	if pending != nil {
		srv.SetLinkage(attachDirectBatchCommitter(*pending, app))
		return
	}

	var snap *extractor.RuntimeSnapshot
	if app.extractorRuntime != nil {
		if rt, ok := app.extractorRuntime.(*taggedExtractorRuntime); ok && rt != nil && rt.manager != nil {
			snap = rt.manager.CurrentSnapshot()
		}
	}
	linkage := buildExtensionLinkageFromSnapshot(snap, app.downloadEngine)
	srv.SetLinkage(attachDirectBatchCommitter(linkage, app))
}
