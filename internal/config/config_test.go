package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func isolateConfigHome(t *testing.T) string {
	t.Helper()
	orig := Get()
	t.Cleanup(func() {
		SetTestConfig(orig)
		configPathOverride = ""
		readConfigFile = os.ReadFile
		createConfigTemp = os.CreateTemp
		renameConfigFile = os.Rename
	})
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	configPathOverride = ""
	readConfigFile = os.ReadFile
	createConfigTemp = os.CreateTemp
	renameConfigFile = os.Rename
	return tmp
}

func TestDefaultConfig(t *testing.T) {
	isolateConfigHome(t)
	got := DefaultConfig()
	if got.RPCPort != DefaultRPCPort {
		t.Fatalf("RPCPort = %q, want %q", got.RPCPort, DefaultRPCPort)
	}
	if got.MaxConnections != DefaultMaxConnections {
		t.Fatalf("MaxConnections = %q, want %q", got.MaxConnections, DefaultMaxConnections)
	}
	if got.MaxConcurrentDownloads != DefaultMaxConcurrentDownloads {
		t.Fatalf("MaxConcurrentDownloads = %q, want %q", got.MaxConcurrentDownloads, DefaultMaxConcurrentDownloads)
	}
	if got.UserAgent != DefaultUserAgent {
		t.Fatalf("UserAgent changed from shipping default")
	}
	if got.ShowHistory != true || got.SmartThreadMode != true || got.CloseToTray != false || got.ExtensionEnabled != true {
		t.Fatalf("boolean defaults mismatch: %+v", got)
	}
	if got.WindowTransparency != DefaultWindowTransparency {
		t.Fatalf("WindowTransparency = %q", got.WindowTransparency)
	}
	if got.MinThreadLife != DefaultMinThreadLife {
		t.Fatalf("MinThreadLife = %d", got.MinThreadLife)
	}
	if got.ConvergenceInterval != 0 {
		t.Fatalf("ConvergenceInterval = %d, want 0", got.ConvergenceInterval)
	}
	if got.ExtensionWSPort != DefaultExtensionWSPort {
		t.Fatalf("ExtensionWSPort = %d", got.ExtensionWSPort)
	}
	wantDir := filepath.Join(t.TempDir(), "") // ensure DefaultConfig used HOME
	_ = wantDir
	if got.DownloadDir != filepath.Join(os.Getenv("HOME"), "Downloads") &&
		got.DownloadDir != filepath.Join(os.Getenv("USERPROFILE"), "Downloads") {
		t.Fatalf("DownloadDir = %q, want user Downloads", got.DownloadDir)
	}
}

func TestValidateAndSanitize_MaxConnections(t *testing.T) {
	isolateConfigHome(t)
	cases := []struct {
		in, want string
	}{
		{"1", "1"},
		{"016", "16"},
		{" 64 ", "64"},
		{"128", "128"},
		{"256", "256"},
		{"", "16"},
		{"0", "16"},
		{"-1", "16"},
		{"257", "16"},
		{"999", "16"},
		{"abc", "16"},
		{"16.0", "16"},
		{"0x10", "16"},
		{"16e1", "16"},
		{"16x", "16"},
	}
	for _, tc := range cases {
		got := ValidateAndSanitize(AppConfig{MaxConnections: tc.in}).MaxConnections
		if got != tc.want {
			t.Errorf("MaxConnections %q → %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateAndSanitize_MaxConcurrentDownloads(t *testing.T) {
	isolateConfigHome(t)
	cases := []struct {
		in, want string
	}{
		{"1", "1"},
		{"5", "5"},
		{"32", "32"},
		{"0", "5"},
		{"-3", "5"},
		{"33", "5"},
		{"nope", "5"},
		{" 08 ", "8"},
	}
	for _, tc := range cases {
		got := ValidateAndSanitize(AppConfig{MaxConcurrentDownloads: tc.in}).MaxConcurrentDownloads
		if got != tc.want {
			t.Errorf("MaxConcurrentDownloads %q → %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateAndSanitize_RPCPort(t *testing.T) {
	isolateConfigHome(t)
	cases := []struct {
		in, want string
	}{
		{"1024", "1024"},
		{"16800", "16800"},
		{"65535", "65535"},
		{"016800", "16800"},
		{"80", "16800"},
		{"65536", "16800"},
		{"port", "16800"},
		{"", "16800"},
	}
	for _, tc := range cases {
		got := ValidateAndSanitize(AppConfig{RPCPort: tc.in}).RPCPort
		if got != tc.want {
			t.Errorf("RPCPort %q → %q, want %q", tc.in, got, tc.want)
		}
		if strings.HasPrefix(got, "0") && got != "0" {
			t.Errorf("RPCPort output %q has leading zeros", got)
		}
	}
}

func TestValidateAndSanitize_ExtensionWSPort(t *testing.T) {
	isolateConfigHome(t)
	if got := ValidateAndSanitize(AppConfig{ExtensionWSPort: 0}).ExtensionWSPort; got != 0 {
		t.Fatalf("0 → %d, want 0", got)
	}
	for _, p := range []int{16801, 16802, 16803} {
		if got := ValidateAndSanitize(AppConfig{ExtensionWSPort: p, RPCPort: "16800"}).ExtensionWSPort; got != p {
			t.Fatalf("%d → %d, want retained", p, got)
		}
	}
	if got := ValidateAndSanitize(AppConfig{ExtensionWSPort: 20000}).ExtensionWSPort; got != 16801 {
		t.Fatalf("20000 → %d, want 16801", got)
	}
	if got := ValidateAndSanitize(AppConfig{ExtensionWSPort: 16801, RPCPort: "16801"}).ExtensionWSPort; got != 0 {
		t.Fatalf("equal RPC → %d, want 0", got)
	}
}

func TestValidateAndSanitize_DownloadDir(t *testing.T) {
	isolateConfigHome(t)
	defaults := DefaultConfig()
	if got := ValidateAndSanitize(AppConfig{DownloadDir: "   "}).DownloadDir; got != defaults.DownloadDir {
		t.Fatalf("whitespace dir → %q, want default %q", got, defaults.DownloadDir)
	}
	if got := ValidateAndSanitize(AppConfig{DownloadDir: ""}).DownloadDir; got != defaults.DownloadDir {
		t.Fatalf("empty dir → %q, want default", got)
	}
	rel := ValidateAndSanitize(AppConfig{DownloadDir: "downloads/nested"}).DownloadDir
	if rel != filepath.Clean("downloads/nested") {
		t.Fatalf("relative Clean = %q, want %q", rel, filepath.Clean("downloads/nested"))
	}
	dotted := ValidateAndSanitize(AppConfig{DownloadDir: "foo/../bar"}).DownloadDir
	if dotted != filepath.Clean("foo/../bar") {
		t.Fatalf("dotted Clean = %q, want %q", dotted, filepath.Clean("foo/../bar"))
	}
	abs := filepath.Join(t.TempDir(), "dl")
	if got := ValidateAndSanitize(AppConfig{DownloadDir: abs}).DownloadDir; got != filepath.Clean(abs) {
		t.Fatalf("abs Clean = %q, want %q", got, filepath.Clean(abs))
	}
	unc := `\\server\share\folder`
	if runtime.GOOS == "windows" {
		if got := ValidateAndSanitize(AppConfig{DownloadDir: unc}).DownloadDir; got != filepath.Clean(unc) {
			t.Fatalf("UNC Clean = %q, want %q", got, filepath.Clean(unc))
		}
	}
}

func TestValidateAndSanitize_RemainingFields(t *testing.T) {
	isolateConfigHome(t)
	if got := ValidateAndSanitize(AppConfig{WindowTransparency: "mica"}).WindowTransparency; got != "mica" {
		t.Fatalf("mica retained, got %q", got)
	}
	if got := ValidateAndSanitize(AppConfig{WindowTransparency: "glass"}).WindowTransparency; got != "none" {
		t.Fatalf("invalid transparency → %q", got)
	}
	for _, v := range []int{0, 1, 60} {
		if got := ValidateAndSanitize(AppConfig{ConvergenceInterval: v}).ConvergenceInterval; got != v {
			t.Fatalf("ConvergenceInterval %d → %d", v, got)
		}
	}
	if got := ValidateAndSanitize(AppConfig{ConvergenceInterval: 61}).ConvergenceInterval; got != 0 {
		t.Fatalf("61 → %d, want 0", got)
	}
	if got := ValidateAndSanitize(AppConfig{ConvergenceInterval: -1}).ConvergenceInterval; got != 0 {
		t.Fatalf("-1 → %d, want 0", got)
	}
	if got := ValidateAndSanitize(AppConfig{MinThreadLife: 12}).MinThreadLife; got != 12 {
		t.Fatalf("MinThreadLife 12 → %d", got)
	}
	if got := ValidateAndSanitize(AppConfig{MinThreadLife: 0}).MinThreadLife; got != 5 {
		t.Fatalf("MinThreadLife 0 → %d", got)
	}
	ua := "CustomAgent/1.0"
	if got := ValidateAndSanitize(AppConfig{UserAgent: ua}).UserAgent; got != ua {
		t.Fatalf("UA not preserved")
	}
	if got := ValidateAndSanitize(AppConfig{UserAgent: "  \t"}).UserAgent; got != DefaultUserAgent {
		t.Fatalf("blank UA → %q", got)
	}
	in := AppConfig{
		ShowHistory: false, SmartThreadMode: false, CloseToTray: true, ExtensionEnabled: false,
	}
	out := ValidateAndSanitize(in)
	if out.ShowHistory || out.SmartThreadMode || !out.CloseToTray || out.ExtensionEnabled {
		t.Fatalf("booleans not preserved: %+v", out)
	}
}

func TestValidateAndSanitize_DoesNotMutateInput(t *testing.T) {
	isolateConfigHome(t)
	in := AppConfig{
		RPCPort: "016800", MaxConnections: " 64 ", DownloadDir: "  x  ",
		UserAgent: "keep", ExtensionSecret: "secret-value",
	}
	before := in
	_ = ValidateAndSanitize(in)
	if in != before {
		t.Fatalf("input mutated:\n before=%+v\n after=%+v", before, in)
	}
}

func TestLoad_GeneratesExtensionSecretIfEmpty(t *testing.T) {
	isolateConfigHome(t)
	tmp := os.Getenv("HOME")
	if tmp == "" {
		tmp = os.Getenv("USERPROFILE")
	}
	cfgDir := filepath.Join(tmp, ".goaria")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"rpc_port":"16800"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	Load()
	if Get() == nil {
		t.Fatal("Get() should not be nil after Load")
	}
	if Get().ExtensionSecret == "" {
		t.Fatal("ExtensionSecret should be generated on first boot")
	}
	if len(Get().ExtensionSecret) != 64 {
		t.Fatalf("ExtensionSecret should be 64 hex chars, got %d", len(Get().ExtensionSecret))
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), Get().ExtensionSecret) {
		t.Fatal("ExtensionSecret should be persisted in config.json")
	}
}

func TestLoad_PreservesExistingExtensionSecret(t *testing.T) {
	isolateConfigHome(t)
	tmp := os.Getenv("HOME")
	if tmp == "" {
		tmp = os.Getenv("USERPROFILE")
	}
	existingSecret := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	cfgDir := filepath.Join(tmp, ".goaria")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"extension_secret":"`+existingSecret+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	Load()
	if Get().ExtensionSecret != existingSecret {
		t.Fatalf("ExtensionSecret should be preserved, got %q", Get().ExtensionSecret)
	}
}

func TestLoad_AbsentFileCreatesCanonical(t *testing.T) {
	isolateConfigHome(t)
	Load()
	got := Get()
	if got == nil {
		t.Fatal("nil after Load")
	}
	if len(got.ExtensionSecret) != 64 {
		t.Fatalf("secret len %d", len(got.ExtensionSecret))
	}
	data, err := os.ReadFile(GetConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var disk AppConfig
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk != *got {
		t.Fatalf("disk != memory")
	}
	if disk.MaxConnections != DefaultMaxConnections {
		t.Fatalf("canonical MaxConnections = %q", disk.MaxConnections)
	}
}

func TestLoad_PartialJSONPreservesExplicitFalse(t *testing.T) {
	isolateConfigHome(t)
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"show_history":false,"smart_thread_mode":false,"extension_enabled":false,"close_to_tray":true,"extension_secret":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	Load()
	got := Get()
	if got.ShowHistory || got.SmartThreadMode || got.ExtensionEnabled || !got.CloseToTray {
		t.Fatalf("explicit bools lost: %+v", got)
	}
	if got.RPCPort != DefaultRPCPort {
		t.Fatalf("missing field overlay failed, RPCPort=%q", got.RPCPort)
	}
}

func TestLoad_ValidHighConnectionsSurvive(t *testing.T) {
	isolateConfigHome(t)
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"64", "128", "256"} {
		body := `{"max_connections":"` + n + `","extension_secret":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		Load()
		if Get().MaxConnections != n {
			t.Fatalf("%s did not survive, got %q", n, Get().MaxConnections)
		}
	}
}

func TestLoad_OutOfPolicySelfHeals(t *testing.T) {
	isolateConfigHome(t)
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"max_connections":"257","max_concurrent_downloads":"99","extension_secret":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	Load()
	if Get().MaxConnections != "16" || Get().MaxConcurrentDownloads != "5" {
		t.Fatalf("self-heal failed: %+v", Get())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"max_connections": "16"`) {
		t.Fatalf("disk not healed: %s", data)
	}
}

func TestLoad_MalformedDoesNotWipe(t *testing.T) {
	isolateConfigHome(t)
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	originals := [][]byte{
		[]byte(`{not json`),
		[]byte(`{"rpc_port":"16800"} trailing`),
		[]byte(`{"max_connections":16}`),
	}
	for _, original := range originals {
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
		Load()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, original) {
			t.Fatalf("bytes changed:\n got %q\n want %q", data, original)
		}
		if Get() == nil || Get().MaxConnections != DefaultMaxConnections {
			t.Fatalf("memory defaults not published")
		}
		if Get().ExtensionSecret == "" {
			t.Fatal("runtime secret missing")
		}
		if strings.Contains(string(data), Get().ExtensionSecret) {
			t.Fatal("runtime secret must not be written to corrupt file")
		}
	}
}

func TestLoad_ReadErrorDoesNotReplace(t *testing.T) {
	isolateConfigHome(t)
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"rpc_port":"20000","max_connections":"64"}`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	readConfigFile = func(name string) ([]byte, error) {
		return nil, errors.New("simulated read failure")
	}
	Load()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, original) {
		t.Fatalf("file replaced on read error: %s", data)
	}
	got := Get()
	if got == nil {
		t.Fatal("expected published defaults")
	}
	if got.RPCPort != DefaultRPCPort || got.MaxConnections != DefaultMaxConnections {
		t.Fatalf("published %+v, want defaults", got)
	}
}

func TestLoad_CorruptMissingSecretRemainsIdentical(t *testing.T) {
	isolateConfigHome(t)
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"rpc_port":`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	Load()
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatalf("corrupt file rewritten: %s", data)
	}
}

func TestLoad_PersistFailureDoesNotReshapePublishedPointer(t *testing.T) {
	isolateConfigHome(t)
	createConfigTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("disk full")
	}
	Load()
	first := Get()
	if first == nil || first.ExtensionSecret == "" {
		t.Fatal("expected published memory snapshot")
	}
	secret := first.ExtensionSecret
	port := first.RPCPort
	if Get() != first {
		t.Fatal("Load stored more than once")
	}
	if first.ExtensionSecret != secret || first.RPCPort != port {
		t.Fatal("published snapshot mutated after persist failure")
	}
}

func TestLoad_CanonicalFileNotRewritten(t *testing.T) {
	isolateConfigHome(t)
	Load()
	path := GetConfigPath()
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	Load()
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) || info1.Size() != info2.Size() {
		t.Fatalf("canonical file rewritten: %v → %v", info1.ModTime(), info2.ModTime())
	}
}

func TestLoad_DoesNotWipeConfigOnFailedRead(t *testing.T) {
	isolateConfigHome(t)
	tmp := os.Getenv("HOME")
	if tmp == "" {
		tmp = os.Getenv("USERPROFILE")
	}
	cfgDir := filepath.Join(tmp, ".goaria")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	originalContent := `{"rpc_port":"20000","download_dir":"` + filepath.ToSlash(filepath.Join(tmp, "data", "dl")) + `","extension_secret":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}`
	if err := os.WriteFile(cfgPath, []byte(originalContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(cfgPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })
	} else {
		readConfigFile = func(name string) ([]byte, error) {
			return nil, errors.New("access denied")
		}
	}

	Load()

	_ = os.Chmod(cfgPath, 0o644)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config should still be readable after Load: %v", err)
	}
	if !strings.Contains(string(data), `"rpc_port":"20000"`) {
		t.Fatalf("config was wiped to defaults, got: %s", string(data))
	}
}

func TestUpdate_PersistsToDisk(t *testing.T) {
	isolateConfigHome(t)
	SetTestConfig(&AppConfig{MinThreadLife: 5})
	Update(func(c *AppConfig) { c.MinThreadLife = 42 })

	if got := Get().MinThreadLife; got != 42 {
		t.Fatalf("Get().MinThreadLife = %d, want 42", got)
	}

	cfgPath := GetConfigPath()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config file not persisted: %v", err)
	}
	if !strings.Contains(string(data), `"min_thread_life": 42`) {
		t.Fatalf("persisted config missing min_thread_life=42, got: %s", string(data))
	}
}

func TestUpdate_BeforeLoadReturnsWithoutPublishing(t *testing.T) {
	isolateConfigHome(t)
	SetTestConfig(nil)
	Update(func(c *AppConfig) { c.MinThreadLife = 1 })
	if Get() != nil {
		t.Fatal("Update before Load must not publish")
	}
}

func TestSetTestConfig_DoesNotPersist(t *testing.T) {
	isolateConfigHome(t)
	cfgPath := GetConfigPath()
	_ = os.Remove(cfgPath)

	SetTestConfig(&AppConfig{MinThreadLife: 99})

	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("SetTestConfig should not persist to disk, but config file was created")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking config file: %v", err)
	}
}

func TestUpdate_ConcurrentWritesAreSerialized(t *testing.T) {
	isolateConfigHome(t)
	const initial = 10
	const n = 50
	SetTestConfig(&AppConfig{MinThreadLife: initial, MaxConnections: "16", MaxConcurrentDownloads: "5", RPCPort: "16800"})

	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			Update(func(c *AppConfig) { c.MinThreadLife++ })
		}()
	}
	wg.Wait()

	if got := Get().MinThreadLife; got != initial+n {
		t.Fatalf("MinThreadLife = %d, want %d (lost updates detected)", got, initial+n)
	}
	data, err := os.ReadFile(GetConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var disk AppConfig
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.MinThreadLife != Get().MinThreadLife {
		t.Fatalf("disk %d != Get %d", disk.MinThreadLife, Get().MinThreadLife)
	}
}

func TestUpdateChecked_MutationReceivesCopy(t *testing.T) {
	isolateConfigHome(t)
	start := DefaultConfig()
	start.ExtensionSecret = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	SetTestConfig(&start)
	oldPtr := Get()
	oldCopy := *oldPtr
	res, err := UpdateChecked(func(c *AppConfig) { c.MinThreadLife = 9 })
	if err != nil {
		t.Fatal(err)
	}
	if *oldPtr != oldCopy {
		t.Fatal("old pointer mutated")
	}
	if !res.Changed || res.Current.MinThreadLife != 9 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestUpdateChecked_PersistFailureKeepsPrevious(t *testing.T) {
	isolateConfigHome(t)
	start := DefaultConfig()
	start.ExtensionSecret = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	SetTestConfig(&start)
	if err := saveLocked(start); err != nil {
		t.Fatal(err)
	}
	oldPtr := Get()
	createConfigTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("persist boom")
	}
	res, err := UpdateChecked(func(c *AppConfig) { c.MinThreadLife = 99 })
	if err == nil {
		t.Fatal("expected persist error")
	}
	if Get() != oldPtr {
		t.Fatal("pointer swapped on persist failure")
	}
	if Get().MinThreadLife != start.MinThreadLife {
		t.Fatal("value changed on persist failure")
	}
	if res.Changed {
		t.Fatal("Changed must be false")
	}
	data, _ := os.ReadFile(GetConfigPath())
	if strings.Contains(string(data), `"min_thread_life": 99`) {
		t.Fatal("disk updated on persist failure")
	}
}

func TestUpdateChecked_CanonicalNoOp(t *testing.T) {
	isolateConfigHome(t)
	start := DefaultConfig()
	start.MaxConnections = "16"
	start.ExtensionSecret = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	SetTestConfig(&start)
	if err := saveLocked(start); err != nil {
		t.Fatal(err)
	}
	info1, _ := os.Stat(GetConfigPath())
	oldPtr := Get()
	res, err := UpdateChecked(func(c *AppConfig) { c.MaxConnections = " 016 " })
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatal("canonical no-op must not change")
	}
	if Get() != oldPtr {
		t.Fatal("pointer swapped on no-op")
	}
	info2, _ := os.Stat(GetConfigPath())
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("no-op wrote disk")
	}
}

func TestUpdateChecked_ReadersNeverSeeUnsanitized(t *testing.T) {
	isolateConfigHome(t)
	start := DefaultConfig()
	start.ExtensionSecret = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	SetTestConfig(&start)
	var stop atomic.Bool
	var bad atomic.Int64
	go func() {
		for !stop.Load() {
			c := Get()
			if c != nil && c.MaxConnections == " 64 " {
				bad.Add(1)
			}
		}
	}()
	for range 20 {
		if _, err := UpdateChecked(func(c *AppConfig) { c.MaxConnections = " 64 " }); err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateChecked(func(c *AppConfig) { c.MaxConnections = "128" }); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	time.Sleep(5 * time.Millisecond)
	if bad.Load() != 0 {
		t.Fatalf("observed unsanitized value %d times", bad.Load())
	}
}

func TestUpdateChecked_RenameFailureLeavesOldFileAndNoTempLeak(t *testing.T) {
	isolateConfigHome(t)
	start := DefaultConfig()
	start.ExtensionSecret = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	SetTestConfig(&start)
	if err := saveLocked(start); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(GetConfigPath())
	renameConfigFile = func(oldpath, newpath string) error {
		return errors.New("rename denied")
	}
	_, err := UpdateChecked(func(c *AppConfig) { c.MinThreadLife = 77 })
	if err == nil {
		t.Fatal("expected rename error")
	}
	data, _ := os.ReadFile(GetConfigPath())
	if !bytes.Equal(data, original) {
		t.Fatal("old JSON changed")
	}
	var leftover []string
	_ = filepath.Walk(filepath.Dir(GetConfigPath()), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(info.Name(), ".tmp") {
			leftover = append(leftover, path)
		}
		return nil
	})
	if len(leftover) != 0 {
		t.Fatalf("leaked temp files: %v", leftover)
	}
}

func TestUpdate_CompatibilityPersistsAndLogsFailure(t *testing.T) {
	isolateConfigHome(t)
	start := DefaultConfig()
	start.ExtensionSecret = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	SetTestConfig(&start)
	Update(func(c *AppConfig) { c.UserAgent = "compat-agent" })
	if Get().UserAgent != "compat-agent" {
		t.Fatal("Update did not persist success path")
	}
	createConfigTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("compat fail")
	}
	prev := Get()
	Update(func(c *AppConfig) { c.UserAgent = "should-not-stick" })
	if Get() != prev || Get().UserAgent != "compat-agent" {
		t.Fatal("Update failure must retain previous")
	}
}

func TestCanonicalBoundedIntRejectsNonDecimal(t *testing.T) {
	if canonicalBoundedInt("1_6", 1, 256, "16") != "16" {
		t.Fatal("underscore should fall back")
	}
	if canonicalBoundedInt("0x10", 1, 256, "16") != "16" {
		t.Fatal("hex should fall back")
	}
	if canonicalBoundedInt("8.0", 1, 256, "16") != "16" {
		t.Fatal("float should fall back")
	}
	if canonicalBoundedInt("8abc", 1, 256, "16") != "16" {
		t.Fatal("trailing junk should fall back")
	}
	if canonicalBoundedInt("+8", 1, 256, "16") != "8" {
		t.Fatal("+8 is accepted by decimal Atoi and canonicalized to 8")
	}
}
