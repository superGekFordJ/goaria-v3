package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoad_GeneratesExtensionSecretIfEmpty(t *testing.T) {
	orig := Get()
	t.Cleanup(func() { SetTestConfig(orig) })

	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	// Write a config without extension_secret.
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
		t.Fatal("Current should not be nil after Load")
	}
	if Get().ExtensionSecret == "" {
		t.Fatal("ExtensionSecret should be generated on first boot")
	}
	if len(Get().ExtensionSecret) != 64 {
		t.Fatalf("ExtensionSecret should be 64 hex chars, got %d", len(Get().ExtensionSecret))
	}

	// Verify it was persisted to disk.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), Get().ExtensionSecret) {
		t.Fatal("ExtensionSecret should be persisted in config.json")
	}
}

func TestLoad_PreservesExistingExtensionSecret(t *testing.T) {
	orig := Get()
	t.Cleanup(func() { SetTestConfig(orig) })

	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

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

func TestLoad_DoesNotWipeConfigOnFailedRead(t *testing.T) {
	orig := Get()
	t.Cleanup(func() { SetTestConfig(orig) })

	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".goaria")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	originalContent := `{"rpc_port":"20000","download_dir":"/data/dl","extension_secret":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}`
	if err := os.WriteFile(cfgPath, []byte(originalContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(cfgPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })
	}

	Load()

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config should still be readable after Load: %v", err)
	}
	if !strings.Contains(string(data), `"rpc_port":"20000"`) {
		t.Fatalf("config was wiped to defaults, got: %s", string(data))
	}
	if !strings.Contains(string(data), `"download_dir":"/data/dl"`) {
		t.Fatalf("download_dir was wiped, got: %s", string(data))
	}
}
