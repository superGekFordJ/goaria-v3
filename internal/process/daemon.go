package process

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
)

type bundledAria2Source struct {
	targetOS      string
	embeddedPath  string
	extractedName string
	prepareHint   string
	bytes         []byte
	loadErr       error
}

type preparedBundledAria2Binary struct {
	source        bundledAria2Source
	finalPath     string
	candidatePath string
}

var (
	aria2Mu                    sync.Mutex
	aria2Cmd                   *exec.Cmd
	readFile                   = os.ReadFile
	writeFile                  = os.WriteFile
	statFile                   = os.Stat
	mkdirAll                   = os.MkdirAll
	userHomeDir                = os.UserHomeDir
	createTempFile             = os.CreateTemp
	removeFile                 = os.Remove
	validateBundledAria2Binary = defaultValidateBundledAria2Binary
	killAllAria2Processes      = KillAllOldProcesses
	currentBundledAria2        = bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/unsupported/aria2c",
		extractedName: defaultBundledAria2Name(),
		prepareHint:   "bundled aria2 is only wired for windows/linux/darwin in this branch",
		loadErr:       fs.ErrNotExist,
	}
)

func newBundledAria2Source(embedded fs.ReadFileFS, embeddedPath, extractedName, targetOS, prepareHint string) bundledAria2Source {
	data, err := embedded.ReadFile(embeddedPath)

	return bundledAria2Source{
		targetOS:      targetOS,
		embeddedPath:  embeddedPath,
		extractedName: extractedName,
		prepareHint:   prepareHint,
		bytes:         data,
		loadErr:       err,
	}
}

func (source bundledAria2Source) target() string {
	return fmt.Sprintf("%s/%s", source.targetOS, runtime.GOARCH)
}

func (source bundledAria2Source) remediation() string {
	return fmt.Sprintf("Rebuild after staging a target-compatible aria2c at %q (%s).", source.embeddedPath, source.prepareHint)
}

func (prepared *preparedBundledAria2Binary) cleanup() {
	if prepared.candidatePath == "" {
		return
	}

	if err := removeFile(prepared.candidatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}

	prepared.candidatePath = ""
}

func defaultBundledAria2Name() string {
	if runtime.GOOS == "windows" {
		return "aria2c.exe"
	}

	return "aria2c"
}

func KillAllOldProcesses() {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("taskkill", "/F", "/IM", "aria2c.exe", "/T")
		configureCommand(cmd)
		_ = cmd.Run()
	} else {
		_ = exec.Command("pkill", "-9", "aria2").Run()
	}
	time.Sleep(300 * time.Millisecond)
}

// Aria2RestartError reports a RestartAria2 failure and whether the previous
// process was already stopped. Callers can skip a restore restart when Stopped
// is false (prepare/dir validation failed before kill).
type Aria2RestartError struct {
	Err     error
	Stopped bool
}

func (e *Aria2RestartError) Error() string {
	if e == nil || e.Err == nil {
		return "aria2 restart failed"
	}
	return e.Err.Error()
}

func (e *Aria2RestartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapRestartErr(err error, stopped bool) error {
	if err == nil {
		return nil
	}
	return &Aria2RestartError{Err: err, Stopped: stopped}
}

func RestartAria2(cfg *config.AppConfig) error {
	aria2Mu.Lock()
	defer aria2Mu.Unlock()
	return restartAria2Locked(cfg)
}

func restartAria2Locked(cfg *config.AppConfig) error {
	if cfg == nil {
		return wrapRestartErr(errors.New("启动失败: 配置为空"), false)
	}

	prepared, err := prepareBundledAria2Binary()
	if err != nil {
		return wrapRestartErr(err, false)
	}

	if err := ValidateDownloadDir(cfg.DownloadDir); err != nil {
		prepared.cleanup()
		return wrapRestartErr(err, false)
	}

	stopAria2Locked()
	time.Sleep(1 * time.Second)

	return wrapRestartErr(startPreparedAria2Locked(cfg, prepared), true)
}

func prepareBundledAria2Binary() (prepared preparedBundledAria2Binary, err error) {
	source := currentBundledAria2
	prepared.source = source

	if source.loadErr != nil || len(source.bytes) == 0 {
		detail := "embedded bundle is empty"
		if source.loadErr != nil {
			detail = source.loadErr.Error()
		}

		return prepared, fmt.Errorf("bundled aria2 staging missing for %s: expected staged input %q (%s). %s", source.target(), source.embeddedPath, detail, source.remediation())
	}

	home, err := userHomeDir()
	if err != nil {
		return prepared, fmt.Errorf("bundled aria2 runtime preparation failed for %s: could not resolve the GoAria runtime directory: %w", source.target(), err)
	}

	appDataDir := filepath.Join(home, ".goaria")
	if err := mkdirAll(appDataDir, 0o755); err != nil {
		return prepared, fmt.Errorf("bundled aria2 runtime preparation failed for %s: could not create %q: %w", source.target(), appDataDir, err)
	}

	prepared.finalPath = filepath.Join(appDataDir, source.extractedName)

	matches, err := bundledAria2BinaryMatches(prepared.finalPath, source.bytes)
	if err != nil {
		return prepared, fmt.Errorf("bundled aria2 runtime inspection failed for %s at %q: %w. %s", source.target(), prepared.finalPath, err, source.remediation())
	}

	if matches {
		if err := ensureBundledBinaryPermissions(prepared.finalPath); err != nil {
			return prepared, fmt.Errorf("bundled aria2 runtime activation failed for %s at %q: could not apply permissions: %w. %s", source.target(), prepared.finalPath, err, source.remediation())
		}

		if err := validateBundledAria2Binary(prepared.finalPath, source); err != nil {
			return prepared, fmt.Errorf("bundled aria2 validation failed for %s at %q: %w. %s", source.target(), prepared.finalPath, err, source.remediation())
		}

		return prepared, nil
	}

	prepared.candidatePath, err = stageBundledAria2Candidate(appDataDir, source)
	if err != nil {
		return prepared, fmt.Errorf("bundled aria2 staging failed for %s: could not write the candidate runtime into %q: %w. %s", source.target(), appDataDir, err, source.remediation())
	}

	defer func() {
		if err != nil {
			prepared.cleanup()
		}
	}()

	if err := ensureBundledBinaryPermissions(prepared.candidatePath); err != nil {
		return prepared, fmt.Errorf("bundled aria2 staging failed for %s at %q: could not apply permissions to the candidate runtime: %w. %s", source.target(), prepared.candidatePath, err, source.remediation())
	}

	if err := validateBundledAria2Binary(prepared.candidatePath, source); err != nil {
		return prepared, fmt.Errorf("bundled aria2 validation failed for %s at staged candidate %q: %w. %s", source.target(), prepared.candidatePath, err, source.remediation())
	}

	return prepared, nil
}

func StartAria2(cfg *config.AppConfig) error {
	aria2Mu.Lock()
	defer aria2Mu.Unlock()
	return startAria2Locked(cfg)
}

func startAria2Locked(cfg *config.AppConfig) error {
	if cfg == nil {
		return errors.New("启动失败: 配置为空")
	}

	prepared, err := prepareBundledAria2Binary()
	if err != nil {
		return err
	}

	if err := ValidateDownloadDir(cfg.DownloadDir); err != nil {
		prepared.cleanup()
		return err
	}

	return startPreparedAria2Locked(cfg, prepared)
}

func startPreparedAria2Locked(cfg *config.AppConfig, prepared preparedBundledAria2Binary) error {
	defer prepared.cleanup()

	stopAria2Locked()
	killAllAria2Processes()

	aria2Path, err := activatePreparedBundledAria2Binary(prepared)
	if err != nil {
		return err
	}

	home, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("启动失败: 无法解析用户目录: %w", err)
	}

	appDataDir := filepath.Join(home, ".goaria")
	if err := mkdirAll(appDataDir, 0o755); err != nil {
		return fmt.Errorf("启动失败: 无法创建运行目录 %q: %w", appDataDir, err)
	}

	cleanDir := filepath.Clean(cfg.DownloadDir)
	sessionPath := filepath.Join(appDataDir, "aria2.session")
	if _, err := statFile(sessionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := writeFile(sessionPath, []byte(""), 0o644); err != nil {
				return fmt.Errorf("启动失败: 无法创建 session 文件 %q: %w", sessionPath, err)
			}
		} else {
			return fmt.Errorf("启动失败: 无法检查 session 文件 %q: %w", sessionPath, err)
		}
	}

	// aria2c hard-codes max-connection-per-server to 16; values above 16
	// are only meaningful for the Surge engine. Clamp here to prevent
	// aria2c from rejecting the config and breaking all RPC requests.
	aria2MaxConn := EffectiveAria2MaxConnections(cfg.MaxConnections)

	args := []string{
		"--enable-rpc",
		"--rpc-allow-origin-all",
		"--rpc-listen-all=false",
		"--rpc-listen-port=" + cfg.RPCPort,
		"--dir=" + cleanDir,
		"--auto-file-renaming=true",
		"--allow-overwrite=false",
		"--max-concurrent-downloads=" + cfg.MaxConcurrentDownloads,
		fmt.Sprintf("--max-connection-per-server=%d", aria2MaxConn),
		"--user-agent=" + cfg.UserAgent,
		"--continue=true",
		"--seed-time=0",            // 下载完立即停止做种
		"--bt-save-metadata=false", // 防止元数据任务残留文件
		"--save-session=" + sessionPath,
		"--input-file=" + sessionPath,
		"--save-session-interval=10",
		"--force-save=false",
		"--quiet=true",
	}

	if cfg.RPCSecret != "" {
		args = append(args, "--rpc-secret="+cfg.RPCSecret)
	}

	cmd := exec.Command(aria2Path, args...)
	configureCommand(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动失败: bundled aria2 launch failed: %w", err)
	}

	aria2Cmd = cmd
	return nil
}

func StopAria2() {
	aria2Mu.Lock()
	defer aria2Mu.Unlock()
	stopAria2Locked()
}

func stopAria2Locked() {
	if aria2Cmd != nil && aria2Cmd.Process != nil {
		_ = aria2Cmd.Process.Kill()
		_, _ = aria2Cmd.Process.Wait()
		aria2Cmd = nil
	}
}

// ValidateDownloadDir ensures dir exists, is a directory, and is writable.
// Empty values are rejected; unreachable paths are not rewritten to Downloads.
func ValidateDownloadDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("download directory is empty")
	}
	clean := filepath.Clean(dir)
	info, err := statFile(clean)
	switch {
	case err == nil:
		if !info.IsDir() {
			return errors.New("download directory is not a directory")
		}
	case errors.Is(err, os.ErrNotExist):
		if mkErr := mkdirAll(clean, 0o755); mkErr != nil {
			return fmt.Errorf("download directory unavailable: %w", mkErr)
		}
	default:
		return fmt.Errorf("download directory unavailable: %w", err)
	}

	probe, err := createTempFile(clean, ".goaria-dirprobe-*")
	if err != nil {
		return fmt.Errorf("download directory not writable: %w", err)
	}
	probeName := probe.Name()
	closeErr := probe.Close()
	_ = removeFile(probeName)
	if closeErr != nil {
		return fmt.Errorf("download directory not writable: %w", closeErr)
	}
	return nil
}

// EffectiveAria2MaxConnections returns the Aria2 launch value: min(parsed, 16).
// Invalid input falls back to the product default (16).
func EffectiveAria2MaxConnections(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		n, _ = strconv.Atoi(config.DefaultMaxConnections)
	}
	if n > config.Aria2MaxConnectionsPerServer {
		return config.Aria2MaxConnectionsPerServer
	}
	return n
}

func defaultValidateBundledAria2Binary(path string, source bundledAria2Source) error {
	cmd := exec.Command(path, "--version")
	configureCommand(cmd)

	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return err
	}

	return fmt.Errorf("%w: %s", err, trimmed)
}

func bundledAria2BinaryMatches(path string, data []byte) (bool, error) {
	info, err := statFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, err
	}

	if info.Size() != int64(len(data)) {
		return false, nil
	}

	content, err := readFile(path)
	if err != nil {
		return false, err
	}

	return bytes.Equal(content, data), nil
}

func stageBundledAria2Candidate(appDataDir string, source bundledAria2Source) (string, error) {
	tempFile, err := createTempFile(appDataDir, source.extractedName+".candidate-*")
	if err != nil {
		return "", err
	}

	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = removeFile(tempPath)
		return "", err
	}

	if err := writeFile(tempPath, source.bytes, 0o700); err != nil {
		_ = removeFile(tempPath)
		return "", err
	}

	return tempPath, nil
}

func activatePreparedBundledAria2Binary(prepared preparedBundledAria2Binary) (string, error) {
	if prepared.candidatePath == "" {
		return prepared.finalPath, nil
	}

	if err := replaceBundledBinary(prepared.candidatePath, prepared.finalPath); err != nil {
		return "", fmt.Errorf("bundled aria2 activation failed for %s: could not promote validated candidate %q into %q: %w. %s", prepared.source.target(), prepared.candidatePath, prepared.finalPath, err, prepared.source.remediation())
	}

	return prepared.finalPath, nil
}
