package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
)

const (
	maxAddURIHeaders        = 64
	maxAddURIHeaderLineSize = 8 * 1024

	DownloadGroupNameStatusStable   = "stable"
	DownloadGroupNameStatusPending  = "pending"
	DownloadGroupNameStatusFallback = "fallback"
	DownloadGroupNameStatusDegraded = "degraded"
)

var (
	bufferPool = sync.Pool{
		New: func() any {
			return new(bytes.Buffer)
		},
	}

	currentURL    string
	currentSecret string
	httpClient    = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     30 * time.Second,
		},
	}
)

func Init(port, secret string) {
	currentURL = fmt.Sprintf("http://127.0.0.1:%s/jsonrpc", port)
	currentSecret = secret
}

type Uri struct {
	Uri    string `json:"uri"`
	Status string `json:"status"`
}

type File struct {
	Path string `json:"path"`
	Uris []Uri  `json:"uris"`
}

type DownloadGroup struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	NameStatus string `json:"name_status,omitempty"`
	FolderName string `json:"folder_name"`
	Dir        string `json:"dir"`
	ItemCount  int    `json:"item_count"`
	CreatedAt  int64  `json:"created_at"`
}

func IsDownloadGroupNameStatus(status string) bool {
	switch status {
	case DownloadGroupNameStatusStable,
		DownloadGroupNameStatusPending,
		DownloadGroupNameStatusFallback,
		DownloadGroupNameStatusDegraded:
		return true
	default:
		return false
	}
}

type Task struct {
	GID             string         `json:"gid"`
	Title           string         `json:"title,omitempty"`
	Status          string         `json:"status"`
	TotalLength     string         `json:"totalLength"`
	CompletedLength string         `json:"completedLength"`
	DownloadSpeed   string         `json:"downloadSpeed"`
	ErrorCode       string         `json:"errorCode"`
	ErrorMessage    string         `json:"errorMessage"`
	Dir             string         `json:"dir"`
	Files           []File         `json:"files"`
	DownloadGroup   *DownloadGroup `json:"download_group,omitempty"`
}

type TaskProgress struct {
	GID             string `json:"gid"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
}

type MultiCallItemResult struct {
	GID   string
	OK    bool
	Error string
}

type AddURIOptions struct {
	Dir          string
	Out          string
	Headers      []string
	Split        int
	MinSplitSize int64
}

type AddURIHook func(gid string) error

func (t Task) GetTitle() string {
	if len(t.Files) > 0 && t.Files[0].Path != "" {
		return filepath.Base(t.Files[0].Path)
	}
	return t.GID
}

// --- 核心控制 ---

func ForceSaveSession() {
	_, _ = sendRequest("aria2.saveSession", nil)
}

// ChangeGlobalOption 强制修改运行中的配置
func ChangeGlobalOption(options map[string]string) error {
	_, err := sendRequest("aria2.changeGlobalOption", []any{options})
	return err
}

func Remove(gid string) error {
	// 1. 从活跃列表移除
	_, _ = sendRequest("aria2.remove", []any{gid})
	// 2. 从结果列表移除 (这是让 .aria2 文件消失的关键)
	_, _ = sendRequest("aria2.removeDownloadResult", []any{gid})
	ForceSaveSession()
	return nil
}

func AddUri(url string, downloadDir string) error {
	_, err := AddUriWithAria2Options(url, AddURIOptions{Dir: downloadDir})
	return err
}

// AddUriWithOptions 添加下载任务，支持动态线程参数
// 返回 (gid, error)
func AddUriWithOptions(url string, downloadDir string, split int, minSplitSize int64) (string, error) {
	return AddUriWithAria2Options(url, AddURIOptions{
		Dir:          downloadDir,
		Split:        split,
		MinSplitSize: minSplitSize,
	})
}

func AddUriWithAria2Options(url string, options AddURIOptions) (string, error) {
	return AddUriWithAria2OptionsHook(url, options, nil)
}

func AddUriWithAria2OptionsHook(url string, options AddURIOptions, beforeSave AddURIHook) (string, error) {
	aria2Options, err := buildAddURIOptions(options)
	if err != nil {
		return "", err
	}
	params := []any{
		[]string{url},
		aria2Options,
	}
	resp, err := sendRequest("aria2.addUri", params)
	if err != nil {
		return "", err
	}
	gid, err := parseAddURIResponse(resp)
	if err != nil {
		return "", err
	}
	if beforeSave != nil && gid != "" {
		if err := beforeSave(gid); err != nil {
			return "", err
		}
	}
	ForceSaveSession()

	return gid, nil
}

func buildAddURIOptions(options AddURIOptions) (map[string]any, error) {
	if err := validateAddURIHeaders(options.Headers); err != nil {
		return nil, err
	}

	aria2Options := map[string]any{"dir": options.Dir}
	if options.Out != "" {
		aria2Options["out"] = options.Out
	}
	if len(options.Headers) > 0 {
		headers := make([]string, len(options.Headers))
		copy(headers, options.Headers)
		aria2Options["header"] = headers
	}
	if options.Split > 0 {
		split := strconv.Itoa(options.Split)
		aria2Options["split"] = split
		aria2Options["max-connection-per-server"] = split
	}
	if options.MinSplitSize > 0 {
		aria2Options["min-split-size"] = strconv.FormatInt(options.MinSplitSize, 10)
	}

	return aria2Options, nil
}

func validateAddURIHeaders(headers []string) error {
	if len(headers) > maxAddURIHeaders {
		return fmt.Errorf("aria2 header count exceeds %d", maxAddURIHeaders)
	}

	for _, header := range headers {
		line := strings.TrimSpace(header)
		if line == "" {
			return fmt.Errorf("aria2 header line must be non-empty")
		}
		if line != header {
			return fmt.Errorf("aria2 header line must be trimmed")
		}
		if len(line) > maxAddURIHeaderLineSize {
			return fmt.Errorf("aria2 header line exceeds %d bytes", maxAddURIHeaderLineSize)
		}
		if strings.ContainsAny(line, "\r\n") {
			return fmt.Errorf("aria2 header line must not contain CR/LF")
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("aria2 header line must be in name: value form")
		}
		if strings.TrimSpace(name) != name {
			return fmt.Errorf("aria2 header name must be trimmed")
		}
		for _, r := range name {
			if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
				return fmt.Errorf("aria2 header name contains invalid characters")
			}
		}
	}

	return nil
}

func parseAddURIResponse(resp []byte) (string, error) {
	var result struct {
		Result any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("aria2.addUri: rpc error %d: %s", result.Error.Code, result.Error.Message)
	}
	if gid, ok := result.Result.(string); ok {
		return gid, nil
	}

	return "", nil
}

// HeadContentLength 发送 HEAD 请求获取文件大小
// 返回 Content-Length (bytes)，失败返回 0
// timeout: 超时时间
func HeadContentLength(url string, timeout time.Duration) int64 {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0
	}

	ua := ""
	if config.Current != nil {
		ua = config.Current.UserAgent
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		if resp.ContentLength > 0 {
			return resp.ContentLength
		}
	}
	return 0
}

func Pause(gid string) error {
	_, err := sendRequest("aria2.pause", []any{gid})
	ForceSaveSession()
	return err
}

func Unpause(gid string) error {
	_, err := sendRequest("aria2.unpause", []any{gid})
	ForceSaveSession()
	return err
}

// --- 数据获取 ---

func TellStatusMulti(gids []string) ([]*Task, error) {
	if len(gids) == 0 {
		return nil, nil
	}

	secret := currentSecret
	hasSecret := secret != ""
	token := ""
	numParams := 2
	if hasSecret {
		token = "token:" + secret
		numParams = 3
	}

	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "files", "dir"}
	calls := make([]any, 0, len(gids))
	for _, gid := range gids {
		params := make([]any, 0, numParams)
		if hasSecret {
			params = append(params, token)
		}
		params = append(params, gid, keys)

		calls = append(calls, map[string]any{
			"methodName": "aria2.tellStatus",
			"params":     params,
		})
	}

	resp, err := sendRequestInternal("system.multicall", []any{calls}, false)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result []json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	if result.Error != nil && result.Error.Message != "" {
		return nil, fmt.Errorf("system.multicall: rpc error %d: %s", result.Error.Code, result.Error.Message)
	}

	tasks := make([]*Task, 0, len(result.Result))
	for _, raw := range result.Result {
		var batch []Task
		if err := json.Unmarshal(raw, &batch); err != nil || len(batch) == 0 {
			continue
		}
		task := batch[0]
		tasks = append(tasks, &task)
	}

	return tasks, nil
}

func TellStatus(gid string) (*Task, error) {
	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "files", "dir"}
	params := []any{gid, keys}
	resp, err := sendRequest("aria2.tellStatus", params)
	if err != nil {
		return nil, err
	}
	var result struct {
		Result Task `json:"result"`
	}
	json.Unmarshal(resp, &result)
	return &result.Result, nil
}

func TellActive() ([]Task, error) { return getTasks("aria2.tellActive", nil, nil) }
func TellWaiting(offset, num int) ([]Task, error) {
	return getTasks("aria2.tellWaiting", []any{offset, num}, nil)
}

func TellStopped(offset, num int) ([]Task, error) {
	return getTasks("aria2.tellStopped", []any{offset, num}, nil)
}

func TellActiveLite() ([]Task, error) {
	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "dir"}
	return getTasks("aria2.tellActive", nil, keys)
}

func TellWaitingLite(offset, num int) ([]Task, error) {
	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "dir"}
	return getTasks("aria2.tellWaiting", []any{offset, num}, keys)
}

func TellStoppedLite(offset, num int) ([]Task, error) {
	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "dir"}
	return getTasks("aria2.tellStopped", []any{offset, num}, keys)
}

func TellActiveProgress() ([]TaskProgress, error) {
	keys := []string{"gid", "completedLength", "downloadSpeed"}
	resp, err := sendRequest("aria2.tellActive", []any{keys})
	if err != nil {
		return nil, err
	}
	var result struct {
		Result []TaskProgress `json:"result"`
	}
	json.Unmarshal(resp, &result)
	return result.Result, nil
}

func getTasks(method string, extraParams []any, keys []string) ([]Task, error) {
	if keys == nil {
		keys = []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "files", "dir"}
	}
	params := []any{}
	if extraParams != nil {
		params = append(params, extraParams...)
	}
	params = append(params, keys)
	resp, err := sendRequest(method, params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result []Task `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	if result.Error != nil && result.Error.Message != "" {
		return nil, fmt.Errorf("%s: rpc error %d: %s", method, result.Error.Code, result.Error.Message)
	}

	if result.Result == nil {
		return nil, fmt.Errorf("%s: unmarshal failed or empty result", method)
	}

	return result.Result, nil
}

func GetGlobalStat() (string, error) {
	resp, err := sendRequest("aria2.getGlobalStat", nil)
	if err != nil {
		return "0", err
	}
	var res struct {
		Result struct {
			Speed string `json:"downloadSpeed"`
		} `json:"result" `
	}
	json.Unmarshal(resp, &res)
	return res.Result.Speed, nil
}

func sendRequest(method string, params []any) ([]byte, error) {
	return sendRequestInternal(method, params, true)
}

func sendRequestInternal(method string, params []any, prependSecret bool) ([]byte, error) {
	finalParams := params
	if prependSecret && currentSecret != "" {
		finalParams = append([]any{"token:" + currentSecret}, params...)
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": "goaria", "method": method, "params": finalParams}

	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(payload); err != nil {
		return nil, err
	}
	resp, err := httpClient.Post(currentURL, "application/json", buf)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var resBuf bytes.Buffer
	resBuf.ReadFrom(resp.Body)
	return resBuf.Bytes(), nil
}

func WaitForReady(timeout time.Duration) error {
	// Check immediately
	if _, err := GetGlobalStat(); err == nil {
		return nil
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	timeoutChan := time.After(timeout)

	for {
		select {
		case <-timeoutChan:
			return fmt.Errorf("Aria2 无响应")
		case <-ticker.C:
			if _, err := GetGlobalStat(); err == nil {
				return nil
			}
		}
	}
}

func PauseMulti(gids []string) error {
	_, err := PauseMultiResults(gids)
	return err
}

func UnpauseMulti(gids []string) error {
	_, err := UnpauseMultiResults(gids)
	return err
}

func PauseMultiResults(gids []string) ([]MultiCallItemResult, error) {
	return pauseResumeMultiResults("aria2.pause", gids)
}

func UnpauseMultiResults(gids []string) ([]MultiCallItemResult, error) {
	return pauseResumeMultiResults("aria2.unpause", gids)
}

func pauseResumeMultiResults(method string, gids []string) ([]MultiCallItemResult, error) {
	if len(gids) == 0 {
		return []MultiCallItemResult{}, nil
	}

	secret := currentSecret
	hasSecret := secret != ""
	token := ""
	numParams := 1
	if hasSecret {
		token = "token:" + secret
		numParams = 2
	}

	calls := make([]any, 0, len(gids))
	for _, gid := range gids {
		params := make([]any, 0, numParams)
		if hasSecret {
			params = append(params, token)
		}
		params = append(params, gid)

		calls = append(calls, map[string]any{
			"methodName": method,
			"params":     params,
		})
	}

	resp, err := sendRequestInternal("system.multicall", []any{calls}, false)
	ForceSaveSession()
	if err != nil {
		return nil, err
	}

	return parseMultiCallItemResults(gids, resp)
}

func parseMultiCallItemResults(gids []string, resp []byte) ([]MultiCallItemResult, error) {
	var result struct {
		Result []json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("system.multicall: rpc error %d: %s", result.Error.Code, result.Error.Message)
	}
	if result.Result == nil {
		return nil, fmt.Errorf("system.multicall: missing result")
	}

	items := make([]MultiCallItemResult, 0, len(gids))
	for i, gid := range gids {
		item := MultiCallItemResult{GID: gid}
		if i >= len(result.Result) {
			item.Error = "missing multicall result"
			items = append(items, item)
			continue
		}
		ok, message := parseMultiCallNestedResult(result.Result[i])
		item.OK = ok
		if !ok {
			item.Error = message
		}
		items = append(items, item)
	}

	return items, nil
}

func parseMultiCallNestedResult(raw json.RawMessage) (bool, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, "empty multicall result"
	}
	if message, ok := parseMultiCallFault(raw); ok {
		return false, message
	}

	var batch []json.RawMessage
	if err := json.Unmarshal(raw, &batch); err == nil {
		if len(batch) == 0 {
			return false, "empty multicall result"
		}
		if message, ok := parseMultiCallFault(batch[0]); ok {
			return false, message
		}
		return true, ""
	}

	return true, ""
}

func parseMultiCallFault(raw json.RawMessage) (string, bool) {
	var fault struct {
		Code        *int   `json:"code"`
		Message     string `json:"message"`
		FaultCode   *int   `json:"faultCode"`
		FaultString string `json:"faultString"`
	}
	if err := json.Unmarshal(raw, &fault); err != nil {
		return "", false
	}
	if fault.Code != nil || fault.Message != "" {
		if fault.Message != "" {
			return fault.Message, true
		}
		return "rpc error", true
	}
	if fault.FaultCode != nil || fault.FaultString != "" {
		if fault.FaultString != "" {
			return fault.FaultString, true
		}
		return "rpc fault", true
	}
	return "", false
}
