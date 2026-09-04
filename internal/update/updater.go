package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/events"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	maxArchiveEntries         = 50_000
	maxArchiveTotalSize int64 = 2 << 30 // 2 GiB total uncompressed limit
)

type archiveKind int

const (
	archiveNone archiveKind = iota
	archiveZip
	archiveTarGz
)

var (
	selfExecutable = os.Executable
	startHelperCmd = func(cmd *exec.Cmd) error {
		return cmd.Start()
	}
	quitApplication = func() {
		if app := application.Get(); app != nil {
			app.Quit()
		} else {
			os.Exit(0)
		}
	}
)

// Updater manages the update download, extraction, staging, and helper replacement process.
type Updater struct {
	eventHub   *events.Hub
	mu         sync.RWMutex
	busy       bool
	stagedPath string
	stagingDir string
}

// NewUpdater creates a new Updater instance
func NewUpdater(hub *events.Hub) *Updater {
	return &Updater{
		eventHub: hub,
	}
}

// StagedPath returns the current staged executable or bundle path.
func (u *Updater) StagedPath() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.stagedPath
}

// StagingDir returns the temporary directory holding staged artifacts.
func (u *Updater) StagingDir() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.stagingDir
}

// Apply downloads the update asset and stages it for replacement.
// This method runs synchronously — callers should invoke it in a goroutine.
func (u *Updater) Apply(info *ReleaseInfo) error {
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return errors.New("update already in progress")
	}
	u.busy = true

	// Clean up any old staging directory from prior attempts
	if u.stagingDir != "" {
		_ = os.RemoveAll(u.stagingDir)
		u.stagingDir = ""
		u.stagedPath = ""
	}
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	if info == nil || info.AssetURL == "" {
		u.emitStatus(StatusError, "no asset URL provided")
		return errors.New("no asset URL provided")
	}

	u.emitStatus(StatusDownloading, nil)

	stagingDir, err := os.MkdirTemp("", "wails-update-*")
	if err != nil {
		u.emitStatus(StatusError, err.Error())
		return fmt.Errorf("create staging directory: %w", err)
	}

	failCleanup := func(err error) error {
		_ = os.RemoveAll(stagingDir)
		u.emitStatus(StatusError, err.Error())
		return err
	}

	log.Printf("[Update] Downloading asset: %s", info.AssetURL)
	resp, err := http.Get(info.AssetURL)
	if err != nil {
		return failCleanup(fmt.Errorf("download failed: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return failCleanup(fmt.Errorf("download returned HTTP %d", resp.StatusCode))
	}

	totalSize := resp.ContentLength
	if info.AssetSize > 0 {
		totalSize = info.AssetSize
	}

	u.emitProgress(0, totalSize, 0)

	assetFilename := "update_asset"
	if parsed, err := url.Parse(info.AssetURL); err == nil {
		base := filepath.Base(parsed.Path)
		if base != "" && base != "." && base != "/" {
			assetFilename = base
		}
	}
	downloadPath := filepath.Join(stagingDir, assetFilename)

	destFile, err := os.OpenFile(downloadPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return failCleanup(fmt.Errorf("create download destination: %w", err))
	}

	reader := &progressReader{
		reader:    resp.Body,
		total:     totalSize,
		updater:   u,
		lastEmit:  time.Now(),
		emitEvery: 250 * time.Millisecond,
	}

	written, err := io.Copy(destFile, reader)
	_ = destFile.Close()
	if err != nil {
		return failCleanup(fmt.Errorf("download interrupted: %w", err))
	}

	u.emitProgress(written, totalSize, 0)
	log.Printf("[Update] Download complete (%d bytes)", written)

	staged, err := stageDownloadedAsset(downloadPath, stagingDir, currentPlatform())
	if err != nil {
		return failCleanup(fmt.Errorf("staging failed: %w", err))
	}

	u.mu.Lock()
	u.stagedPath = staged
	u.stagingDir = stagingDir
	u.mu.Unlock()

	log.Printf("[Update] Update staged successfully at: %s", staged)
	u.emitStatus(StatusReady, nil)
	return nil
}

// Restart launches a new instance of the application via Wails 3 Helper protocol and exits the current one.
func (u *Updater) Restart() error {
	u.mu.RLock()
	staged := u.stagedPath
	u.mu.RUnlock()

	if staged == "" {
		return errors.New("updater: nothing to restart into (download update first)")
	}

	self, err := selfExecutable()
	if err != nil {
		return fmt.Errorf("cannot find executable: %w", err)
	}

	target := bundleTarget(self)
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("wails-update-%d.log", os.Getpid()))

	env := append(os.Environ(),
		"WAILS_UPDATER_HELPER=1",
		"WAILS_UPDATER_HELPER_TARGET="+target,
		"WAILS_UPDATER_HELPER_NEW="+staged,
		"WAILS_UPDATER_HELPER_PID="+strconv.Itoa(os.Getpid()),
		"WAILS_UPDATER_HELPER_LOG="+logPath,
	)

	cmd := exec.Command(self)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	applyDetachAttrs(cmd)

	log.Printf("[Update] Spawning updater helper: target=%s, new=%s, pid=%d", target, staged, os.Getpid())
	if err := startHelperCmd(cmd); err != nil {
		return fmt.Errorf("spawn updater helper failed: %w", err)
	}

	quitApplication()
	return nil
}

// bundleTargetPath resolves .app on macOS, AppImage on Linux, or leaves exe unchanged on other platforms.
func bundleTargetPath(exe, goos string) string {
	switch goos {
	case "darwin":
		clean := strings.ReplaceAll(filepath.Clean(exe), `\`, "/")
		parts := strings.Split(clean, "/")
		for i, p := range parts {
			if strings.HasSuffix(p, ".app") {
				joined := strings.Join(parts[1:i+1], "/")
				if strings.HasPrefix(clean, "/") {
					return "/" + joined
				}
				return joined
			}
		}
	case "linux":
		if appImage := os.Getenv("APPIMAGE"); appImage != "" {
			if info, err := os.Stat(appImage); err == nil && !info.IsDir() {
				return appImage
			}
		}
	}
	return exe
}

// currentPlatform returns the platform string using Wails 3 platform detection with runtime.GOOS fallback.
func currentPlatform() string {
	switch {
	case application.System.IsPlatform(application.PlatformWindows):
		return "windows"
	case application.System.IsPlatform(application.PlatformMacOS):
		return "darwin"
	case application.System.IsPlatform(application.PlatformLinux):
		return "linux"
	default:
		return runtime.GOOS
	}
}

// bundleTarget returns the .app bundle path on macOS, AppImage on Linux, or exe unchanged on other platforms.
func bundleTarget(exe string) string {
	return bundleTargetPath(exe, currentPlatform())
}

// detectArchiveKind classifies path by filename extension.
func detectArchiveKind(path string) archiveKind {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return archiveZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveTarGz
	default:
		return archiveNone
	}
}

// stageDownloadedAsset handles archive unpacking and staging target resolution.
func stageDownloadedAsset(downloadPath, stagingDir, goos string) (string, error) {
	kind := detectArchiveKind(downloadPath)
	if kind == archiveNone {
		_ = os.Chmod(downloadPath, 0o755)
		return downloadPath, nil
	}

	scratch := filepath.Join(stagingDir, "extracted")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return "", fmt.Errorf("create extract scratch dir: %w", err)
	}

	switch kind {
	case archiveZip:
		if err := extractZip(downloadPath, scratch); err != nil {
			return "", fmt.Errorf("zip extract: %w", err)
		}
	case archiveTarGz:
		if err := extractTarGz(downloadPath, scratch); err != nil {
			return "", fmt.Errorf("tar.gz extract: %w", err)
		}
	}

	// Clean up original downloaded archive to conserve disk space
	_ = os.Remove(downloadPath)

	staged, err := findStagedArtifact(scratch, goos)
	if err != nil {
		return "", fmt.Errorf("locate staged artifact: %w", err)
	}

	// Promote staged artifact to be a direct child of stagingDir so that:
	// filepath.Dir(finalPath) == stagingDir (which has prefix "wails-update-")
	// enabling Wails Helper's post-swap stagingDir cleanup.
	finalPath := filepath.Join(stagingDir, filepath.Base(staged))
	if finalPath != staged {
		_ = os.RemoveAll(finalPath)
		if err := os.Rename(staged, finalPath); err != nil {
			return "", fmt.Errorf("promote staged artifact: %w", err)
		}
	}

	// Clean up extract scratch directory
	_ = os.RemoveAll(scratch)

	return finalPath, nil
}

// findStagedArtifact searches the extracted directory for the platform-specific executable or bundle.
func findStagedArtifact(dir string, goos string) (string, error) {
	dir = filepath.Clean(dir)

	// 1. macOS: look for .app bundle
	if goos == "darwin" {
		var appPath string
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".app") {
				appPath = p
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if appPath != "" {
			return appPath, nil
		}
	}

	// 2. Windows: look for .exe file
	if goos == "windows" {
		var exePath string
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(info.Name()), ".exe") {
				exePath = p
				return io.EOF
			}
			return nil
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if exePath != "" {
			return exePath, nil
		}
	}

	// 3. Linux: look for .AppImage first, then any binary
	if goos == "linux" {
		var appImagePath string
		var binaryPath string
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			lower := strings.ToLower(info.Name())
			if strings.HasSuffix(lower, ".appimage") {
				appImagePath = p
				return io.EOF
			}
			if binaryPath == "" && (info.Mode()&0o111 != 0 || !strings.Contains(info.Name(), ".")) {
				binaryPath = p
			}
			return nil
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if appImagePath != "" {
			_ = os.Chmod(appImagePath, 0o755)
			return appImagePath, nil
		}
		if binaryPath != "" {
			_ = os.Chmod(binaryPath, 0o755)
			return binaryPath, nil
		}
	}

	// 4. Fallback: single file or single child directory in dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) == 1 {
		single := filepath.Join(dir, entries[0].Name())
		if entries[0].IsDir() {
			return findStagedArtifact(single, goos)
		}
		_ = os.Chmod(single, 0o755)
		return single, nil
	}

	return "", fmt.Errorf("unable to locate executable artifact in %s for %s", dir, goos)
}

// safeJoin verifies that joining root and name does not escape root (Zip-Slip defense).
func safeJoin(root, name string) (string, error) {
	// Normalize separators so Windows / POSIX slashes behave consistently
	clean := filepath.Clean(strings.ReplaceAll(name, `\`, "/"))
	if clean == "" || clean == "." {
		return root, nil
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "\\") || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("archive entry escapes root: %s", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes root: %s", name)
	}
	return target, nil
}

// validateSymlinkPath checks that linkName does not escape root.
func validateSymlinkPath(linkName, target, root string) error {
	normalized := strings.ReplaceAll(linkName, `\`, "/")
	if filepath.IsAbs(linkName) || strings.HasPrefix(normalized, "/") || strings.HasPrefix(linkName, "\\") {
		return fmt.Errorf("archive symlink has absolute target: %s", linkName)
	}
	resolved := filepath.Join(filepath.Dir(target), linkName)
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("archive symlink escapes root: %s -> %s", target, linkName)
	}
	return nil
}

// extractZip unpacks a zip archive into dst with path traversal and bomb protection.
func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("zip open: %w", err)
	}
	defer zr.Close()

	if len(zr.File) > maxArchiveEntries {
		return fmt.Errorf("zip has %d entries (cap %d)", len(zr.File), maxArchiveEntries)
	}

	rootClean := filepath.Clean(dst)
	var written int64

	type pendingLink struct {
		file   *zip.File
		target string
	}
	var symlinks []pendingLink

	// Pass 1: Directories and regular files
	for _, f := range zr.File {
		target, err := safeJoin(rootClean, f.Name)
		if err != nil {
			return err
		}

		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			symlinks = append(symlinks, pendingLink{file: f, target: target})
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("zip mkdir: %w", err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("zip mkdir: %w", err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("zip open entry %s: %w", f.Name, err)
		}

		perm := mode.Perm()
		if perm == 0 {
			perm = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return fmt.Errorf("zip create file %s: %w", target, err)
		}

		remaining := maxArchiveTotalSize - written + 1
		n, copyErr := io.CopyN(out, rc, remaining)
		closeErr := out.Close()
		rc.Close()

		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			return fmt.Errorf("zip copy %s: %w", f.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("zip close %s: %w", target, closeErr)
		}

		written += n
		if written > maxArchiveTotalSize {
			return fmt.Errorf("zip uncompressed size exceeds %d bytes", maxArchiveTotalSize)
		}
	}

	// Pass 2: Symlinks deferred until after regular files are written
	for _, sl := range symlinks {
		rc, err := sl.file.Open()
		if err != nil {
			return fmt.Errorf("zip open symlink: %w", err)
		}
		linkBytes, err := io.ReadAll(io.LimitReader(rc, 4096))
		rc.Close()
		if err != nil {
			return fmt.Errorf("zip read symlink: %w", err)
		}
		linkTarget := string(linkBytes)
		if err := validateSymlinkPath(linkTarget, sl.target, rootClean); err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(sl.target), 0o755); err != nil {
			return err
		}
		_ = os.Remove(sl.target)
		if err := os.Symlink(linkTarget, sl.target); err != nil {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("zip symlink create: %w", err)
			}
		}
	}

	return nil
}

// extractTarGz unpacks a .tar.gz archive into dst with path traversal and bomb protection.
func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("tar.gz open: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	rootClean := filepath.Clean(dst)

	type pendingLink struct {
		linkname string
		target   string
	}
	var symlinks []pendingLink
	var entries int
	var written int64

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("tar has more than %d entries", maxArchiveEntries)
		}

		target, err := safeJoin(rootClean, hdr.Name)
		if err != nil {
			return err
		}

		mode := os.FileMode(hdr.Mode)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("tar mkdir: %w", err)
			}
		case tar.TypeSymlink:
			symlinks = append(symlinks, pendingLink{linkname: hdr.Linkname, target: target})
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("tar mkdir: %w", err)
			}
			perm := mode.Perm()
			if perm == 0 {
				perm = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return fmt.Errorf("tar create: %w", err)
			}

			remaining := maxArchiveTotalSize - written + 1
			n, copyErr := io.CopyN(out, tr, remaining)
			closeErr := out.Close()
			if copyErr != nil && !errors.Is(copyErr, io.EOF) {
				return fmt.Errorf("tar copy: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("tar close: %w", closeErr)
			}
			written += n
			if written > maxArchiveTotalSize {
				return fmt.Errorf("tar uncompressed size exceeds %d bytes", maxArchiveTotalSize)
			}
		}
	}

	// Pass 2: Symlinks
	for _, sl := range symlinks {
		if err := validateSymlinkPath(sl.linkname, sl.target, rootClean); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(sl.target), 0o755); err != nil {
			return err
		}
		_ = os.Remove(sl.target)
		if err := os.Symlink(sl.linkname, sl.target); err != nil {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("tar symlink create: %w", err)
			}
		}
	}

	return nil
}

// emitStatus sends an update status event to the frontend
func (u *Updater) emitStatus(status string, payload any) {
	if u.eventHub != nil {
		u.eventHub.EmitUpdateStatus(status, payload)
	}
}

// emitProgress sends an update progress event to the frontend
func (u *Updater) emitProgress(downloaded, total, speed int64) {
	if u.eventHub != nil {
		u.eventHub.EmitUpdateProgress(downloaded, total, speed)
	}
}

// progressReader wraps an io.Reader to track download progress
type progressReader struct {
	reader       io.Reader
	total        int64
	read         int64
	updater      *Updater
	lastEmit     time.Time
	lastReadMark int64
	emitEvery    time.Duration
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.read += int64(n)

	if pr.total > 0 && time.Since(pr.lastEmit) >= pr.emitEvery {
		elapsed := time.Since(pr.lastEmit).Seconds()
		var speed int64
		if elapsed > 0 {
			speed = int64(float64(pr.read-pr.lastReadMark) / elapsed)
		}
		pr.updater.emitProgress(pr.read, pr.total, speed)
		pr.lastReadMark = pr.read
		pr.lastEmit = time.Now()
	}

	return n, err
}
