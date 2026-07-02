package rpc

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
)

type HybridEngine struct {
	aria2Engine DownloadEngine
	surgeEngine DownloadEngine
}

func NewHybridEngine(aria2, surge DownloadEngine) *HybridEngine {
	return &HybridEngine{
		aria2Engine: aria2,
		surgeEngine: surge,
	}
}

func (h *HybridEngine) splitGid(gid string) (string, string) {
	if strings.HasPrefix(gid, "sg_") {
		return "sg", gid[3:]
	}
	if strings.HasPrefix(gid, "ar_") {
		return "ar", gid[3:]
	}
	return "ar", gid // Default to Aria2 if no prefix is present
}

func (h *HybridEngine) AddUri(url string, options AddURIOptions) (string, error) {
	subOpts := options
	subOpts.BeforeSave = nil

	lowerURL := strings.ToLower(url)
	isHTTP := strings.HasPrefix(lowerURL, "http://") || strings.HasPrefix(lowerURL, "https://")

	if isHTTP {
		gid, err := h.surgeEngine.AddUri(url, subOpts)
		if err == nil {
			prefixedGid := "sg_" + gid
			if options.BeforeSave != nil {
				if err := options.BeforeSave(prefixedGid); err != nil {
					return "", err
				}
			}
			return prefixedGid, nil
		}
		log.Printf("[Hybrid] Surge failed to add URI %s: %v, falling back to Aria2", url, err)
	}

	// Aria2 hard limit: max-connection-per-server = 16.
	// Surge allows up to 32 (PerDownloadMax), so only clamp on the Aria2 path.
	if subOpts.Split > 16 {
		subOpts.Split = 16
	}

	gid, err := h.aria2Engine.AddUri(url, subOpts)
	if err != nil {
		return "", err
	}
	prefixedGid := "ar_" + gid
	if options.BeforeSave != nil {
		if err := options.BeforeSave(prefixedGid); err != nil {
			return "", err
		}
	}
	return prefixedGid, nil
}

func (h *HybridEngine) Pause(gid string) error {
	engine, rawGid := h.splitGid(gid)
	if engine == "sg" {
		return h.surgeEngine.Pause(rawGid)
	}
	return h.aria2Engine.Pause(rawGid)
}

func (h *HybridEngine) Resume(gid string) error {
	engine, rawGid := h.splitGid(gid)
	if engine == "sg" {
		return h.surgeEngine.Resume(rawGid)
	}
	return h.aria2Engine.Resume(rawGid)
}

func (h *HybridEngine) PauseMulti(gids []string) error {
	var sgGids, arGids []string
	for _, gid := range gids {
		engine, rawGid := h.splitGid(gid)
		if engine == "sg" {
			sgGids = append(sgGids, rawGid)
		} else {
			arGids = append(arGids, rawGid)
		}
	}
	var sgErr, arErr error
	if len(sgGids) > 0 {
		sgErr = h.surgeEngine.PauseMulti(sgGids)
	}
	if len(arGids) > 0 {
		arErr = h.aria2Engine.PauseMulti(arGids)
	}
	if sgErr != nil && arErr != nil {
		return fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	return nil
}

func (h *HybridEngine) ResumeMulti(gids []string) error {
	var sgGids, arGids []string
	for _, gid := range gids {
		engine, rawGid := h.splitGid(gid)
		if engine == "sg" {
			sgGids = append(sgGids, rawGid)
		} else {
			arGids = append(arGids, rawGid)
		}
	}
	var sgErr, arErr error
	if len(sgGids) > 0 {
		sgErr = h.surgeEngine.ResumeMulti(sgGids)
	}
	if len(arGids) > 0 {
		arErr = h.aria2Engine.ResumeMulti(arGids)
	}
	if sgErr != nil && arErr != nil {
		return fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	return nil
}

func (h *HybridEngine) PauseMultiResults(gids []string) ([]MultiCallItemResult, error) {
	return h.pauseResumeMultiResults(gids, true)
}

func (h *HybridEngine) ResumeMultiResults(gids []string) ([]MultiCallItemResult, error) {
	return h.pauseResumeMultiResults(gids, false)
}

func (h *HybridEngine) pauseResumeMultiResults(gids []string, isPause bool) ([]MultiCallItemResult, error) {
	var sgGids, arGids []string
	originalGIDs := make(map[string]string)

	for _, gid := range gids {
		engine, rawGid := h.splitGid(gid)
		originalGIDs[rawGid] = gid
		if engine == "sg" {
			sgGids = append(sgGids, rawGid)
		} else {
			arGids = append(arGids, rawGid)
		}
	}

	results := make([]MultiCallItemResult, 0, len(gids))

	// Handle Surge GIDs
	for _, rawGid := range sgGids {
		var err error
		if isPause {
			err = h.surgeEngine.Pause(rawGid)
		} else {
			err = h.surgeEngine.Resume(rawGid)
		}
		origGID := originalGIDs[rawGid]
		itemRes := MultiCallItemResult{
			GID: origGID,
			OK:  err == nil,
		}
		if err != nil {
			itemRes.Error = err.Error()
		}
		results = append(results, itemRes)
	}

	// Handle Aria2 GIDs
	if len(arGids) > 0 {
		var arResults []MultiCallItemResult
		var err error
		if isPause {
			arResults, err = PauseMultiResults(arGids)
		} else {
			arResults, err = UnpauseMultiResults(arGids)
		}
		if err != nil {
			for _, rawGid := range arGids {
				origGID := originalGIDs[rawGid]
				results = append(results, MultiCallItemResult{
					GID:   origGID,
					OK:    false,
					Error: err.Error(),
				})
			}
		} else {
			for _, res := range arResults {
				origGID := originalGIDs[res.GID]
				if origGID == "" {
					origGID = "ar_" + res.GID
				}
				results = append(results, MultiCallItemResult{
					GID:   origGID,
					OK:    res.OK,
					Error: res.Error,
				})
			}
		}
	}

	return results, nil
}

func (h *HybridEngine) Remove(gid string, deleteFile bool) error {
	engine, rawGid := h.splitGid(gid)
	if engine == "sg" {
		return h.surgeEngine.Remove(rawGid, deleteFile)
	}
	return h.aria2Engine.Remove(rawGid, deleteFile)
}

func (h *HybridEngine) TellStatus(gid string, keys []string) (Task, error) {
	engine, rawGid := h.splitGid(gid)
	if engine == "sg" {
		t, err := h.surgeEngine.TellStatus(rawGid, keys)
		if err != nil {
			return Task{}, err
		}
		t.GID = "sg_" + t.GID
		return t, nil
	}
	t, err := h.aria2Engine.TellStatus(rawGid, keys)
	if err != nil {
		return Task{}, err
	}
	t.GID = "ar_" + t.GID
	return t, nil
}

func (h *HybridEngine) TellStatusMulti(gids []string, keys []string) ([]Task, error) {
	var sgGids, arGids []string
	for _, gid := range gids {
		engine, rawGid := h.splitGid(gid)
		if engine == "sg" {
			sgGids = append(sgGids, rawGid)
		} else {
			arGids = append(arGids, rawGid)
		}
	}
	var results []Task
	if len(sgGids) > 0 {
		tasks, err := h.surgeEngine.TellStatusMulti(sgGids, keys)
		if err != nil {
			return nil, err
		}
		for i := range tasks {
			tasks[i].GID = "sg_" + tasks[i].GID
		}
		results = append(results, tasks...)
	}
	if len(arGids) > 0 {
		tasks, err := h.aria2Engine.TellStatusMulti(arGids, keys)
		if err != nil {
			return nil, err
		}
		for i := range tasks {
			tasks[i].GID = "ar_" + tasks[i].GID
		}
		results = append(results, tasks...)
	}
	return results, nil
}

func (h *HybridEngine) TellActive() ([]Task, error) {
	sgList, sgErr := h.surgeEngine.TellActive()
	if sgErr != nil {
		log.Printf("[Hybrid] Surge TellActive error: %v", sgErr)
	}
	arList, arErr := h.aria2Engine.TellActive()
	if arErr != nil {
		log.Printf("[Hybrid] Aria2 TellActive error: %v", arErr)
	}
	if sgErr != nil && arErr != nil {
		return nil, fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	var merged []Task
	for _, t := range sgList {
		t.GID = "sg_" + t.GID
		merged = append(merged, t)
	}
	for _, t := range arList {
		t.GID = "ar_" + t.GID
		merged = append(merged, t)
	}
	return merged, nil
}

func (h *HybridEngine) TellActiveProgress() ([]TaskProgress, error) {
	sgList, sgErr := h.surgeEngine.TellActiveProgress()
	if sgErr != nil {
		log.Printf("[Hybrid] Surge TellActiveProgress error: %v", sgErr)
	}
	arList, arErr := h.aria2Engine.TellActiveProgress()
	if arErr != nil {
		log.Printf("[Hybrid] Aria2 TellActiveProgress error: %v", arErr)
	}
	if sgErr != nil && arErr != nil {
		return nil, fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	var merged []TaskProgress
	for _, tp := range sgList {
		tp.GID = "sg_" + tp.GID
		merged = append(merged, tp)
	}
	for _, tp := range arList {
		tp.GID = "ar_" + tp.GID
		merged = append(merged, tp)
	}
	return merged, nil
}

func (h *HybridEngine) TellWaiting(offset, num int) ([]Task, error) {
	sgList, sgErr := h.surgeEngine.TellWaiting(0, offset+num)
	if sgErr != nil {
		log.Printf("[Hybrid] Surge TellWaiting error: %v", sgErr)
	}
	arList, arErr := h.aria2Engine.TellWaiting(0, offset+num)
	if arErr != nil {
		log.Printf("[Hybrid] Aria2 TellWaiting error: %v", arErr)
	}
	if sgErr != nil && arErr != nil {
		return nil, fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	var merged []Task
	for _, t := range sgList {
		t.GID = "sg_" + t.GID
		merged = append(merged, t)
	}
	for _, t := range arList {
		t.GID = "ar_" + t.GID
		merged = append(merged, t)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(merged) {
		return []Task{}, nil
	}
	end := offset + num
	if end > len(merged) || num <= 0 {
		end = len(merged)
	}
	return merged[offset:end], nil
}

func (h *HybridEngine) TellStopped(offset, num int) ([]Task, error) {
	sgList, sgErr := h.surgeEngine.TellStopped(0, offset+num)
	if sgErr != nil {
		log.Printf("[Hybrid] Surge TellStopped error: %v", sgErr)
	}
	arList, arErr := h.aria2Engine.TellStopped(0, offset+num)
	if arErr != nil {
		log.Printf("[Hybrid] Aria2 TellStopped error: %v", arErr)
	}
	if sgErr != nil && arErr != nil {
		return nil, fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	var merged []Task
	for _, t := range sgList {
		t.GID = "sg_" + t.GID
		merged = append(merged, t)
	}
	for _, t := range arList {
		t.GID = "ar_" + t.GID
		merged = append(merged, t)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(merged) {
		return []Task{}, nil
	}
	end := offset + num
	if end > len(merged) || num <= 0 {
		end = len(merged)
	}
	return merged[offset:end], nil
}

func (h *HybridEngine) GetGlobalStat() (GlobalStat, error) {
	sgStat, sgErr := h.surgeEngine.GetGlobalStat()
	if sgErr != nil {
		log.Printf("[Hybrid] Surge GetGlobalStat error: %v", sgErr)
	}
	arStat, arErr := h.aria2Engine.GetGlobalStat()
	if arErr != nil {
		log.Printf("[Hybrid] Aria2 GetGlobalStat error: %v", arErr)
	}
	if sgErr != nil && arErr != nil {
		return GlobalStat{}, fmt.Errorf("both engines failed: surge=%w, aria2=%w", sgErr, arErr)
	}
	var sgSpeed, arSpeed int64
	if s, err := strconv.ParseInt(sgStat.DownloadSpeed, 10, 64); err == nil {
		sgSpeed = s
	}
	if a, err := strconv.ParseInt(arStat.DownloadSpeed, 10, 64); err == nil {
		arSpeed = a
	}
	totalSpeed := sgSpeed + arSpeed
	return GlobalStat{
		DownloadSpeed: strconv.FormatInt(totalSpeed, 10),
	}, nil
}

func (h *HybridEngine) SaveSession() error {
	err1 := h.surgeEngine.SaveSession()
	err2 := h.aria2Engine.SaveSession()
	if err1 != nil {
		return err1
	}
	return err2
}

func (h *HybridEngine) ChangeGlobalOption(options map[string]string) error {
	err1 := h.surgeEngine.ChangeGlobalOption(options)
	err2 := h.aria2Engine.ChangeGlobalOption(options)
	if err1 != nil {
		return err1
	}
	return err2
}

func (h *HybridEngine) TellActiveLite() ([]Task, error) {
	arList, err := h.aria2Engine.TellActiveLite()
	if err != nil {
		return nil, err
	}
	result := make([]Task, 0, len(arList))
	for _, t := range arList {
		t.GID = "ar_" + t.GID
		result = append(result, t)
	}
	return result, nil
}

func (h *HybridEngine) TellWaitingLite(offset, num int) ([]Task, error) {
	arList, err := h.aria2Engine.TellWaitingLite(0, offset+num)
	if err != nil {
		return nil, err
	}
	var result []Task
	for _, t := range arList {
		t.GID = "ar_" + t.GID
		result = append(result, t)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(result) {
		return []Task{}, nil
	}
	end := offset + num
	if end > len(result) || num <= 0 {
		end = len(result)
	}
	return result[offset:end], nil
}

func (h *HybridEngine) TellStoppedLite(offset, num int) ([]Task, error) {
	arList, err := h.aria2Engine.TellStoppedLite(0, offset+num)
	if err != nil {
		return nil, err
	}
	var result []Task
	for _, t := range arList {
		t.GID = "ar_" + t.GID
		result = append(result, t)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(result) {
		return []Task{}, nil
	}
	end := offset + num
	if end > len(result) || num <= 0 {
		end = len(result)
	}
	return result[offset:end], nil
}

func (h *HybridEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	return h.surgeEngine.StreamEvents(ctx)
}

func (h *HybridEngine) IsSurgeActive() bool {
	if h.surgeEngine == nil {
		return false
	}
	return h.surgeEngine.IsSurgeActive()
}

func (h *HybridEngine) SurgeEngineRef() (*SurgeEngine, bool) {
	se, ok := h.surgeEngine.(*SurgeEngine)
	return se, ok
}

// ScaleWorkers adjusts the worker count for a download.
// Routes to SurgeEngine for sg_ prefixed GIDs; returns 0 for others.
func (h *HybridEngine) ScaleWorkers(gid string, delta int) int {
	engine, rawGid := h.splitGid(gid)
	if engine != "sg" {
		return 0
	}
	if se, ok := h.surgeEngine.(*SurgeEngine); ok {
		return se.ScaleWorkers(rawGid, delta)
	}
	return 0
}

// GetRateLimit returns the effective rate limit (bps) and whether a limit is active.
// Routes to SurgeEngine for sg_ prefixed GIDs; returns (0, false) for others.
func (h *HybridEngine) GetRateLimit(gid string) (int64, bool) {
	engine, rawGid := h.splitGid(gid)
	if engine != "sg" {
		return 0, false
	}
	if se, ok := h.surgeEngine.(*SurgeEngine); ok {
		return se.GetRateLimit(rawGid)
	}
	return 0, false
}

func (h *HybridEngine) Close() {
	if closer, ok := h.surgeEngine.(interface{ Close() }); ok {
		closer.Close()
	}
	if closer, ok := h.aria2Engine.(interface{ Close() }); ok {
		closer.Close()
	}
}
