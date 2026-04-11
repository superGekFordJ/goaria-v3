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
	"strings"
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

func RestartAria2(cfg *config.AppConfig) error {
	if cfg == nil {
		return fmt.Errorf("启动失败: 配置为空")
	}

	prepared, err := prepareBundledAria2Binary()
	if err != nil {
		return err
	}

	StopAria2()
	time.Sleep(1 * time.Second)

	return startPreparedAria2(cfg, prepared)
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
	if cfg == nil {
		return fmt.Errorf("启动失败: 配置为空")
	}

	prepared, err := prepareBundledAria2Binary()
	if err != nil {
		return err
	}

	return startPreparedAria2(cfg, prepared)
}

func startPreparedAria2(cfg *config.AppConfig, prepared preparedBundledAria2Binary) error {
	defer prepared.cleanup()

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

	args := []string{
		"--enable-rpc",
		"--rpc-allow-origin-all",
		"--rpc-listen-all=false",
		fmt.Sprintf("--rpc-listen-port=%s", cfg.RPCPort),
		fmt.Sprintf("--dir=%s", cleanDir),
		"--auto-file-renaming=true",
		"--allow-overwrite=false",
		fmt.Sprintf("--max-concurrent-downloads=%s", cfg.MaxConcurrentDownloads),
		fmt.Sprintf("--max-connection-per-server=%s", cfg.MaxConnections),
		fmt.Sprintf("--user-agent=%s", cfg.UserAgent),
		"--continue=true",
		"--seed-time=0",            // 下载完立即停止做种
		"--bt-save-metadata=false", // 防止元数据任务残留文件
		fmt.Sprintf("--save-session=%s", sessionPath),
		fmt.Sprintf("--input-file=%s", sessionPath),
		"--save-session-interval=10",
		"--force-save=false",
		"--quiet=true",
	}

	if cfg.RPCSecret != "" {
		args = append(args, fmt.Sprintf("--rpc-secret=%s", cfg.RPCSecret))
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
	if aria2Cmd != nil && aria2Cmd.Process != nil {
		_ = aria2Cmd.Process.Kill()
		_, _ = aria2Cmd.Process.Wait()
		aria2Cmd = nil
	}
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
