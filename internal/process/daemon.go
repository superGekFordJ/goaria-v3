package process

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"goaria-v3/internal/config"
)

//go:embed aria2c.exe
var aria2cBin []byte

var aria2Cmd *exec.Cmd

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
	StopAria2()
	time.Sleep(1 * time.Second)
	return StartAria2(cfg)
}

func StartAria2(cfg *config.AppConfig) error {
	KillAllOldProcesses()

	home, _ := os.UserHomeDir()
	appDataDir := filepath.Join(home, ".goaria")
	os.MkdirAll(appDataDir, 0o755)

	aria2Path := filepath.Join(appDataDir, "aria2c.exe")
	if runtime.GOOS != "windows" {
		aria2Path = filepath.Join(appDataDir, "aria2c")
	}

	_ = extractAria2Binary(aria2Path)

	cleanDir := filepath.Clean(cfg.DownloadDir)
	sessionPath := filepath.Join(appDataDir, "aria2.session")
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		_ = os.WriteFile(sessionPath, []byte(""), 0o644)
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
	if runtime.GOOS == "windows" {
		configureCommand(cmd)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动失败: %w", err)
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

func extractAria2Binary(path string) error {
	info, err := os.Stat(path)
	if err == nil && info.Size() == int64(len(aria2cBin)) {
		content, err := os.ReadFile(path)
		if err == nil && bytes.Equal(content, aria2cBin) {
			return nil
		}
	}
	return os.WriteFile(path, aria2cBin, 0o755)
}
