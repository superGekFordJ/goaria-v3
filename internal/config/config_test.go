package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_GeneratesExtensionSecretIfEmpty(t *testing.T) {
	orig := Current
	t.Cleanup(func() { Current = orig })

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
	if Current == nil {
		t.Fatal("Current should not be nil after Load")
	}
	if Current.ExtensionSecret == "" {
		t.Fatal("ExtensionSecret should be generated on first boot")
	}
	if len(Current.ExtensionSecret) != 64 {
		t.Fatalf("ExtensionSecret should be 64 hex chars, got %d", len(Current.ExtensionSecret))
	}

	// Verify it was persisted to disk.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), Current.ExtensionSecret) {
		t.Fatal("ExtensionSecret should be persisted in config.json")
	}
}

func TestLoad_PreservesExistingExtensionSecret(t *testing.T) {
	orig := Current
	t.Cleanup(func() { Current = orig })

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
	if Current.ExtensionSecret != existingSecret {
		t.Fatalf("ExtensionSecret should be preserved, got %q", Current.ExtensionSecret)
	}
}
