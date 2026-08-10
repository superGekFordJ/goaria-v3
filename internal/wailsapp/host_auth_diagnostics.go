package wailsapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	appHostAuthUnavailableMessage    = "auth webview session unavailable"
	appHostAuthInProgressMessage     = "auth webview session already in progress"
	appHostAuthInvalidPayloadMessage = "auth webview callback payload invalid"
	appHostAuthCallbackErrorMessage  = "auth webview session failed"
	appHostAuthCallbackPrefix        = "/_goaria/auth/callback/"
	appHostAuthSessionHeader         = "X-Goaria-Auth-Session"
	appHostAuthInitialURL            = "/"
	appHostAuthInitialHTML           = `<!doctype html><html><head><meta charset="utf-8"><title>GoAria Auth</title></head><body></body></html>`
	appHostAuthCORSAllowedHeaders    = "content-type, x-goaria-auth-session"
	appHostAuthDiagnosticEnv         = "GOARIA_WEBVIEW_AUTH_DIAGNOSTIC"
	appHostAuthDiagnosticLogEnv      = "GOARIA_WEBVIEW_AUTH_DIAGNOSTIC_LOG"
	appHostAuthSourceProbeEnv        = "GOARIA_WEBVIEW_AUTH_SOURCE_PROBE"
	appHostAuthDiagnosticDefaultLog  = "build/extractor/cache/spec113-webview-auth-diagnostic.jsonl"
)

type appHostAuthDiagnosticObserver struct{}

var appHostAuthDiagnosticSeq atomic.Int64

var appHostAuthDiagnosticMu sync.Mutex

type appHostAuthDiagnosticEvent struct {
	Seq       int64  `json:"seq"`
	Stage     string `json:"stage"`
	Category  string `json:"category"`
	Timestamp string `json:"timestamp,omitempty"`
}

func appHostAuthDiagnosticsEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(appHostAuthDiagnosticEnv)))

	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func appHostAuthSourceProbeEnabled() bool {
	if !appHostAuthDiagnosticsEnabled() {
		return false
	}
	value := strings.TrimSpace(strings.ToLower(os.Getenv(appHostAuthSourceProbeEnv)))

	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (appHostAuthDiagnosticObserver) RecordWebViewAuthEvent(stage string, category string) {
	appHostAuthRecordDiagnostic(stage, category)
}

func appHostAuthRecordDiagnostic(stage string, category string) {
	if !appHostAuthDiagnosticsEnabled() || !appHostAuthDiagnosticAllowed(stage, category) {
		return
	}
	logPath := strings.TrimSpace(os.Getenv(appHostAuthDiagnosticLogEnv))
	if logPath == "" {
		logPath = appHostAuthDiagnosticDefaultLog
	}
	if strings.TrimSpace(filepath.Base(logPath)) == "" {
		return
	}
	cleanPath := filepath.Clean(logPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return
	}
	event := appHostAuthDiagnosticEvent{
		Seq:       appHostAuthDiagnosticSeq.Add(1),
		Stage:     stage,
		Category:  category,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	appHostAuthDiagnosticMu.Lock()
	defer appHostAuthDiagnosticMu.Unlock()
	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(raw, '\n'))
}

func appHostAuthRawDiagnosticAllowed(stage string, category string) bool {
	if stage != "injection" {
		return false
	}
	switch category {
	case "script_running", "origin_check_passed":
		return true
	default:
		return false
	}
}

func appHostAuthDiagnosticAllowed(stage string, category string) bool {
	switch stage {
	case "driver":
		switch category {
		case "open_attempted", "opened", "unavailable", "inflight_rejected":
			return true
		}
	case "injection":
		switch category {
		case "navigation_completed", "execjs_dispatched", "script_running", "origin_check_passed", "attempted", "origin_mismatch", "marker_skip", "collector_eval_attempted", "collector_eval_succeeded", "collector_eval_failed", "collector_function_missing", "collector_invoked":
			return true
		}
	case "raw_message":
		return category == "handler_invoked"
	case "post_capture":
		switch category {
		case "called", "rejected":
			return true
		}
	case "callback_route":
		switch category {
		case "hit", "diagnostic_event_accepted", "diagnostic_event_rejected", "method_rejected", "origin_rejected", "content_type_rejected", "session_rejected", "body_rejected", "late_or_expired":
			return true
		}
	case "parser":
		switch category {
		case "accepted", "rejected_payload", "rejected_secret_candidate", "rejected_kind", "rejected_expiry":
			return true
		}
	case "session":
		switch category {
		case "success", "timeout", "cancel", "error":
			return true
		}
	case "store":
		switch category {
		case "set_attempted", "set_succeeded", "set_failed", "snapshot_bucket_zero", "snapshot_bucket_nonzero":
			return true
		}
	case "collector_attempt":
		return category == "ticked"
	case "collector_source":
		switch category {
		case "bounded_found", "bounded_not_found", "bounded_invalid", "single_fire_already_done":
			return true
		}
	case "collector_probe":
		switch category {
		case "started", "completed", "storage_checked", "storage_present", "storage_absent", "storage_invalid", "cookie_checked", "cookie_present", "cookie_absent", "cookie_invalid", "request_header_checked", "request_header_present", "request_header_absent", "request_header_invalid", "response_json_checked", "response_json_present", "response_json_absent", "response_json_invalid", "network_hooks_installed", "network_hooks_absent", "network_fetch_seen", "network_xhr_seen", "request_header_source_checked", "request_header_source_matched", "request_header_source_unmatched", "request_header_value_absent", "response_source_checked", "response_source_matched", "response_source_unmatched", "candidate_shape_nonempty", "candidate_shape_parser_compatible", "candidate_shape_invalid", "post_capture_suppressed", "no_bounded_channel", "channel_count_one", "channel_count_many":
			return true
		}
	}

	return false
}

func appHostAuthRawMessageHandler(_ application.Window, message string, _ *application.OriginInfo) {
	const prefix = "goaria-auth-diag:"
	if !strings.HasPrefix(message, prefix) {
		return
	}
	payload := strings.TrimPrefix(message, prefix)
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) != 2 {
		return
	}
	if !appHostAuthRawDiagnosticAllowed(parts[0], parts[1]) {
		return
	}
	appHostAuthRecordDiagnostic("raw_message", "handler_invoked")
	appHostAuthRecordDiagnostic(parts[0], parts[1])
}
