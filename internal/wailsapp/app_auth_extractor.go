//go:build extractor

package wailsapp

import (
	"goaria-v3/internal/extractor"
)

func (a *App) setHostAuthState(store extractor.AuthProfileStore, runtime *extractor.HostAuthRuntime, driver extractor.AuthWebViewDriver) {
	if a == nil {
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	a.authProfileStore = store
	a.hostAuthRuntime = runtime
	a.authWebViewDriver = driver
}

func (a *App) authProfileStoreForTest() extractor.AuthProfileStore {
	if a == nil {
		return nil
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	if a.authProfileStore == nil {
		return nil
	}
	return a.authProfileStore.(extractor.AuthProfileStore)
}

func (a *App) hostAuthRuntimeForTest() *extractor.HostAuthRuntime {
	return a.hostAuthRuntimeForTaskFlow()
}

func (a *App) hostAuthRuntimeForTaskFlow() *extractor.HostAuthRuntime {
	if a == nil {
		return nil
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	if a.hostAuthRuntime == nil {
		return nil
	}
	return a.hostAuthRuntime.(*extractor.HostAuthRuntime)
}

func (a *App) authWebViewDriverForTest() extractor.AuthWebViewDriver {
	if a == nil {
		return nil
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	if a.authWebViewDriver == nil {
		return nil
	}
	return a.authWebViewDriver.(extractor.AuthWebViewDriver)
}
