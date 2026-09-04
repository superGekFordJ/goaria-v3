package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestDetectArchiveKind(t *testing.T) {
	cases := []struct {
		path string
		want archiveKind
	}{
		{"app.zip", archiveZip},
		{"APP.ZIP", archiveZip},
		{"bundle.tar.gz", archiveTarGz},
		{"bundle.tgz", archiveTarGz},
		{"goaria.exe", archiveNone},
		{"GoAria.AppImage", archiveNone},
		{"binary", archiveNone},
	}

	for _, tc := range cases {
		got := detectArchiveKind(tc.path)
		if got != tc.want {
			t.Errorf("detectArchiveKind(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestSafeJoinZipSlip(t *testing.T) {
	tmpDir := t.TempDir()

	// Normal inside paths
	normal, err := safeJoin(tmpDir, "sub/dir/file.txt")
	if err != nil {
		t.Fatalf("unexpected error for normal path: %v", err)
	}
	if !strings.HasPrefix(normal, tmpDir) {
		t.Errorf("expected %q to be inside %q", normal, tmpDir)
	}

	// Zip-Slip escape attempts
	malicious := []string{
		"../escaped.txt",
		"../../escaped.txt",
		"/etc/passwd",
		"sub/../../escaped.txt",
		"..\\escaped.txt",
		"c:/windows/system32",
	}

	for _, path := range malicious {
		_, err := safeJoin(tmpDir, path)
		if err == nil {
			t.Errorf("expected error for Zip-Slip path %q, got nil", path)
		}
	}
}

func TestValidateSymlinkPath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "app", "root")
	target := filepath.Join(root, "sub", "link")

	// Valid link inside root
	if err := validateSymlinkPath("../sibling", target, root); err != nil {
		t.Errorf("expected valid relative link, got: %v", err)
	}

	// Absolute link rejected
	if err := validateSymlinkPath("/etc/shadow", target, root); err == nil {
		t.Error("expected absolute symlink to be rejected")
	}

	// Escaping relative link rejected
	if err := validateSymlinkPath("../../outside", target, root); err == nil {
		t.Error("expected escaping symlink to be rejected")
	}
}

func TestExtractZipAndFindArtifact(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")

	// Create a zip with a macOS .app and a Windows .exe inside
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// Add GoAria.app/Contents/MacOS/GoAria
	fApp, err := zw.Create("GoAria.app/Contents/MacOS/GoAria")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fApp.Write([]byte("fake-macos-binary"))

	// Add goaria.exe
	fExe, err := zw.Create("goaria.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fExe.Write([]byte("fake-windows-binary"))

	// Add linux AppImage
	fLinux, err := zw.Create("GoAria.AppImage")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fLinux.Write([]byte("fake-linux-appimage"))

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := extractZip(zipPath, extractDir); err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}

	// Test findStagedArtifact for macOS
	stagedDarwin, err := findStagedArtifact(extractDir, "darwin")
	if err != nil {
		t.Fatalf("findStagedArtifact darwin failed: %v", err)
	}
	if !strings.HasSuffix(stagedDarwin, "GoAria.app") {
		t.Errorf("expected .app bundle, got %q", stagedDarwin)
	}

	// Test findStagedArtifact for Windows
	stagedWin, err := findStagedArtifact(extractDir, "windows")
	if err != nil {
		t.Fatalf("findStagedArtifact windows failed: %v", err)
	}
	if !strings.HasSuffix(stagedWin, "goaria.exe") {
		t.Errorf("expected .exe file, got %q", stagedWin)
	}

	// Test findStagedArtifact for Linux
	stagedLinux, err := findStagedArtifact(extractDir, "linux")
	if err != nil {
		t.Fatalf("findStagedArtifact linux failed: %v", err)
	}
	if !strings.HasSuffix(stagedLinux, "GoAria.AppImage") {
		t.Errorf("expected .AppImage file, got %q", stagedLinux)
	}
}

func TestExtractTarGzAndFindArtifact(t *testing.T) {
	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "test.tar.gz")

	buf := new(bytes.Buffer)
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)

	content := []byte("#!/bin/sh\necho hi")
	hdr := &tar.Header{
		Name: "goaria",
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(tarGzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := extractTarGz(tarGzPath, extractDir); err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}

	staged, err := findStagedArtifact(extractDir, "linux")
	if err != nil {
		t.Fatalf("findStagedArtifact linux failed: %v", err)
	}
	if !strings.HasSuffix(staged, "goaria") {
		t.Errorf("expected 'goaria' binary, got %q", staged)
	}
}

func TestBundleTarget(t *testing.T) {
	// For Windows/Linux paths without APPIMAGE, bundleTarget should return input as-is
	winPath := `C:\Program Files\GoAria\goaria.exe`
	if got := bundleTargetPath(winPath, "windows"); got != winPath {
		t.Errorf("bundleTargetPath(%q, windows) = %q, want %q", winPath, got, winPath)
	}

	linuxPath := "/usr/local/bin/goaria"
	if got := bundleTargetPath(linuxPath, "linux"); got != linuxPath {
		t.Errorf("bundleTargetPath(%q, linux) = %q, want %q", linuxPath, got, linuxPath)
	}

	// For macOS paths inside a .app bundle, bundleTarget should return the .app root
	darwinPath := "/Applications/GoAria.app/Contents/MacOS/goaria"
	wantDarwin := "/Applications/GoAria.app"
	if got := bundleTargetPath(darwinPath, "darwin"); got != wantDarwin {
		t.Errorf("bundleTargetPath(%q, darwin) = %q, want %q", darwinPath, got, wantDarwin)
	}

	// For Linux with APPIMAGE environment variable pointing to a valid file
	tmpFile := filepath.Join(t.TempDir(), "GoAria.AppImage")
	if err := os.WriteFile(tmpFile, []byte("fake-appimage"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPIMAGE", tmpFile)
	if got := bundleTargetPath("/tmp/.mount_goaria/usr/bin/goaria", "linux"); got != tmpFile {
		t.Errorf("bundleTargetPath with APPIMAGE = %q, want %q", got, tmpFile)
	}
}

func TestStageDownloadedAssetPromotion(t *testing.T) {
	stagingDir := t.TempDir()
	zipPath := filepath.Join(stagingDir, "update.zip")

	// Create a zip with nested structure: goaria-v1.4.0/goaria.exe
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	f, err := zw.Create("goaria-v1.4.0/goaria.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("mock-exe"))
	_ = zw.Close()

	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	staged, err := stageDownloadedAsset(zipPath, stagingDir, "windows")
	if err != nil {
		t.Fatalf("stageDownloadedAsset failed: %v", err)
	}

	// Crucial check: staged artifact must be a direct child of stagingDir
	// so filepath.Dir(staged) == stagingDir, allowing Wails Helper's post-swap cleanup.
	if filepath.Dir(staged) != stagingDir {
		t.Errorf("expected staged artifact to be promoted directly into stagingDir %q, but got %q", stagingDir, filepath.Dir(staged))
	}
	if filepath.Base(staged) != "goaria.exe" {
		t.Errorf("expected artifact base name 'goaria.exe', got %q", filepath.Base(staged))
	}

	// The scratch extracted directory must be removed
	scratchDir := filepath.Join(stagingDir, "extracted")
	if _, err := os.Stat(scratchDir); err == nil {
		t.Errorf("expected scratch directory %q to be deleted", scratchDir)
	}
}

func TestUpdaterApplyAndRestart(t *testing.T) {
	// 1. Setup mock asset server serving a zip
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	f, err := zw.Create("goaria.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("mock-exe-content"))
	_ = zw.Close()
	zipBytes := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", strconv.Itoa(len(zipBytes)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBytes)
	}))
	defer server.Close()

	updater := NewUpdater(nil)

	// Apply invalid URL
	if err := updater.Apply(&ReleaseInfo{}); err == nil {
		t.Error("expected error when asset URL is empty")
	}

	// Apply valid URL
	info := &ReleaseInfo{
		AssetURL:  server.URL + "/goaria-v2.0.0-windows-amd64.zip",
		AssetSize: int64(len(zipBytes)),
	}

	if err := updater.Apply(info); err != nil {
		t.Fatalf("updater.Apply failed: %v", err)
	}

	staged := updater.StagedPath()
	if staged == "" {
		t.Fatal("expected stagedPath to be non-empty")
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged artifact does not exist at %s: %v", staged, err)
	}

	// 2. Test Restart
	var capturedEnv []string
	var quitCalled bool
	var mu sync.Mutex

	origStartCmd := startHelperCmd
	origQuit := quitApplication
	defer func() {
		startHelperCmd = origStartCmd
		quitApplication = origQuit
	}()

	startHelperCmd = func(cmd *exec.Cmd) error {
		mu.Lock()
		defer mu.Unlock()
		capturedEnv = cmd.Env
		return nil
	}
	quitApplication = func() {
		mu.Lock()
		defer mu.Unlock()
		quitCalled = true
	}

	if err := updater.Restart(); err != nil {
		t.Fatalf("updater.Restart failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !quitCalled {
		t.Error("expected quitApplication to be called")
	}

	hasHelperEnv := false
	hasTargetEnv := false
	hasNewEnv := false
	hasPIDEnv := false

	for _, e := range capturedEnv {
		if strings.HasPrefix(e, "WAILS_UPDATER_HELPER=1") {
			hasHelperEnv = true
		}
		if strings.HasPrefix(e, "WAILS_UPDATER_HELPER_TARGET=") {
			hasTargetEnv = true
		}
		if strings.HasPrefix(e, "WAILS_UPDATER_HELPER_NEW=") {
			hasNewEnv = true
		}
		if strings.HasPrefix(e, "WAILS_UPDATER_HELPER_PID=") {
			hasPIDEnv = true
		}
	}

	if !hasHelperEnv || !hasTargetEnv || !hasNewEnv || !hasPIDEnv {
		t.Errorf("missing expected WAILS_UPDATER_HELPER environment variables, got: %v", capturedEnv)
	}

	// Cleanup staging dir
	if stagingDir := updater.StagingDir(); stagingDir != "" {
		_ = os.RemoveAll(stagingDir)
	}
}
