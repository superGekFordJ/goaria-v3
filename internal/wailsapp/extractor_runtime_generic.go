//go:build !extractor

package wailsapp

func (a *App) GetExtractorState() ExtractorState {
	return newEmptyExtractorState(false)
}

func (a *App) LoadExtractorPackFile() ExtractorOperationResult {
	return newGenericUnavailableResult()
}

func (a *App) LoadExtractorPackDirectory() ExtractorOperationResult {
	return newGenericUnavailableResult()
}

func (a *App) LoadExtractorPackURL(lockURL string) ExtractorOperationResult {
	return newGenericUnavailableResult()
}

func (a *App) ReloadExtractorSource(sourceID string) ExtractorOperationResult {
	return newGenericUnavailableResult()
}

func (a *App) RemoveExtractorSource(sourceID string) ExtractorOperationResult {
	return newGenericUnavailableResult()
}
