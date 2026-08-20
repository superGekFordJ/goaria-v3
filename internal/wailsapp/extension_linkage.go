//go:build extractor

package wailsapp

import (
	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
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
	l.Committer = &extensionBatchAdapter{lease: lease, minter: minter, app: app}

	return l
}

func ConfigureExtensionLinkage(app *App, srv *extension.Server) {
	if app == nil || srv == nil {
		return
	}
	app.pendingMu.Lock()
	pending := app.pendingExtensionLinkage
	app.pendingMu.Unlock()
	if pending == nil {
		return
	}
	srv.SetLinkage(*pending)
}
