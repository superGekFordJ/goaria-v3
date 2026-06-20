package rpc

import (
	"context"
	"encoding/json"
)

// DownloadEngine abstracts download operations.
type DownloadEngine interface {
	AddUri(url string, options AddURIOptions) (string, error)
	Pause(gid string) error
	Resume(gid string) error
	PauseMulti(gids []string) error
	ResumeMulti(gids []string) error
	Remove(gid string, deleteFile bool) error
	TellStatus(gid string, keys []string) (Task, error)
	TellStatusMulti(gids []string, keys []string) ([]Task, error)
	TellActive() ([]Task, error)
	TellActiveLite() ([]Task, error)
	TellActiveProgress() ([]TaskProgress, error)
	TellWaiting(offset, num int) ([]Task, error)
	TellWaitingLite(offset, num int) ([]Task, error)
	TellStopped(offset, num int) ([]Task, error)
	TellStoppedLite(offset, num int) ([]Task, error)
	GetGlobalStat() (GlobalStat, error)
	SaveSession() error
	ChangeGlobalOption(options map[string]string) error
	StreamEvents(ctx context.Context) (<-chan any, func(), error)
}

// Aria2Engine implements DownloadEngine by forwarding to the aria2 client package functions.
type Aria2Engine struct{}

func (e *Aria2Engine) AddUri(url string, options AddURIOptions) (string, error) {
	return AddUriWithAria2OptionsHook(url, options, options.BeforeSave)
}

func (e *Aria2Engine) Pause(gid string) error {
	return Pause(gid)
}

func (e *Aria2Engine) Resume(gid string) error {
	return Unpause(gid)
}

func (e *Aria2Engine) PauseMulti(gids []string) error {
	return PauseMulti(gids)
}

func (e *Aria2Engine) ResumeMulti(gids []string) error {
	return UnpauseMulti(gids)
}

func (e *Aria2Engine) Remove(gid string, deleteFile bool) error {
	return Remove(gid)
}

func (e *Aria2Engine) TellStatus(gid string, keys []string) (Task, error) {
	if len(keys) == 0 {
		keys = []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "files", "dir"}
	}
	params := []any{gid, keys}
	resp, err := sendRequest("aria2.tellStatus", params)
	if err != nil {
		return Task{}, err
	}
	var result struct {
		Result Task `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return Task{}, err
	}
	return result.Result, nil
}

func (e *Aria2Engine) TellStatusMulti(gids []string, keys []string) ([]Task, error) {
	if len(gids) == 0 {
		return nil, nil
	}
	tasks, err := TellStatusMulti(gids)
	if err != nil {
		return nil, err
	}
	res := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t != nil {
			res = append(res, *t)
		}
	}
	return res, nil
}

func (e *Aria2Engine) TellActive() ([]Task, error) {
	return TellActive()
}

func (e *Aria2Engine) TellActiveLite() ([]Task, error) {
	return TellActiveLite()
}

func (e *Aria2Engine) TellActiveProgress() ([]TaskProgress, error) {
	return TellActiveProgress()
}

func (e *Aria2Engine) TellWaiting(offset, num int) ([]Task, error) {
	return TellWaiting(offset, num)
}

func (e *Aria2Engine) TellWaitingLite(offset, num int) ([]Task, error) {
	return TellWaitingLite(offset, num)
}

func (e *Aria2Engine) TellStopped(offset, num int) ([]Task, error) {
	return TellStopped(offset, num)
}

func (e *Aria2Engine) TellStoppedLite(offset, num int) ([]Task, error) {
	return TellStoppedLite(offset, num)
}

func (e *Aria2Engine) GetGlobalStat() (GlobalStat, error) {
	return GetGlobalStat()
}

func (e *Aria2Engine) SaveSession() error {
	ForceSaveSession()
	return nil
}

func (e *Aria2Engine) ChangeGlobalOption(options map[string]string) error {
	return ChangeGlobalOption(options)
}

func (e *Aria2Engine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	return nil, func() {}, nil
}
