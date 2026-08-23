package wailsapp

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/tasks"
)

const (
	directReceiptTTL  = 5 * time.Minute
	maxDirectReceipts = 256
)

type directReceipt struct {
	generation uint64
	epoch      uint64
	digest     string
	pending    bool
	stored     time.Time
	result     extension.DirectCommitResult
}

type directBatchAdapter struct {
	app *App

	mu       sync.Mutex
	epoch    uint64
	receipts map[string]directReceipt
}

func newDirectBatchAdapter(app *App) *directBatchAdapter {
	return &directBatchAdapter{
		app:      app,
		receipts: make(map[string]directReceipt),
	}
}

func attachDirectBatchCommitter(l extension.Linkage, app *App) extension.Linkage {
	l.DirectCommitter = newDirectBatchAdapter(app)
	return l
}

func (a *directBatchAdapter) Ready() bool {
	return a != nil && a.app != nil
}

func (a *directBatchAdapter) AdmitPending(requestID, digest string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admitPendingLocked(requestID, digest)
}

func (a *directBatchAdapter) AbandonPending(requestID string) {
	a.deleteReceipt(requestID)
}

func (a *directBatchAdapter) HandleDirectBatch(ctx context.Context, env extension.RequestEnvelope, req extension.DirectBatchRequest) extension.DirectCommitResult {
	empty := extension.DirectCommitResult{
		SucceededItemIDs: []string{},
		DuplicateItemIDs: []string{},
		ErrorsByItemID:   map[string]string{},
	}
	if a == nil || !a.Ready() {
		empty.ErrorCode = extension.ErrCodeUnavailable
		empty.SkipIdempotency = true
		return empty
	}
	if ctx == nil {
		ctx = context.Background()
	}

	defer func() {
		if rec := recover(); rec != nil {
			a.deleteReceipt(env.RequestID)
			panic(rec)
		}
	}()

	epoch, generation, digest, state := a.receiptState(env.RequestID)
	switch state {
	case "complete":
		if digest != "" && req.PayloadDigest != "" && digest != req.PayloadDigest {
			return extension.DirectCommitResult{ErrorCode: extension.ErrCodeIdempotencyConflict}
		}
		return a.cloneStoredResult(env.RequestID)
	case "pending":
		// Submit on the in-flight marker written at admission.
	default:
		if !a.AdmitPending(env.RequestID, req.PayloadDigest) {
			empty.ErrorCode = extension.ErrCodeBusy
			empty.SkipIdempotency = true
			return empty
		}
		epoch, generation, digest, state = a.receiptState(env.RequestID)
		if state != "pending" {
			empty.ErrorCode = extension.ErrCodeBusy
			empty.SkipIdempotency = true
			return empty
		}
	}

	svc := a.app.taskService()
	if svc == nil || svc.Engine == nil {
		a.deleteReceipt(env.RequestID)
		empty.ErrorCode = extension.ErrCodeUnavailable
		empty.SkipIdempotency = true
		return empty
	}

	items := make([]tasks.PreparedDirectAddItem, 0, len(req.Items))
	for _, item := range req.Items {
		prepared := tasks.PreparedDirectAddItem{
			ClientItemID:  item.ClientItemID,
			URL:           item.CanonicalURL,
			FinalURL:      item.FinalURL,
			Headers:       append([]string{}, item.Headers...),
			FileSize:      item.FileSize,
			HasFileSize:   item.HasFileSize,
			SkipHeadProbe: item.SkipHeadProbe,
			Filename:      item.Filename,
			DownloadPage:  item.DownloadPage,
		}
		items = append(items, prepared)
	}

	added, err := svc.AddPreparedDirectItems(ctx, tasks.PreparedDirectAddRequest{
		Items:       items,
		CreateGroup: req.CreateGroup,
		FolderName:  req.FolderName,
	})
	if ctx.Err() != nil {
		a.deleteReceipt(env.RequestID)
		empty.ErrorCode = extension.ErrCodeBusy
		empty.SkipIdempotency = true
		return empty
	}
	if err != nil {
		a.deleteReceipt(env.RequestID)
		empty.SkipIdempotency = true
		if errors.Is(err, tasks.ErrInvalidPreparedAdd) {
			empty.ErrorCode = extension.ErrCodeInvalidRequest
		} else {
			empty.ErrorCode = extension.ErrCodeUnavailable
		}
		return empty
	}

	result := empty
	result.SucceededItemIDs = append([]string{}, added.Succeeded...)
	result.DuplicateItemIDs = append([]string{}, added.Duplicates...)
	if result.SucceededItemIDs == nil {
		result.SucceededItemIDs = []string{}
	}
	if result.DuplicateItemIDs == nil {
		result.DuplicateItemIDs = []string{}
	}
	errorsByItem := make(map[string]string)
	for id := range added.Errors {
		errorsByItem[id] = extension.CommitItemErrorAddFailed
	}
	ids := make([]string, 0, len(req.Items))
	for _, item := range req.Items {
		ids = append(ids, item.ClientItemID)
	}
	succeeded := toDirectIDSet(result.SucceededItemIDs)
	duplicated := toDirectIDSet(result.DuplicateItemIDs)
	for _, id := range ids {
		if _, ok := succeeded[id]; ok {
			continue
		}
		if _, ok := duplicated[id]; ok {
			continue
		}
		if _, ok := errorsByItem[id]; !ok {
			errorsByItem[id] = extension.CommitItemErrorAddFailed
		}
	}
	if len(errorsByItem) > 0 {
		result.ErrorsByItemID = extension.SanitizeCommitItemErrors(errorsByItem)
	} else {
		result.ErrorsByItemID = map[string]string{}
	}
	result.Success = len(errorsByItem) == 0 && everyDirectIDClassified(ids, succeeded, duplicated)
	if len(added.Groups) > 0 {
		result.GroupKey = added.Groups[0].ID
	}
	storedDigest := digest
	if storedDigest == "" {
		storedDigest = req.PayloadDigest
	}
	a.storeComplete(env.RequestID, result, epoch, generation, storedDigest)
	return result
}

func (a *directBatchAdapter) LookupStatus(requestID string) (extension.DirectStatusSnapshot, bool) {
	if a == nil {
		return extension.DirectStatusSnapshot{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	rec, ok := a.receipts[requestID]
	if !ok || rec.epoch != a.epoch || rec.generation != a.currentGenerationLocked() {
		return extension.DirectStatusSnapshot{}, false
	}
	if rec.pending {
		return extension.DirectStatusSnapshot{Status: extension.DirectBatchStatusPending}, true
	}
	if time.Since(rec.stored) >= directReceiptTTL {
		delete(a.receipts, requestID)
		return extension.DirectStatusSnapshot{}, false
	}
	return extension.DirectStatusSnapshot{
		Status:           extension.DirectBatchStatusComplete,
		Success:          rec.result.Success,
		GroupKey:         rec.result.GroupKey,
		SucceededItemIDs: append([]string{}, rec.result.SucceededItemIDs...),
		DuplicateItemIDs: append([]string{}, rec.result.DuplicateItemIDs...),
		ErrorsByItemID:   copyStringMap(rec.result.ErrorsByItemID),
	}, true
}

func (a *directBatchAdapter) Invalidate() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.epoch++
	a.receipts = make(map[string]directReceipt)
}

func (a *directBatchAdapter) receiptState(requestID string) (epoch, generation uint64, digest, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	rec, ok := a.receipts[requestID]
	if !ok || rec.epoch != a.epoch || rec.generation != a.currentGenerationLocked() {
		return 0, 0, "", "miss"
	}
	if rec.pending {
		return rec.epoch, rec.generation, rec.digest, "pending"
	}
	if time.Since(rec.stored) >= directReceiptTTL {
		delete(a.receipts, requestID)
		return 0, 0, "", "miss"
	}
	return rec.epoch, rec.generation, rec.digest, "complete"
}

func (a *directBatchAdapter) cloneStoredResult(requestID string) extension.DirectCommitResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.receipts[requestID]
	cloned := rec.result
	cloned.SkipIdempotency = false
	cloned.SucceededItemIDs = append([]string{}, rec.result.SucceededItemIDs...)
	cloned.DuplicateItemIDs = append([]string{}, rec.result.DuplicateItemIDs...)
	cloned.ErrorsByItemID = copyStringMap(rec.result.ErrorsByItemID)
	if cloned.SucceededItemIDs == nil {
		cloned.SucceededItemIDs = []string{}
	}
	if cloned.DuplicateItemIDs == nil {
		cloned.DuplicateItemIDs = []string{}
	}
	if cloned.ErrorsByItemID == nil {
		cloned.ErrorsByItemID = map[string]string{}
	}
	return cloned
}

func (a *directBatchAdapter) admitPendingLocked(requestID, digest string) bool {
	a.pruneLocked()
	if rec, ok := a.receipts[requestID]; ok && rec.epoch == a.epoch && rec.generation == a.currentGenerationLocked() {
		if rec.pending {
			return false
		}
		if time.Since(rec.stored) < directReceiptTTL {
			return true
		}
		delete(a.receipts, requestID)
	}
	if len(a.receipts) >= maxDirectReceipts {
		if !a.evictOldestCompletedLocked() {
			return false
		}
	}
	a.receipts[requestID] = directReceipt{
		generation: a.currentGenerationLocked(),
		epoch:      a.epoch,
		digest:     digest,
		pending:    true,
		stored:     time.Now(),
	}
	return true
}

func (a *directBatchAdapter) storeComplete(requestID string, result extension.DirectCommitResult, epoch, generation uint64, digest string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.receipts[requestID]
	if !ok || !rec.pending {
		return
	}
	if rec.epoch != epoch || rec.generation != generation {
		return
	}
	if rec.epoch != a.epoch || rec.generation != a.currentGenerationLocked() {
		delete(a.receipts, requestID)
		return
	}
	cloned := result
	cloned.SkipIdempotency = false
	cloned.SucceededItemIDs = append([]string{}, result.SucceededItemIDs...)
	cloned.DuplicateItemIDs = append([]string{}, result.DuplicateItemIDs...)
	cloned.ErrorsByItemID = copyStringMap(result.ErrorsByItemID)
	storedDigest := digest
	if storedDigest == "" {
		storedDigest = rec.digest
	}
	a.receipts[requestID] = directReceipt{
		generation: rec.generation,
		epoch:      rec.epoch,
		digest:     storedDigest,
		pending:    false,
		stored:     time.Now(),
		result:     cloned,
	}
}

func (a *directBatchAdapter) deleteReceipt(requestID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.receipts, requestID)
}

func (a *directBatchAdapter) pruneLocked() {
	now := time.Now()
	for id, rec := range a.receipts {
		if rec.pending {
			continue
		}
		if rec.epoch != a.epoch || rec.generation != a.currentGenerationLocked() || now.Sub(rec.stored) >= directReceiptTTL {
			delete(a.receipts, id)
		}
	}
}

func (a *directBatchAdapter) evictOldestCompletedLocked() bool {
	var oldestID string
	var oldest time.Time
	found := false
	for id, rec := range a.receipts {
		if rec.pending {
			continue
		}
		if !found || rec.stored.Before(oldest) {
			oldestID = id
			oldest = rec.stored
			found = true
		}
	}
	if !found {
		return false
	}
	delete(a.receipts, oldestID)
	return true
}

func (a *directBatchAdapter) currentGenerationLocked() uint64 {
	if a.app == nil || a.app.extensionServer == nil {
		return 0
	}
	store := a.app.extensionServer.GetStore()
	if store == nil {
		return 0
	}
	return store.Generation()
}

func toDirectIDSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func everyDirectIDClassified(ids []string, succeeded, duplicated map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := succeeded[id]; ok {
			continue
		}
		if _, ok := duplicated[id]; ok {
			continue
		}
		return false
	}
	return true
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
