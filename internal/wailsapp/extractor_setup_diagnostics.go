package wailsapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	extractorStartupDiagnosticEnv        = "GOARIA_EXTRACTOR_STARTUP_DIAGNOSTIC"
	extractorStartupDiagnosticLogEnv     = "GOARIA_EXTRACTOR_STARTUP_DIAGNOSTIC_LOG"
	extractorStartupDiagnosticDefaultLog = "build/extractor/cache/spec129-startup-activation.jsonl"
)

type extractorStartupDiagnosticEvent struct {
	Seq       int64  `json:"seq"`
	Stage     string `json:"stage"`
	Category  string `json:"category"`
	Timestamp string `json:"timestamp,omitempty"`
}

var extractorStartupDiagnosticSeq atomic.Int64

var extractorStartupDiagnosticMu sync.Mutex

func extractorStartupDiagnosticsEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(extractorStartupDiagnosticEnv)))

	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func extractorStartupDiagnosticAllowed(stage string, category string) bool {
	switch stage {
	case "embedded_pack":
		return category == "present" || category == "absent"
	case "embedded_release":
		return category == "required" || category == "optional"
	case "policy_source", "auth_runtime_source":
		switch category {
		case "none", "env", "embedded", "ambiguous":
			return true
		}
	case "policy_load":
		switch category {
		case "loaded", "skipped", "invalid":
			return true
		}
	case "auth_runtime_load":
		switch category {
		case "loaded_nonzero", "loaded_zero", "invalid":
			return true
		}
	case "auth_store", "host_auth_runtime", "driver":
		return category == "configured" || category == "skipped"
	case "dispatcher":
		switch category {
		case "configured", "skipped", "failed":
			return true
		}
	case "startup_activation":
		switch category {
		case "no_runtime_inputs", "activation_missing_or_skipped", "activation_proved":
			return true
		}
	}

	return false
}

func extractorStartupRecordDiagnostic(stage string, category string) {
	if !extractorStartupDiagnosticsEnabled() || !extractorStartupDiagnosticAllowed(stage, category) {
		return
	}
	logPath := strings.TrimSpace(os.Getenv(extractorStartupDiagnosticLogEnv))
	if logPath == "" {
		logPath = extractorStartupDiagnosticDefaultLog
	}
	if strings.TrimSpace(filepath.Base(logPath)) == "" {
		return
	}
	cleanPath := filepath.Clean(logPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return
	}
	event := extractorStartupDiagnosticEvent{
		Seq:       extractorStartupDiagnosticSeq.Add(1),
		Stage:     stage,
		Category:  category,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	extractorStartupDiagnosticMu.Lock()
	defer extractorStartupDiagnosticMu.Unlock()
	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(raw, '\n'))
}

type extractorStartupDiagnosticRecorder struct {
	terminalRecorded bool
}

func (r *extractorStartupDiagnosticRecorder) record(stage string, category string) {
	extractorStartupRecordDiagnostic(stage, category)
}

func (r *extractorStartupDiagnosticRecorder) finish(category string) {
	if r == nil || r.terminalRecorded {
		return
	}
	r.terminalRecorded = true
	extractorStartupRecordDiagnostic("startup_activation", category)
}
