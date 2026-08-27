package process

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
)

func TestPrepareBundledAria2BinaryFailsWhenBundleMissing(t *testing.T) {
	homeDir := t.TempDir()
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      "linux",
		embeddedPath:  "bundled/linux/aria2c",
		extractedName: "aria2c",
		prepareHint:   "wails3 task linux:prepare:aria2",
		loadErr:       os.ErrNotExist,
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	validateBundledAria2Binary = func(path string, source bundledAria2Source) error {
		t.Fatalf("validation must not run when bundle is missing")
		return nil
	}

	_, err := prepareBundledAria2Binary()
	if err == nil {
		t.Fatal("expected missing bundle error")
	}

	message := err.Error()
	if !strings.Contains(message, "bundled aria2 staging missing") {
		t.Fatalf("expected missing bundle message, got %q", message)
	}
	if !strings.Contains(message, "linux/"+runtime.GOARCH) {
		t.Fatalf("expected target tuple in error, got %q", message)
	}
	if !strings.Contains(message, "bundled/linux/aria2c") {
		t.Fatalf("expected staged path in error, got %q", message)
	}
}

func TestPrepareBundledAria2BinarySkipsRewriteWhenContentMatches(t *testing.T) {
	homeDir := t.TempDir()
	bundle := []byte("fake aria2 payload")
	writeCount := 0

	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		bytes:         bundle,
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writeCount++
		return os.WriteFile(name, data, perm)
	}
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }
	validateBundledAria2Binary = func(path string, source bundledAria2Source) error { return nil }

	prepared, err := prepareBundledAria2Binary()
	if err != nil {
		t.Fatalf("first prepare failed: %v", err)
	}
	path := prepared.candidatePath
	finalPath := prepared.finalPath
	if _, err := activatePreparedBundledAria2Binary(prepared); err != nil {
		t.Fatalf("failed to activate first prepared binary: %v", err)
	}
	prepared.cleanup()
	if writeCount != 1 {
		t.Fatalf("expected one write after first prepare, got %d", writeCount)
	}
	if path == "" {
		t.Fatal("expected candidate path when installing new bundled runtime")
	}
	if killCalls != 0 {
		t.Fatalf("expected no process kill during preparation, got %d", killCalls)
	}

	infoBefore, err := os.Stat(finalPath)
	if err != nil {
		t.Fatalf("stat before second prepare failed: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	secondPrepared, err := prepareBundledAria2Binary()
	if err != nil {
		t.Fatalf("second prepare failed: %v", err)
	}
	secondPath := secondPrepared.finalPath
	if secondPath != finalPath {
		t.Fatalf("expected second prepare to reuse final path %q, got %q", finalPath, secondPath)
	}
	if writeCount != 1 {
		t.Fatalf("expected no extra write when content matches, got %d writes", writeCount)
	}

	infoAfter, err := os.Stat(finalPath)
	if err != nil {
		t.Fatalf("stat after second prepare failed: %v", err)
	}
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Fatalf("expected unchanged modtime, got %v -> %v", infoBefore.ModTime(), infoAfter.ModTime())
	}
	if killCalls != 0 {
		t.Fatalf("expected no process kill during repeated preparation, got %d", killCalls)
	}
}

func TestPrepareBundledAria2BinaryFailsValidationBeforeLaunch(t *testing.T) {
	homeDir := t.TempDir()
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      "linux",
		embeddedPath:  "bundled/linux/aria2c",
		extractedName: "aria2c",
		prepareHint:   "wails3 task linux:prepare:aria2",
		bytes:         []byte("not-a-real-binary"),
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	validateBundledAria2Binary = func(path string, source bundledAria2Source) error {
		return errors.New("exec format error")
	}

	prepared, err := prepareBundledAria2Binary()
	if err == nil {
		t.Fatal("expected validation failure")
	}

	message := err.Error()
	if !strings.Contains(message, "validation failed") {
		t.Fatalf("expected validation failure message, got %q", message)
	}
	if !strings.Contains(message, "exec format error") {
		t.Fatalf("expected underlying validation error, got %q", message)
	}
	if !strings.Contains(message, "linux/"+runtime.GOARCH) {
		t.Fatalf("expected target tuple in error, got %q", message)
	}
	if prepared.finalPath != filepath.Join(homeDir, ".goaria", "aria2c") {
		t.Fatalf("unexpected final path: %q", prepared.finalPath)
	}
	if _, statErr := os.Stat(prepared.finalPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected existing runtime to remain untouched on validation failure, stat err=%v", statErr)
	}
}

func TestPrepareBundledAria2BinaryDoesNotFallbackToPath(t *testing.T) {
	homeDir := t.TempDir()
	pathDir := t.TempDir()
	fakeBinaryPath := filepath.Join(pathDir, bundledBinaryNameForTest())
	if err := os.WriteFile(fakeBinaryPath, []byte("echo fake"), 0o755); err != nil {
		t.Fatalf("failed to write fake PATH binary: %v", err)
	}

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+originalPath)

	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      "linux",
		embeddedPath:  "bundled/linux/aria2c",
		extractedName: "aria2c",
		prepareHint:   "wails3 task linux:prepare:aria2",
		loadErr:       os.ErrNotExist,
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	validateBundledAria2Binary = func(path string, source bundledAria2Source) error {
		t.Fatalf("validation must not run when bundle is missing")
		return nil
	}

	_, err := prepareBundledAria2Binary()
	if err == nil {
		t.Fatal("expected missing bundle error")
	}
	if !strings.Contains(err.Error(), "bundled aria2 staging missing") {
		t.Fatalf("expected missing bundle error, got %q", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(homeDir, ".goaria", "aria2c")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no extracted linux binary when bundle is missing, stat err=%v", statErr)
	}
}

func TestStartAria2DoesNotKillExistingProcessesWhenPreparationFails(t *testing.T) {
	homeDir := t.TempDir()
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		loadErr:       os.ErrNotExist,
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }

	err := StartAria2(&config.AppConfig{
		RPCPort:                "16800",
		DownloadDir:            homeDir,
		MaxConcurrentDownloads: "1",
		MaxConnections:         "1",
		UserAgent:              "test-agent",
	})
	if err == nil {
		t.Fatal("expected StartAria2 to fail when bundled runtime is missing")
	}
	if killCalls != 0 {
		t.Fatalf("expected no process kill before preparation succeeds, got %d", killCalls)
	}
	if !strings.Contains(err.Error(), "staging missing") {
		t.Fatalf("expected staging failure message, got %q", err.Error())
	}
}

func TestPrepareBundledAria2BinaryPreservesLastKnownGoodRuntimeOnValidationFailure(t *testing.T) {
	homeDir := t.TempDir()
	finalPath := filepath.Join(homeDir, ".goaria", bundledBinaryNameForTest())
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}
	original := []byte("last-known-good")
	if err := os.WriteFile(finalPath, original, 0o755); err != nil {
		t.Fatalf("failed to seed existing runtime: %v", err)
	}

	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		bytes:         []byte("bad-update"),
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	validateBundledAria2Binary = func(path string, source bundledAria2Source) error {
		if path == finalPath {
			t.Fatalf("validation should run against candidate, not final path")
		}
		return errors.New("candidate invalid")
	}
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }

	_, err := prepareBundledAria2Binary()
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if killCalls != 0 {
		t.Fatalf("expected no process kill when preparation fails, got %d", killCalls)
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("expected validation failure message, got %q", err.Error())
	}
	candidates, globErr := filepath.Glob(filepath.Join(homeDir, ".goaria", bundledBinaryNameForTest()+".candidate-*"))
	if globErr != nil {
		t.Fatalf("failed to glob candidate files: %v", globErr)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no leftover candidate files, found %v", candidates)
	}
	content, readErr := os.ReadFile(finalPath)
	if readErr != nil {
		t.Fatalf("failed to read final path: %v", readErr)
	}
	if !bytes.Equal(content, original) {
		t.Fatalf("expected final runtime to remain unchanged, got %q", string(content))
	}
}

func TestPrepareBundledAria2BinaryValidationExecutesCandidatePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix executable permission probe is not portable to windows")
	}

	homeDir := t.TempDir()
	script := []byte("#!/bin/sh\nexit 0\n")
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		bytes:         script,
	})
	defer restore()

	userHomeDir = func() (string, error) { return homeDir, nil }
	validateBundledAria2Binary = defaultValidateBundledAria2Binary

	prepared, err := prepareBundledAria2Binary()
	if err != nil {
		t.Fatalf("expected validation to succeed with executable script, got %v", err)
	}
	if prepared.candidatePath == "" {
		t.Fatal("expected candidate path for updated runtime")
	}
	if info, statErr := os.Stat(prepared.candidatePath); statErr != nil {
		t.Fatalf("expected candidate file to exist: %v", statErr)
	} else if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected candidate file to be executable, mode=%v", info.Mode())
	}
	prepared.cleanup()
}

func stubBundledAria2Runtime(t *testing.T, source bundledAria2Source) func() {
	t.Helper()

	originalSource := currentBundledAria2
	originalUserHomeDir := userHomeDir
	originalValidate := validateBundledAria2Binary
	originalWriteFile := writeFile
	originalReadFile := readFile
	originalStatFile := statFile
	originalMkdirAll := mkdirAll
	originalCreateTempFile := createTempFile
	originalRemoveFile := removeFile
	originalKillAllAria2Processes := killAllAria2Processes

	currentBundledAria2 = source
	userHomeDir = os.UserHomeDir
	validateBundledAria2Binary = defaultValidateBundledAria2Binary
	writeFile = os.WriteFile
	readFile = os.ReadFile
	statFile = os.Stat
	mkdirAll = os.MkdirAll
	createTempFile = os.CreateTemp
	removeFile = os.Remove
	killAllAria2Processes = KillAllOldProcesses

	return func() {
		currentBundledAria2 = originalSource
		userHomeDir = originalUserHomeDir
		validateBundledAria2Binary = originalValidate
		writeFile = originalWriteFile
		readFile = originalReadFile
		statFile = originalStatFile
		mkdirAll = originalMkdirAll
		createTempFile = originalCreateTempFile
		removeFile = originalRemoveFile
		killAllAria2Processes = originalKillAllAria2Processes
	}
}

func bundledBinaryNameForTest() string {
	if runtime.GOOS == "windows" {
		return "aria2c.exe"
	}

	return "aria2c"
}

func TestEffectiveAria2MaxConnections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"1", 1},
		{"8", 8},
		{"16", 16},
		{"17", 16},
		{"64", 16},
		{"128", 16},
		{"256", 16},
		{"", 16},
		{"0", 16},
		{"-4", 16},
		{"abc", 16},
		{" 8 ", 8},
	}
	for _, tc := range cases {
		if got := EffectiveAria2MaxConnections(tc.in); got != tc.want {
			t.Errorf("EffectiveAria2MaxConnections(%q) = %d, want %d", tc.in, got, tc.want)
		}
		if got := EffectiveAria2MaxConnections(tc.in); got > 16 {
			t.Errorf("Aria value %d exceeds 16", got)
		}
	}
}

func TestValidateDownloadDir_EmptyRejected(t *testing.T) {
	if err := ValidateDownloadDir(""); err == nil {
		t.Fatal("empty dir must fail")
	}
	if err := ValidateDownloadDir("   "); err == nil {
		t.Fatal("whitespace dir must fail")
	}
}

func TestValidateDownloadDir_CreatesMissingAndProbes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-dl")
	if err := ValidateDownloadDir(dir); err != nil {
		t.Fatalf("missing dir should be created: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "dirprobe") {
			t.Fatalf("probe leftover: %s", e.Name())
		}
	}
}

func TestValidateDownloadDir_RejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDownloadDir(path); err == nil {
		t.Fatal("file path must fail")
	}
}

func TestValidateDownloadDir_ProbeCreateFailure(t *testing.T) {
	restore := stubBundledAria2Runtime(t, currentBundledAria2)
	defer restore()
	dir := t.TempDir()
	createTempFile = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("probe create denied")
	}
	if err := ValidateDownloadDir(dir); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("create failure: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "dirprobe") {
			t.Fatalf("leftover probe %s", e.Name())
		}
	}
}

func TestStartAria2NilConfigDoesNotKill(t *testing.T) {
	restore := stubBundledAria2Runtime(t, currentBundledAria2)
	defer restore()
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }
	if err := StartAria2(nil); err == nil {
		t.Fatal("expected nil config error")
	}
	if killCalls != 0 {
		t.Fatalf("killed on nil config: %d", killCalls)
	}
}

func TestRestartAria2NilConfigDoesNotKill(t *testing.T) {
	restore := stubBundledAria2Runtime(t, currentBundledAria2)
	defer restore()
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }
	if err := RestartAria2(nil); err == nil {
		t.Fatal("expected nil config error")
	}
	if killCalls != 0 {
		t.Fatalf("killed on nil config: %d", killCalls)
	}
}

func TestRestartAria2InvalidDirectoryDoesNotKill(t *testing.T) {
	homeDir := t.TempDir()
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		bytes:         []byte("fake aria2 payload"),
	})
	defer restore()
	userHomeDir = func() (string, error) { return homeDir, nil }
	validateBundledAria2Binary = func(path string, source bundledAria2Source) error { return nil }
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }
	filePath := filepath.Join(homeDir, "not-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RestartAria2(&config.AppConfig{
		RPCPort:                "16800",
		DownloadDir:            filePath,
		MaxConcurrentDownloads: "1",
		MaxConnections:         "8",
		UserAgent:              "test-agent",
	})
	if err == nil {
		t.Fatal("expected invalid directory error")
	}
	if killCalls != 0 {
		t.Fatalf("killed old process before dir preflight, got %d", killCalls)
	}
}

func TestStartAria2MissingBundleDoesNotKill(t *testing.T) {
	homeDir := t.TempDir()
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		loadErr:       os.ErrNotExist,
	})
	defer restore()
	userHomeDir = func() (string, error) { return homeDir, nil }
	killCalls := 0
	killAllAria2Processes = func() { killCalls++ }
	err := StartAria2(&config.AppConfig{
		RPCPort:                "16800",
		DownloadDir:            homeDir,
		MaxConcurrentDownloads: "1",
		MaxConnections:         "1",
		UserAgent:              "test-agent",
	})
	if err == nil {
		t.Fatal("expected missing bundle")
	}
	if killCalls != 0 {
		t.Fatalf("killed on missing bundle: %d", killCalls)
	}
}

func TestAria2LifecycleConcurrentNoDeadlock(t *testing.T) {
	homeDir := t.TempDir()
	restore := stubBundledAria2Runtime(t, bundledAria2Source{
		targetOS:      runtime.GOOS,
		embeddedPath:  "bundled/test/aria2c",
		extractedName: bundledBinaryNameForTest(),
		prepareHint:   "test prepare",
		loadErr:       os.ErrNotExist,
	})
	defer restore()
	userHomeDir = func() (string, error) { return homeDir, nil }
	killAllAria2Processes = func() {}
	cfg := &config.AppConfig{DownloadDir: homeDir, RPCPort: "16800", MaxConnections: "8", MaxConcurrentDownloads: "1", UserAgent: "t"}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = StartAria2(cfg)
		}()
		go func() {
			defer wg.Done()
			_ = RestartAria2(cfg)
		}()
		go func() {
			defer wg.Done()
			StopAria2()
		}()
	}
	wg.Wait()
}
