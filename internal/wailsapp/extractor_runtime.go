//go:build extractor

package wailsapp

import (
	"context"
	"errors"
	"sync"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/tasks"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type extractorFilePicker interface {
	pickFile() (string, bool, error)
	pickDirectory() (string, bool, error)
}

type taggedExtractorRuntime struct {
	manager    *extractor.ExtractorRuntimeManager
	mutationMu sync.Mutex
	picker     extractorFilePicker
}

func newTaggedExtractorRuntime(manager *extractor.ExtractorRuntimeManager) *taggedExtractorRuntime {
	return &taggedExtractorRuntime{
		manager: manager,
		picker:  defaultWailsFilePicker{},
	}
}

func (r *taggedExtractorRuntime) currentTasksAdapter() tasks.ExtractorAdapter {
	if r == nil || r.manager == nil {
		return nil
	}
	snap := r.manager.CurrentSnapshot()
	if snap == nil {
		return nil
	}
	adapter := snap.TasksAdapter()
	if adapter == nil {
		return nil
	}
	return adapter
}

func (a *App) taggedRuntime() *taggedExtractorRuntime {
	rt := a.getExtractorRuntime()
	if rt == nil {
		return nil
	}
	if r, ok := rt.(*taggedExtractorRuntime); ok {
		return r
	}
	return nil
}

func (a *App) GetExtractorState() ExtractorState {
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return newEmptyExtractorState(false)
	}
	sources := rt.manager.ListSources()
	recoveryErrors := rt.manager.RecoveryErrors()

	dtoSources := make([]ExtractorSource, len(sources))
	for i, s := range sources {
		dtoSources[i] = ExtractorSource{
			SourceID:          s.SourceID,
			Kind:              string(s.Kind),
			DisplayName:       s.DisplayName,
			PackID:            s.PackID,
			PackVersion:       s.PackVersion,
			SignerFingerprint: s.SignerFingerprint,
			Status:            string(s.Status),
			ErrorCode:         s.ErrorCode,
		}
	}
	if recoveryErrors == nil {
		recoveryErrors = []string{}
	}

	return ExtractorState{
		Available:      true,
		Sources:        dtoSources,
		RecoveryErrors: recoveryErrors,
	}
}

type defaultWailsFilePicker struct{}

func (p defaultWailsFilePicker) pickFile() (string, bool, error) {
	app := application.Get()
	if app == nil || app.Dialog == nil {
		return "", false, errors.New("dialog service unavailable")
	}
	dlg := app.Dialog.OpenFile().
		SetTitle("Select Extractor Pack").
		CanChooseDirectories(false).
		CanChooseFiles(true).
		AddFilter("Extractor Pack (*.pack.zip)", "*.pack.zip")
	result, err := dlg.PromptForSingleSelection()
	if err != nil {
		return "", false, err
	}
	if result == "" {
		return "", true, nil
	}
	return result, false, nil
}

func (p defaultWailsFilePicker) pickDirectory() (string, bool, error) {
	app := application.Get()
	if app == nil || app.Dialog == nil {
		return "", false, errors.New("dialog service unavailable")
	}
	dlg := app.Dialog.OpenFile().
		SetTitle("Select Extractor Pack Directory").
		CanChooseDirectories(true).
		CanChooseFiles(false)
	result, err := dlg.PromptForSingleSelection()
	if err != nil {
		return "", false, err
	}
	if result == "" {
		return "", true, nil
	}
	return result, false, nil
}

func mapExtractorError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return ExtractorErrorCodeCancelled, true
	}

	if mgrErr, ok := errors.AsType[*extractor.RuntimeManagerError](err); ok {
		if mgrErr.Code == extractor.RuntimeManagerErrorCancelled {
			return ExtractorErrorCodeCancelled, true
		}
		if mgrErr.Code != "" {
			return string(mgrErr.Code), false
		}
	}

	if loadErr, ok := errors.AsType[*extractor.RuntimePackLoadError](err); ok {
		if loadErr.Code != "" {
			return string(loadErr.Code), false
		}
	}

	return ExtractorErrorCodeUnavailable, false
}

func (a *App) refreshExtensionLinkage() {
	if a == nil || a.extensionServer == nil {
		return
	}
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return
	}
	snap := rt.manager.CurrentSnapshot()
	newLinkage := buildExtensionLinkageFromSnapshot(snap, a.downloadEngine)
	a.extensionServer.ReplaceExtractorLinkage(newLinkage)
}

func (a *App) LoadExtractorPackFile() ExtractorOperationResult {
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return newGenericUnavailableResult()
	}
	rt.mutationMu.Lock()
	defer rt.mutationMu.Unlock()

	picker := rt.picker
	if picker == nil {
		picker = defaultWailsFilePicker{}
	}
	path, cancelled, err := picker.pickFile()
	if cancelled {
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: true,
			State:     a.GetExtractorState(),
		}
	}
	if err != nil {
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: false,
			ErrorCode: ExtractorErrorCodeSourceUnreadable,
			State:     a.GetExtractorState(),
		}
	}

	_, loadErr := rt.manager.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalZip,
		Locator: path,
	})
	if loadErr != nil {
		code, isCancel := mapExtractorError(loadErr)
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: isCancel,
			ErrorCode: code,
			State:     a.GetExtractorState(),
		}
	}

	a.refreshExtensionLinkage()
	return ExtractorOperationResult{
		Success:   true,
		Cancelled: false,
		State:     a.GetExtractorState(),
	}
}

func (a *App) LoadExtractorPackDirectory() ExtractorOperationResult {
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return newGenericUnavailableResult()
	}
	rt.mutationMu.Lock()
	defer rt.mutationMu.Unlock()

	picker := rt.picker
	if picker == nil {
		picker = defaultWailsFilePicker{}
	}
	path, cancelled, err := picker.pickDirectory()
	if cancelled {
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: true,
			State:     a.GetExtractorState(),
		}
	}
	if err != nil {
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: false,
			ErrorCode: ExtractorErrorCodeSourceUnreadable,
			State:     a.GetExtractorState(),
		}
	}

	_, loadErr := rt.manager.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalDirectory,
		Locator: path,
	})
	if loadErr != nil {
		code, isCancel := mapExtractorError(loadErr)
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: isCancel,
			ErrorCode: code,
			State:     a.GetExtractorState(),
		}
	}

	a.refreshExtensionLinkage()
	return ExtractorOperationResult{
		Success:   true,
		Cancelled: false,
		State:     a.GetExtractorState(),
	}
}

func (a *App) LoadExtractorPackURL(lockURL string) ExtractorOperationResult {
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return newGenericUnavailableResult()
	}
	rt.mutationMu.Lock()
	defer rt.mutationMu.Unlock()

	_, loadErr := rt.manager.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindRemoteLock,
		Locator: lockURL,
	})
	if loadErr != nil {
		code, isCancel := mapExtractorError(loadErr)
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: isCancel,
			ErrorCode: code,
			State:     a.GetExtractorState(),
		}
	}

	a.refreshExtensionLinkage()
	return ExtractorOperationResult{
		Success:   true,
		Cancelled: false,
		State:     a.GetExtractorState(),
	}
}

func (a *App) ReloadExtractorSource(sourceID string) ExtractorOperationResult {
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return newGenericUnavailableResult()
	}
	rt.mutationMu.Lock()
	defer rt.mutationMu.Unlock()

	_, reloadErr := rt.manager.ReloadSource(context.Background(), sourceID)
	if reloadErr != nil {
		code, isCancel := mapExtractorError(reloadErr)
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: isCancel,
			ErrorCode: code,
			State:     a.GetExtractorState(),
		}
	}

	a.refreshExtensionLinkage()
	return ExtractorOperationResult{
		Success:   true,
		Cancelled: false,
		State:     a.GetExtractorState(),
	}
}

func (a *App) RemoveExtractorSource(sourceID string) ExtractorOperationResult {
	rt := a.taggedRuntime()
	if rt == nil || rt.manager == nil {
		return newGenericUnavailableResult()
	}
	rt.mutationMu.Lock()
	defer rt.mutationMu.Unlock()

	_, removeErr := rt.manager.RemoveSource(context.Background(), sourceID)
	if removeErr != nil {
		code, isCancel := mapExtractorError(removeErr)
		return ExtractorOperationResult{
			Success:   false,
			Cancelled: isCancel,
			ErrorCode: code,
			State:     a.GetExtractorState(),
		}
	}

	a.refreshExtensionLinkage()
	return ExtractorOperationResult{
		Success:   true,
		Cancelled: false,
		State:     a.GetExtractorState(),
	}
}
