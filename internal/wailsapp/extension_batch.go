//go:build extractor

package wailsapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/tasks"
)

var batchCommitDenylist = []string{
	"url",
	"final_url",
	"headers",
	"items",
	"cookies",
	"source_url",
	"auth_profile_ref",
	"header_profile_ref",
	"gid",
	"gids",
}

type extensionBatchAdapter struct {
	lease  *extensionResolveAdapter
	minter *extractor.TasksAdapter
	app    *App
}

func (a *extensionBatchAdapter) Ready() bool {
	return a != nil && a.lease != nil && a.lease.Ready() && a.minter != nil
}

func (a *extensionBatchAdapter) HandleCommit(ctx context.Context, env extension.RequestEnvelope, raw json.RawMessage) extension.CommitResult {
	if a == nil || !a.Ready() || a.app == nil {
		return extension.CommitResult{ErrorCode: extension.ErrCodeUnavailable}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, errCode := parseBatchDownloadRequest(raw)
	if errCode != "" {
		return extension.CommitResult{ErrorCode: errCode}
	}
	digest := commitPayloadDigest(raw)
	if stored, status := a.lease.lookupReceipt(env.RequestID, digest); status == receiptHit {
		return stored
	} else if status == receiptConflict {
		return extension.CommitResult{ErrorCode: extension.ErrCodeIdempotencyConflict}
	}

	clones, token, errCode := a.lease.consumeLeasedItems(req.SessionID, req.ItemIDs)
	if errCode != "" {
		return extension.CommitResult{ErrorCode: errCode}
	}

	mintedRefs := make([]string, 0, len(req.ItemIDs))
	defer func() {
		for _, ref := range mintedRefs {
			a.minter.Release(ref)
		}
	}()

	needsRestore := make(map[string]struct{}, len(clones))
	for id := range clones {
		needsRestore[id] = struct{}{}
	}
	defer func() {
		if rec := recover(); rec != nil {
			ids := make([]string, 0, len(clones))
			for id := range clones {
				ids = append(ids, id)
			}
			a.lease.restoreLeasedItems(token, ids, clones)
			panic(rec)
		}
		if len(needsRestore) == 0 {
			return
		}
		ids := make([]string, 0, len(needsRestore))
		for id := range needsRestore {
			ids = append(ids, id)
		}
		a.lease.restoreLeasedItems(token, ids, clones)
	}()

	errorsByItem := make(map[string]string)
	prepared := make([]tasks.PreparedAddItem, 0, len(req.ItemIDs))
	for _, id := range req.ItemIDs {
		item := clones[id]
		if err := extractor.ValidateLeaseOutputURL(item); err != nil {
			errorsByItem[id] = extension.CommitItemErrorNotAllowed
			continue
		}
		minted := a.minter.Mint(item)
		mintedRefs = append(mintedRefs, minted.Ref)
		prepared = append(prepared, tasks.PreparedAddItem{Item: minted, DisplayKey: id})
	}

	result := extension.CommitResult{
		SucceededItemIDs: []string{},
		DuplicateItemIDs: []string{},
	}
	if len(prepared) > 0 {
		added, err := a.app.taskService().AddPreparedExtractorItems(ctx, tasks.PreparedAddRequest{
			Items:       prepared,
			CreateGroup: req.CreateGroup,
			FolderName:  req.FolderName,
		})
		if err != nil {
			result := extension.CommitResult{SkipIdempotency: true}
			if errors.Is(err, tasks.ErrInvalidPreparedAdd) {
				result.ErrorCode = extension.ErrCodeInvalidRequest
			} else {
				result.ErrorCode = extension.ErrCodeUnavailable
			}
			return result
		}
		result.SucceededItemIDs = append([]string{}, added.Succeeded...)
		result.DuplicateItemIDs = append([]string{}, added.Duplicates...)
		for id := range added.Errors {
			errorsByItem[id] = extension.CommitItemErrorAddFailed
		}
		for _, id := range added.Succeeded {
			delete(errorsByItem, id)
			delete(needsRestore, id)
		}
		for _, id := range added.Duplicates {
			delete(errorsByItem, id)
			delete(needsRestore, id)
		}
		if len(added.Groups) > 0 {
			result.GroupKey = added.Groups[0].ID
		}
	}

	succeeded := toSet(result.SucceededItemIDs)
	duplicated := toSet(result.DuplicateItemIDs)
	for _, id := range req.ItemIDs {
		if _, ok := succeeded[id]; ok {
			continue
		}
		if _, ok := duplicated[id]; ok {
			continue
		}
		if _, ok := errorsByItem[id]; !ok {
			errorsByItem[id] = extension.CommitItemErrorNotAllowed
		}
	}

	if len(errorsByItem) > 0 {
		result.ErrorsByItemID = extension.SanitizeCommitItemErrors(errorsByItem)
	}
	result.Success = len(errorsByItem) == 0 && everyIDClassified(req.ItemIDs, succeeded, duplicated)
	a.lease.storeReceipt(env.RequestID, digest, cloneCommitResult(result), token.epoch)

	return result
}

func parseBatchDownloadRequest(raw json.RawMessage) (extension.BatchDownloadRequest, string) {
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(raw, &extra); err != nil {
		return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
	}
	for _, key := range batchCommitDenylist {
		if _, ok := extra[key]; ok {
			return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
		}
	}

	var req extension.BatchDownloadRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
	}
	if !validLeaseHandleID(req.SessionID) {
		return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
	}
	if len(req.ItemIDs) == 0 || len(req.ItemIDs) > extension.MaxResolveSessionItems {
		return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
	}
	seen := make(map[string]struct{}, len(req.ItemIDs))
	for _, id := range req.ItemIDs {
		if !validLeaseHandleID(id) {
			return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
		}
		if _, dup := seen[id]; dup {
			return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
		}
		seen[id] = struct{}{}
	}
	if hasCRLF(req.FolderName) || len(req.FolderName) > maxOptionalFieldBytes {
		return extension.BatchDownloadRequest{}, extension.ErrCodeInvalidRequest
	}

	return req, ""
}

func validLeaseHandleID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}

	return true
}

func commitPayloadDigest(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	delete(m, "type")
	delete(m, "request_id")
	b, err := json.Marshal(m)
	if err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

func everyIDClassified(ids []string, succeeded, duplicated map[string]struct{}) bool {
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

func toSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}

	return out
}

func cloneCommitResult(result extension.CommitResult) extension.CommitResult {
	cloned := result
	cloned.SkipIdempotency = false
	cloned.SucceededItemIDs = append([]string{}, result.SucceededItemIDs...)
	cloned.DuplicateItemIDs = append([]string{}, result.DuplicateItemIDs...)
	if result.ErrorsByItemID != nil {
		cloned.ErrorsByItemID = make(map[string]string, len(result.ErrorsByItemID))
		for key, value := range result.ErrorsByItemID {
			cloned.ErrorsByItemID[key] = value
		}
	}

	return cloned
}
