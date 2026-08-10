//go:build !extractor

package wailsapp

func (a *App) setHostAuthState(store hostAuthProfileStore, runtime hostAuthRuntime, driver hostAuthDriver) {
	if a == nil {
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	a.authProfileStore = store
	a.hostAuthRuntime = runtime
	a.authWebViewDriver = driver
}

func (a *App) authProfileStoreForTest() hostAuthProfileStore {
	return nil
}

func (a *App) hostAuthRuntimeForTest() hostAuthRuntime {
	return nil
}

func (a *App) hostAuthRuntimeForTaskFlow() hostAuthRuntime {
	return nil
}

func (a *App) authWebViewDriverForTest() hostAuthDriver {
	return nil
}
