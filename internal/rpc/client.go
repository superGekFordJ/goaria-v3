package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"goaria-v3/internal/config"
)

var (
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

type Task struct {
	GID             string `json:"gid"`
	Status          string `json:"status"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	ErrorCode       string `json:"errorCode"`
	ErrorMessage    string `json:"errorMessage"`
	Dir             string `json:"dir"`
	Files           []File `json:"files"`
}

type TaskProgress struct {
	GID             string `json:"gid"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
}

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
	params := []any{
		[]string{url},
		map[string]string{"dir": downloadDir},
	}
	_, err := sendRequest("aria2.addUri", params)
	if err == nil {
		ForceSaveSession()
	}
	return err
}

// AddUriWithOptions 添加下载任务，支持动态线程参数
// 返回 (gid, error)
func AddUriWithOptions(url string, downloadDir string, split int, minSplitSize int64) (string, error) {
	options := map[string]string{"dir": downloadDir}
	if split > 0 {
		options["split"] = strconv.Itoa(split)
		options["max-connection-per-server"] = strconv.Itoa(split)
	}
	if minSplitSize > 0 {
		options["min-split-size"] = strconv.FormatInt(minSplitSize, 10)
	}
	params := []any{
		[]string{url},
		options,
	}
	resp, err := sendRequest("aria2.addUri", params)
	if err != nil {
		return "", err
	}
	ForceSaveSession()

	var result struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
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

// TellStatus gets detailed status for a specific task by GID
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

func TellActive() ([]Task, error) { return getTasks("aria2.tellActive", nil) }
func TellWaiting(offset, num int) ([]Task, error) {
	return getTasks("aria2.tellWaiting", []any{offset, num})
}
func TellStopped(offset, num int) ([]Task, error) {
	return getTasks("aria2.tellStopped", []any{offset, num})
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

func getTasks(method string, extraParams []any) ([]Task, error) {
	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "files", "dir"}
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
	}
	json.Unmarshal(resp, &result)
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
	finalParams := params
	if currentSecret != "" {
		finalParams = append([]any{"token:" + currentSecret}, params...)
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": "goaria", "method": method, "params": finalParams}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(payload)
	resp, err := httpClient.Post(currentURL, "application/json", &buf)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var resBuf bytes.Buffer
	resBuf.ReadFrom(resp.Body)
	return resBuf.Bytes(), nil
}

func WaitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := GetGlobalStat(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("Aria2 无响应")
}
