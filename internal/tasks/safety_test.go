package tasks

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeAria2OutFilename_ValidBasenames(t *testing.T) {
	cases := []string{"file.bin", "archive.tar.gz", "no_extension", "UPPER.CASE.TXT"}
	for _, name := range cases {
		got, err := SafeAria2OutFilename(name)
		if err != nil {
			t.Fatalf("SafeAria2OutFilename(%q) error = %v", name, err)
		}
		if got != name {
			t.Fatalf("SafeAria2OutFilename(%q) = %q, want %q", name, got, name)
		}
	}
}

func TestSafeAria2OutFilename_RejectsEmpty(t *testing.T) {
	if _, err := SafeAria2OutFilename(""); err == nil {
		t.Fatal("SafeAria2OutFilename(\"\") = nil, want error")
	}
	if _, err := SafeAria2OutFilename("   "); err == nil {
		t.Fatal("SafeAria2OutFilename(\"   \") = nil, want error")
	}
}

func TestSafeAria2OutFilename_RejectsPathSeparators(t *testing.T) {
	cases := []string{"path/to/file", `back\slash`, "with/slash"}
	for _, name := range cases {
		if _, err := SafeAria2OutFilename(name); err == nil {
			t.Fatalf("SafeAria2OutFilename(%q) = nil, want error", name)
		}
	}
}

func TestSafeAria2OutFilename_RejectsDotDot(t *testing.T) {
	cases := []string{"..", "...", "file..name"}
	for _, name := range cases {
		if _, err := SafeAria2OutFilename(name); err == nil {
			t.Fatalf("SafeAria2OutFilename(%q) = nil, want error", name)
		}
	}
}

func TestSafeAria2OutFilename_RejectsCRLF(t *testing.T) {
	cases := []string{"file\r.bin", "file\n.bin", "file\r\n.bin"}
	for _, name := range cases {
		if _, err := SafeAria2OutFilename(name); err == nil {
			t.Fatalf("SafeAria2OutFilename(%q) = nil, want error", name)
		}
	}
}

func TestSafeAria2OutFilename_TrimsWhitespace(t *testing.T) {
	got, err := SafeAria2OutFilename("  file.bin  ")
	if err != nil {
		t.Fatalf("SafeAria2OutFilename() error = %v", err)
	}
	if got != "file.bin" {
		t.Fatalf("SafeAria2OutFilename() = %q, want file.bin", got)
	}
}

func TestSafeAria2OutFilename_RejectsReservedDeviceNames(t *testing.T) {
	for _, name := range []string{"CON.txt", "CON::$DATA", "COM1:", "NUL:"} {
		_, err := SafeAria2OutFilename(name)
		if err == nil {
			t.Fatalf("SafeAria2OutFilename(%q) = nil, want reserved error", name)
		}
		if !errors.Is(err, ErrReservedOutFilename) {
			t.Fatalf("SafeAria2OutFilename(%q) error = %v, want ErrReservedOutFilename", name, err)
		}
	}
}

func TestRedactAssignmentValues_RedactsKnownMarkers(t *testing.T) {
	input := "error: token=secret123 auth=bearer456 key=private789"
	got := redactAssignmentValues(input)
	if strings.Contains(got, "secret123") || strings.Contains(got, "bearer456") || strings.Contains(got, "private789") {
		t.Fatalf("redactAssignmentValues() = %q, leaked secret values", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redactAssignmentValues() = %q, want [REDACTED] marker", got)
	}
}

func TestRedactAssignmentValues_PreservesNonSecretText(t *testing.T) {
	input := "connection failed at host example.com"
	got := redactAssignmentValues(input)
	if got != input {
		t.Fatalf("redactAssignmentValues() = %q, want %q", got, input)
	}
}
