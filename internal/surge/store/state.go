package store

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"

	"github.com/google/uuid"
)

const (
	DefaultInlineHashTimeout = 10 * time.Second
	hashPrefixMD5            = "md5:"
	hashPrefixSHA256         = "sha256:"
)

type SaveStateOptions struct {
	// SkipFileHash disables file_hash computation entirely for this save.
	SkipFileHash bool
	// InlineHashTimeout limits synchronous hashing time. If zero or negative, DefaultInlineHashTimeout is used.
	InlineHashTimeout time.Duration
}

type MasterState struct {
	Version   int
	Downloads []types.DownloadRecord
}

// persistedDetailRecord is the detail-gob wire shape: an explicit whitelist
// projection of types.DownloadRecord. Field names and types must stay
// identical to DownloadRecord — gob matches fields by name both directions,
// so old files (which embedded DownloadRecord directly) decode cleanly here
// and new files still decode in older builds.
// FORK-PATCH: detail state is the per-task credential store — Headers must
// persist so cold resume can re-authenticate — while runtime-only fields
// (ProgressState, Runtime, Limiter, ProgressCh, SupportsRange, IsResume,
// OutputPath…) are structurally excluded and can never reach disk. This also
// means an unregistered ProgressState can no longer fail the whole encode.
type persistedDetailRecord struct {
	ID                   string
	URLHash              string
	URL                  string
	Filename             string
	DestPath             string
	Status               string
	Error                string
	TotalSize            int64
	Downloaded           int64
	Elapsed              int64
	CreatedAt            int64
	PausedAt             int64
	Tasks                []types.Task
	ChunkBitmap          []byte
	ActualChunkSize      int64
	FileHash             string
	Mirrors              []string
	RateLimit            int64
	RateLimitSet         bool
	Workers              int
	MinChunkSize         int64
	RangeAcquisitionMode types.RangeAcquisitionMode
	SkipServerProbe      bool
	Headers              map[string]string
}

func toPersisted(s *types.DownloadRecord) *persistedDetailRecord {
	if s == nil {
		return nil
	}
	return &persistedDetailRecord{
		ID:                   s.ID,
		URLHash:              s.URLHash,
		URL:                  s.URL,
		Filename:             s.Filename,
		DestPath:             s.DestPath,
		Status:               s.Status,
		Error:                s.Error,
		TotalSize:            s.TotalSize,
		Downloaded:           s.Downloaded,
		Elapsed:              s.Elapsed,
		CreatedAt:            s.CreatedAt,
		PausedAt:             s.PausedAt,
		Tasks:                s.Tasks,
		ChunkBitmap:          s.ChunkBitmap,
		ActualChunkSize:      s.ActualChunkSize,
		FileHash:             s.FileHash,
		Mirrors:              s.Mirrors,
		RateLimit:            s.RateLimit,
		RateLimitSet:         s.RateLimitSet,
		Workers:              s.Workers,
		MinChunkSize:         s.MinChunkSize,
		RangeAcquisitionMode: s.RangeAcquisitionMode,
		SkipServerProbe:      s.SkipServerProbe,
		Headers:              s.Headers,
	}
}

func toRecord(s *persistedDetailRecord) *types.DownloadRecord {
	if s == nil {
		return nil
	}
	return &types.DownloadRecord{
		ID:                   s.ID,
		URLHash:              s.URLHash,
		URL:                  s.URL,
		Filename:             s.Filename,
		DestPath:             s.DestPath,
		Status:               s.Status,
		Error:                s.Error,
		TotalSize:            s.TotalSize,
		Downloaded:           s.Downloaded,
		Elapsed:              s.Elapsed,
		CreatedAt:            s.CreatedAt,
		PausedAt:             s.PausedAt,
		Tasks:                s.Tasks,
		ChunkBitmap:          s.ChunkBitmap,
		ActualChunkSize:      s.ActualChunkSize,
		FileHash:             s.FileHash,
		Mirrors:              s.Mirrors,
		RateLimit:            s.RateLimit,
		RateLimitSet:         s.RateLimitSet,
		Workers:              s.Workers,
		MinChunkSize:         s.MinChunkSize,
		RangeAcquisitionMode: s.RangeAcquisitionMode,
		SkipServerProbe:      s.SkipServerProbe,
		Headers:              s.Headers,
	}
}

// DetailState is the detail-gob envelope, stored per download at
// <baseDir>/details/<downloadID>.gob
type DetailState struct {
	Version int
	State   *persistedDetailRecord
}

func URLHash(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:8])
}

func getMasterPath() string {
	return filepath.Join(baseDir, "master.gob")
}

func getDetailPath(dir, id string) string {
	return filepath.Join(dir, "details", id+".gob")
}

func SaveState(url string, destPath string, state *types.DownloadRecord) error {
	return SaveStateWithOptions(url, destPath, state, SaveStateOptions{})
}

func SaveStateWithOptions(url string, destPath string, state *types.DownloadRecord, opts SaveStateOptions) error {
	if state.ID == "" {
		state.ID = uuid.New().String()
	}

	state.URLHash = URLHash(url)
	state.PausedAt = time.Now().Unix()
	if state.CreatedAt == 0 {
		state.CreatedAt = time.Now().Unix()
	}

	hashTimeout := opts.InlineHashTimeout
	if hashTimeout <= 0 {
		hashTimeout = DefaultInlineHashTimeout
	}
	if opts.SkipFileHash {
		state.FileHash = ""
	} else {
		fileHash, timedOut, err := computeFileHashMD5WithTimeout(state.DestPath+types.IncompleteSuffix, hashTimeout)
		if err != nil {
			utils.Debug("SaveState: skipping file hash for %s due to error: %v", state.DestPath, err)
		} else if timedOut {
			utils.Debug("SaveState: skipping file hash for %s after timeout %v", state.DestPath, hashTimeout)
		}
		state.FileHash = fileHash
	}

	if state.ChunkBitmap == nil {
		state.ChunkBitmap = []byte{}
	}
	if state.Mirrors == nil {
		state.Mirrors = []string{}
	}
	if state.Tasks == nil {
		state.Tasks = []types.Task{}
	}

	ds := DetailState{
		Version: 2,
		State:   toPersisted(state),
	}

	if err := ensureDirs(); err != nil {
		return err
	}

	// Acquire the write lock for both the detail write and the master list update.
	// Snapshot baseDir here so both operations use the same directory — eliminating
	// the race window that existed when baseDir was re-read under a separate RLock
	// after ensureDirs() had already released its own RLock.
	masterMu.Lock()
	defer masterMu.Unlock()

	dir := baseDir
	if dir == "" {
		return errors.New("state backend not configured")
	}

	detailPath := getDetailPath(dir, state.ID)
	if err := atomicWrite(detailPath, ds); err != nil {
		return fmt.Errorf("failed to write detail state: %w", err)
	}

	// Update the lightweight index (MasterList)
	list, err := loadMasterListUnlocked()
	if err != nil {
		return fmt.Errorf("failed to load master list: %w", err)
	}
	found := false
	for i, e := range list.Downloads {
		if e.ID == state.ID {
			list.Downloads[i].URL = state.URL
			list.Downloads[i].DestPath = state.DestPath
			list.Downloads[i].Filename = state.Filename
			list.Downloads[i].TotalSize = state.TotalSize
			list.Downloads[i].Downloaded = state.Downloaded
			list.Downloads[i].TimeTaken = state.Elapsed / int64(time.Millisecond)
			list.Downloads[i].Workers = state.Workers
			list.Downloads[i].MinChunkSize = state.MinChunkSize
			// FORK-PATCH: sparse SaveState must not wipe a persisted mode
			// with gob zero, or demote SkipServerProbe true→false.
			if state.RangeAcquisitionMode != "" {
				list.Downloads[i].RangeAcquisitionMode = state.RangeAcquisitionMode
			}
			if state.SkipServerProbe {
				list.Downloads[i].SkipServerProbe = true
			}
			if err := saveMasterListLocked(list); err != nil {
				return fmt.Errorf("failed to update master list: %w", err)
			}
			found = true
			break
		}
	}
	if !found {
		utils.Debug("SaveState: ID %s not found in master list; call AddToMasterList first", state.ID)
	}

	return nil
}

func computeFileHashMD5WithTimeout(path string, timeout time.Duration) (string, bool, error) {
	if timeout <= 0 {
		timeout = DefaultInlineHashTimeout
	}
	if timeout < time.Microsecond {
		return "", true, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()

	h := md5.New()
	buf := make([]byte, utils.MiB)
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return "", true, nil
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, err := h.Write(buf[:n]); err != nil {
				return "", false, fmt.Errorf("failed to update md5 hash: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", false, fmt.Errorf("failed to read file for md5 hash: %w", readErr)
		}
	}

	return hashPrefixMD5 + hex.EncodeToString(h.Sum(nil)), false, nil
}

func compareAgainstStoredFileHash(path string, storedHash string) (bool, error) {
	algo, expected := parseStoredHash(storedHash)
	switch algo {
	case "md5":
		current, timedOut, err := computeFileHashMD5WithTimeout(path, DefaultInlineHashTimeout)
		if err != nil {
			return false, err
		}
		if timedOut {
			utils.Debug("Integrity: hash timed out for %s, skipping verification", path)
			return true, nil
		}
		return strings.EqualFold(strings.TrimPrefix(current, hashPrefixMD5), expected), nil
	case "sha256":
		current, err := computeFileHash(path)
		if err != nil {
			return false, err
		}
		return strings.EqualFold(current, expected), nil
	default:
		return false, fmt.Errorf("unsupported hash format: %q", storedHash)
	}
}

func parseStoredHash(storedHash string) (algo, value string) {
	trimmed := strings.TrimSpace(storedHash)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, hashPrefixMD5):
		return "md5", trimmed[len(hashPrefixMD5):]
	case strings.HasPrefix(lower, hashPrefixSHA256):
		return "sha256", trimmed[len(hashPrefixSHA256):]
	case len(trimmed) == 32:
		return "md5", trimmed
	case len(trimmed) == 64:
		return "sha256", trimmed
	default:
		return "", trimmed
	}
}

func LoadState(url string, destPath string) (*types.DownloadRecord, error) {
	masterList, err := LoadMasterList()
	if err != nil {
		return nil, err
	}

	var foundID string

	for _, e := range masterList.Downloads {
		if e.URL == url && e.DestPath == destPath && e.Status != "completed" {
			foundID = e.ID
			break
		}
	}

	if foundID == "" {
		return nil, fmt.Errorf("state not found: %w", os.ErrNotExist)
	}

	masterMu.RLock()
	dir := baseDir
	masterMu.RUnlock()

	var ds DetailState
	if err := loadGob(getDetailPath(dir, foundID), &ds); err != nil {
		return nil, fmt.Errorf("failed to load detail state: %w", err)
	}

	if ds.State != nil && ds.State.ChunkBitmap == nil {
		ds.State.ChunkBitmap = []byte{}
	}

	return toRecord(ds.State), nil
}

func LoadStates(ids []string) (map[string]*types.DownloadRecord, error) {
	states := make(map[string]*types.DownloadRecord)
	var errs []error

	masterMu.RLock()
	dir := baseDir
	masterMu.RUnlock()

	for _, id := range ids {
		var ds DetailState
		if err := loadGob(getDetailPath(dir, id), &ds); err != nil {
			if !os.IsNotExist(err) {
				utils.Debug("LoadStates: failed to load state for %s: %v", id, err)
				errs = append(errs, fmt.Errorf("id %s: %w", id, err))
			}
			continue
		}
		if ds.State != nil {
			if ds.State.ChunkBitmap == nil {
				ds.State.ChunkBitmap = []byte{}
			}
			states[id] = toRecord(ds.State)
		}
	}
	return states, errors.Join(errs...)
}

// DeleteDetail removes only the detail gob for id (master list untouched).
// Used on EventComplete so persisted credentials do not linger past
// completion; pause/error paths keep the detail for resume.
func DeleteDetail(id string) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	if err := utils.RemoveFile(getDetailPath(baseDir, id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove detail state: %w", err)
	}
	return nil
}

func DeleteState(id string) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	// Remove detail file, propagating real errors (but not "file not found").
	if err := utils.RemoveFile(getDetailPath(baseDir, id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove detail state: %w", err)
	}

	// Also remove from the master list so LoadState won't find an orphaned index entry.
	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	out := make([]types.DownloadRecord, 0, len(list.Downloads))
	for _, e := range list.Downloads {
		if e.ID != id {
			out = append(out, e)
		}
	}
	list.Downloads = out
	return saveMasterListLocked(list)
}

func DeleteTasks(id string) error {
	masterMu.Lock()
	defer masterMu.Unlock()
	var ds DetailState
	detailPath := getDetailPath(baseDir, id)
	if err := loadGob(detailPath, &ds); err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to clear
		}
		return fmt.Errorf("failed to load detail state: %w", err)
	}
	if ds.State == nil {
		return nil
	}
	ds.State.Tasks = []types.Task{}
	if err := atomicWrite(detailPath, ds); err != nil {
		return fmt.Errorf("failed to clear tasks: %w", err)
	}
	return nil
}

// InvalidateResumeState drops the persisted resume payload (Tasks, bitmap,
// chunk size, file hash) while keeping identity, credentials, and Mirrors.
// RangeAcquisitionMode is cleared so a later cold resume follows the master
// row's mode — buildResumeConfig prefers the detail mode, and a stale
// range-supported/payload-first detail must not resurrect a strategy that
// range→single fallback just proved wrong.
// FORK-PATCH: used instead of DeleteDetail where credentials must survive;
// deleting the detail would drop persisted Headers needed by the fallback.
// Returns nil when no detail file exists.
func InvalidateResumeState(id string) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	var ds DetailState
	detailPath := getDetailPath(baseDir, id)
	if err := loadGob(detailPath, &ds); err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to invalidate
		}
		return fmt.Errorf("failed to load detail state: %w", err)
	}
	if ds.State == nil {
		return nil
	}
	ds.State.Tasks = []types.Task{}
	ds.State.ChunkBitmap = nil
	ds.State.ActualChunkSize = 0
	ds.State.FileHash = ""
	ds.State.RangeAcquisitionMode = types.RangeAcquireProbeAtEnqueue
	if err := atomicWrite(detailPath, ds); err != nil {
		return fmt.Errorf("failed to invalidate resume state: %w", err)
	}
	return nil
}

func LoadMasterList() (*types.MasterList, error) {
	masterMu.RLock()
	defer masterMu.RUnlock()

	return loadMasterListUnlocked()
}

func saveMasterListLocked(list *types.MasterList) error {
	if baseDir == "" {
		return errors.New("state backend not configured")
	}
	if err := ensureDirsLocked(); err != nil {
		return err
	}
	ms := MasterState{
		Version:   2,
		Downloads: list.Downloads,
	}
	return atomicWrite(getMasterPath(), ms)
}

func AddToMasterList(entry types.DownloadRecord) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Mirrors == nil {
		entry.Mirrors = []string{}
	}
	// FORK-PATCH: master.gob is the global index — it must never carry
	// per-task credentials or runtime-only fields. Scrub the value copy so
	// Headers stay confined to details/<id>.gob, and an unregistered
	// ProgressState can no longer fail the whole master write.
	entry.Headers = nil
	entry.ProgressState = nil
	entry.ProgressCh = nil
	entry.Runtime = nil
	entry.Limiter = nil
	entry.IsResume = false
	entry.IsExplicitCategory = false
	entry.SupportsRange = false

	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	found := false
	for i, e := range list.Downloads {
		if e.ID == entry.ID {
			list.Downloads[i] = entry
			found = true
			break
		}
	}
	if !found {
		list.Downloads = append(list.Downloads, entry)
	}
	return saveMasterListLocked(list)
}

func loadMasterListUnlocked() (*types.MasterList, error) {
	if baseDir == "" {
		return &types.MasterList{Downloads: []types.DownloadRecord{}}, nil
	}
	var ms MasterState
	if err := loadGob(getMasterPath(), &ms); err != nil {
		if os.IsNotExist(err) {
			return &types.MasterList{Downloads: []types.DownloadRecord{}}, nil
		}
		return nil, err
	}
	if ms.Version != 2 {
		utils.Debug("Master list has unsupported version %d (expected 2), deleting to start fresh", ms.Version)
		_ = os.Remove(getMasterPath())
		return &types.MasterList{Downloads: []types.DownloadRecord{}}, nil
	}
	return &types.MasterList{Downloads: ms.Downloads}, nil
}

func RemoveFromMasterList(id string) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	out := []types.DownloadRecord{}
	for _, e := range list.Downloads {
		if e.ID != id {
			out = append(out, e)
		}
	}
	list.Downloads = out
	return saveMasterListLocked(list)
}

func GetDownload(id string) (*types.DownloadRecord, error) {
	masterMu.RLock()
	defer masterMu.RUnlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return nil, err
	}
	for _, e := range list.Downloads {
		if e.ID == id {
			return &e, nil
		}
	}
	return nil, nil
}

func LoadPausedDownloads() ([]types.DownloadRecord, error) {
	list, err := LoadMasterList()
	if err != nil {
		return nil, err
	}
	var paused []types.DownloadRecord
	for _, e := range list.Downloads {
		if e.Status == "paused" || e.Status == "queued" {
			paused = append(paused, e)
		}
	}
	return paused, nil
}

func LoadCompletedDownloads() ([]types.DownloadRecord, error) {
	list, err := LoadMasterList()
	if err != nil {
		return nil, err
	}
	var completed []types.DownloadRecord
	for _, e := range list.Downloads {
		if e.Status == "completed" {
			completed = append(completed, e)
		}
	}
	return completed, nil
}

func CheckDownloadExists(url string) (bool, error) {
	list, err := LoadMasterList()
	if err != nil {
		return false, err
	}
	for _, e := range list.Downloads {
		if e.URL == url {
			return true, nil
		}
	}
	return false, nil
}

func UpdateStatus(id string, status string) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	found := false
	for i, e := range list.Downloads {
		if e.ID == id {
			list.Downloads[i].Status = status
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", types.ErrNotFound, id)
	}
	return saveMasterListLocked(list)
}

func UpdateURL(id string, newURL string) error {
	masterMu.Lock()
	defer masterMu.Unlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	found := false
	for i, e := range list.Downloads {
		if e.ID == id {
			list.Downloads[i].URL = newURL
			list.Downloads[i].URLHash = URLHash(newURL)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", types.ErrNotFound, id)
	}
	return saveMasterListLocked(list)
}

func PauseAllDownloads() error {
	masterMu.Lock()
	defer masterMu.Unlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	for i, e := range list.Downloads {
		if e.Status != "completed" {
			list.Downloads[i].Status = "paused"
		}
	}
	return saveMasterListLocked(list)
}

func ResumeAllDownloads() error {
	masterMu.Lock()
	defer masterMu.Unlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	for i, e := range list.Downloads {
		if e.Status == "paused" {
			list.Downloads[i].Status = "queued"
		}
	}
	return saveMasterListLocked(list)
}

func ListAllDownloads() ([]types.DownloadRecord, error) {
	list, err := LoadMasterList()
	if err != nil {
		return nil, err
	}
	return list.Downloads, nil
}

func RemoveCompletedDownloads() (int64, error) {
	return removeDownloadsByStatus("completed")
}

func RemoveFailedDownloads() (int64, error) {
	return removeDownloadsByStatus("failed")
}

func removeDownloadsByStatus(status string) (int64, error) {
	masterMu.Lock()
	defer masterMu.Unlock()

	list, err := loadMasterListUnlocked()
	if err != nil {
		return 0, err
	}
	var count int64
	out := []types.DownloadRecord{}
	for _, e := range list.Downloads {
		if e.Status == status {
			count++
			_ = utils.RemoveFile(getDetailPath(baseDir, e.ID))
		} else {
			out = append(out, e)
		}
	}
	list.Downloads = out
	err = saveMasterListLocked(list)
	return count, err
}

func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UpdateRateLimit updates the rate limit of a download in the database
func UpdateRateLimit(id string, rate int64) error {
	masterMu.Lock()
	defer masterMu.Unlock()
	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	found := false
	for i, e := range list.Downloads {
		if e.ID == id {
			list.Downloads[i].RateLimit = rate
			list.Downloads[i].RateLimitSet = true
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", types.ErrNotFound, id)
	}
	return saveMasterListLocked(list)
}

// UpdateDefaultRateLimit updates the inherited rate limit of a download
// only if the download does not have a user-set override.
func UpdateDefaultRateLimit(id string, rate int64) error {
	masterMu.Lock()
	defer masterMu.Unlock()
	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	for i, e := range list.Downloads {
		if e.ID == id {
			if e.RateLimitSet {
				return nil // user override takes precedence, no write needed
			}
			list.Downloads[i].RateLimit = rate
			return saveMasterListLocked(list)
		}
	}
	return fmt.Errorf("%w: %s", types.ErrNotFound, id)
}

// ClearRateLimit removes a download-specific rate limit override.
func ClearRateLimit(id string) error {
	masterMu.Lock()
	defer masterMu.Unlock()
	list, err := loadMasterListUnlocked()
	if err != nil {
		return err
	}
	found := false
	for i, e := range list.Downloads {
		if e.ID == id {
			list.Downloads[i].RateLimit = 0
			list.Downloads[i].RateLimitSet = false
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", types.ErrNotFound, id)
	}
	return saveMasterListLocked(list)
}

// NormalizeStaleDownloads resets downloads that were interrupted (e.g., app crash)
// and appear as dead/frozen items in the TUI.
func NormalizeStaleDownloads() (int, error) {
	masterMu.Lock()
	defer masterMu.Unlock()
	list, err := loadMasterListUnlocked()
	if err != nil {
		return 0, err
	}
	count := 0
	for i, e := range list.Downloads {
		if e.Status == "downloading" {
			list.Downloads[i].Status = "paused"
			count++
		}
	}
	if count > 0 {
		err = saveMasterListLocked(list)
	}
	return count, err
}

// ValidateIntegrity checks that paused .surge files still exist and haven't been tampered with.
// Removes orphaned or corrupted entries from the master list.
// Returns the number of entries removed.
func ValidateIntegrity() (int, error) {
	masterMu.Lock()
	defer masterMu.Unlock()
	list, err := loadMasterListUnlocked()
	if err != nil {
		return 0, err
	}

	removed := 0
	expectedSurgePaths := make(map[string]struct{})
	candidateDirs := make(map[string]struct{})

	var out []types.DownloadRecord

	for _, e := range list.Downloads {
		if e.DestPath == "" {
			out = append(out, e)
			continue
		}

		candidateDirs[filepath.Dir(e.DestPath)] = struct{}{}
		if e.Status != "completed" {
			expectedSurgePaths[e.DestPath+types.IncompleteSuffix] = struct{}{}
		}

		if e.Status == "paused" || e.Status == "queued" {
			surgePath := e.DestPath + types.IncompleteSuffix

			// Check if .surge file exists
			_, statErr := os.Stat(surgePath)
			if os.IsNotExist(statErr) {
				// File missing - remove orphaned DB entry
				utils.Debug("Integrity: .surge file missing for %s, removing entry %s", e.DestPath, e.ID)
				_ = utils.RemoveFile(getDetailPath(baseDir, e.ID))
				removed++
				continue
			}
			if statErr != nil {
				return removed, fmt.Errorf("failed to stat %s: %w", surgePath, statErr)
			}

			// If we have a stored hash, verify it
			var ds DetailState
			if err := loadGob(getDetailPath(baseDir, e.ID), &ds); err == nil && ds.State != nil && ds.State.FileHash != "" {
				matches, err := compareAgainstStoredFileHash(surgePath, ds.State.FileHash)
				if err != nil {
					return removed, fmt.Errorf("failed to verify hash for %s: %w", surgePath, err)
				}
				if !matches {
					// File has been tampered with - remove entry and corrupted file
					utils.Debug("Integrity: hash mismatch for %s (expected %s), removing", surgePath, ds.State.FileHash)
					_ = utils.RemoveFile(surgePath)
					_ = utils.RemoveFile(getDetailPath(baseDir, e.ID))
					removed++
					continue
				}
			}
		}

		out = append(out, e)
	}

	list.Downloads = out
	if removed > 0 {
		if err := saveMasterListLocked(list); err != nil {
			return removed, err
		}
	}

	// Remove orphan .surge files that no longer have matching entries.
	for dir := range candidateDirs {
		files, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("failed to read directory %s: %w", dir, err)
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if !strings.HasSuffix(name, types.IncompleteSuffix) {
				continue
			}
			surgePath := filepath.Join(dir, name)
			if _, ok := expectedSurgePaths[surgePath]; ok {
				continue
			}
			_ = utils.RemoveFile(surgePath)
			utils.Debug("Integrity: removed orphan .surge file %s", surgePath)
		}
	}

	// FORK-PATCH: sweep orphan detail gobs. A detail whose ID has no master
	// row (e.g. RemoveFromMasterList without DeleteState) or whose row is
	// completed has no consumer, yet may still carry persisted credentials.
	// "error" rows keep theirs — the detail is the credential source for
	// retry/resume. Uses the post-cleanup `list` so freshly removed entries
	// count as absent.
	detailsDir := filepath.Join(baseDir, "details")
	if detailFiles, err := os.ReadDir(detailsDir); err == nil {
		liveStatus := make(map[string]string, len(list.Downloads))
		for _, e := range list.Downloads {
			liveStatus[e.ID] = e.Status
		}
		for _, f := range detailFiles {
			name := f.Name()
			if f.IsDir() || strings.HasPrefix(name, ".tmp-") || !strings.HasSuffix(name, ".gob") {
				continue
			}
			id := strings.TrimSuffix(name, ".gob")
			if status, known := liveStatus[id]; !known || status == "completed" {
				_ = utils.RemoveFile(filepath.Join(detailsDir, name))
				utils.Debug("Integrity: removed orphan detail file %s", name)
			}
		}
	}

	return removed, nil
}
