package rpc

import (
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
	if len(sgGids) > 0 {
		if err := h.surgeEngine.PauseMulti(sgGids); err != nil {
			return err
		}
	}
	if len(arGids) > 0 {
		if err := h.aria2Engine.PauseMulti(arGids); err != nil {
			return err
		}
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
	if len(sgGids) > 0 {
		if err := h.surgeEngine.ResumeMulti(sgGids); err != nil {
			return err
		}
	}
	if len(arGids) > 0 {
		if err := h.aria2Engine.ResumeMulti(arGids); err != nil {
			return err
		}
	}
	return nil
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
	sgList, err := h.surgeEngine.TellActive()
	if err != nil {
		return nil, err
	}
	arList, err := h.aria2Engine.TellActive()
	if err != nil {
		return nil, err
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
	sgList, err := h.surgeEngine.TellActiveProgress()
	if err != nil {
		return nil, err
	}
	arList, err := h.aria2Engine.TellActiveProgress()
	if err != nil {
		return nil, err
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
	sgList, err := h.surgeEngine.TellWaiting(0, offset+num)
	if err != nil {
		return nil, err
	}
	arList, err := h.aria2Engine.TellWaiting(0, offset+num)
	if err != nil {
		return nil, err
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
	sgList, err := h.surgeEngine.TellStopped(0, offset+num)
	if err != nil {
		return nil, err
	}
	arList, err := h.aria2Engine.TellStopped(0, offset+num)
	if err != nil {
		return nil, err
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
	sgStat, err := h.surgeEngine.GetGlobalStat()
	if err != nil {
		return GlobalStat{}, err
	}
	arStat, err := h.aria2Engine.GetGlobalStat()
	if err != nil {
		return GlobalStat{}, err
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

func (h *HybridEngine) Close() {
	if closer, ok := h.surgeEngine.(interface{ Close() }); ok {
		closer.Close()
	}
	if closer, ok := h.aria2Engine.(interface{ Close() }); ok {
		closer.Close()
	}
}
