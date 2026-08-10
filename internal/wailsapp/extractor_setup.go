package wailsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/tasks"
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

type diagnosticAuthProfileStore struct {
	inner extractor.AuthProfileStore
}

func wrapDiagnosticAuthProfileStore(store extractor.AuthProfileStore) extractor.AuthProfileStore {
	if store == nil || !appHostAuthDiagnosticsEnabled() {
		return store
	}

	return diagnosticAuthProfileStore{inner: store}
}

func (s diagnosticAuthProfileStore) SetAuthProfile(ctx context.Context, update extractor.AuthProfileUpdate) (extractor.AuthProfileSnapshot, error) {
	return s.inner.SetAuthProfile(ctx, update)
}

func (s diagnosticAuthProfileStore) AuthProfileSnapshots(ctx context.Context, packID string) ([]extractor.AuthProfileSnapshot, error) {
	snapshots, err := s.inner.AuthProfileSnapshots(ctx, packID)
	if err == nil {
		if len(snapshots) == 0 {
			appHostAuthRecordDiagnostic("store", "snapshot_bucket_zero")
		} else {
			appHostAuthRecordDiagnostic("store", "snapshot_bucket_nonzero")
		}
	}

	return snapshots, err
}

func (s diagnosticAuthProfileStore) ClearAuthProfile(ctx context.Context, packID string, profileID extractor.AuthProfileID) error {
	return s.inner.ClearAuthProfile(ctx, packID, profileID)
}

func (s diagnosticAuthProfileStore) ResolveAuthProfile(ctx context.Context, packID string, profileID extractor.AuthProfileID, rawURL string) (extractor.ResolvedAuthSecret, error) {
	return s.inner.ResolveAuthProfile(ctx, packID, profileID, rawURL)
}

func (s diagnosticAuthProfileStore) RecordWebViewAuthEvent(stage string, category string) {
	appHostAuthRecordDiagnostic(stage, category)
}

func ConfigureEmbeddedExtractorDispatcher(appService *App) error {
	return configureEmbeddedExtractorDispatcherWithDeps(appService, defaultEmbeddedExtractorConfigDeps())
}

type embeddedExtractorConfigDeps struct {
	hasEmbeddedReleasePacks          func() bool
	embeddedReleaseRequired          func() bool
	loadHostPolicyResolver           func() (extractor.HostPolicyResolver, error)
	loadAuthRuntimeBundle            func() (*extractor.PrivateAuthRuntimeBundle, error)
	defaultAuthProfileStorePath      func() (string, error)
	newFileAuthProfileStore          func(string) (extractor.AuthProfileStore, error)
	newAuthWebViewDriver             func(*App) extractor.AuthWebViewDriver
	newEmbeddedReleaseAddTaskAdapter func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error)
}

func defaultEmbeddedExtractorConfigDeps() embeddedExtractorConfigDeps {
	return embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:     extractor.HasEmbeddedReleasePacks,
		embeddedReleaseRequired:     extractor.EmbeddedReleaseRequired,
		loadHostPolicyResolver:      extractor.LoadPrivatePolicyBundleResolverFromRuntimeSources,
		loadAuthRuntimeBundle:       extractor.LoadPrivateAuthRuntimeBundleFromRuntimeSources,
		defaultAuthProfileStorePath: extractor.DefaultAuthProfileStorePath,
		newFileAuthProfileStore: func(path string) (extractor.AuthProfileStore, error) {
			return extractor.NewFileAuthProfileStore(path)
		},
		newAuthWebViewDriver: func(appService *App) extractor.AuthWebViewDriver {
			return newAppHostAuthDriver(appService)
		},
		newEmbeddedReleaseAddTaskAdapter: func(config extractor.EmbeddedReleaseDispatcherConfig, runtime *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			dispatcher, err := extractor.NewEmbeddedReleaseAddTaskDispatcher(config)
			if err != nil {
				return nil, err
			}
			return extractor.NewTasksAdapter(dispatcher, runtime), nil
		},
	}
}

func configureEmbeddedExtractorDispatcherWithDeps(appService *App, deps embeddedExtractorConfigDeps) error {
	if appService == nil {
		return nil
	}
	deps = normalizeEmbeddedExtractorConfigDeps(deps)
	diagnostic := &extractorStartupDiagnosticRecorder{}

	hasPacks := deps.hasEmbeddedReleasePacks()
	if hasPacks {
		diagnostic.record("embedded_pack", "present")
	} else {
		diagnostic.record("embedded_pack", "absent")
	}
	required := deps.embeddedReleaseRequired()
	if required {
		diagnostic.record("embedded_release", "required")
	} else {
		diagnostic.record("embedded_release", "optional")
	}
	authBundle, err := deps.loadAuthRuntimeBundle()
	if err != nil {
		diagnostic.record("auth_runtime_load", "invalid")
		diagnostic.record("policy_load", "skipped")
		diagnostic.record("auth_store", "skipped")
		diagnostic.record("host_auth_runtime", "skipped")
		diagnostic.record("driver", "skipped")
		diagnostic.record("dispatcher", "skipped")
		diagnostic.finish("activation_missing_or_skipped")
		return sanitizedEmbeddedExtractorConfigError("load auth runtime bundle", err)
	}
	if authBundle != nil && authBundle.PackCount() > 0 {
		diagnostic.record("auth_runtime_load", "loaded_nonzero")
	} else {
		diagnostic.record("auth_runtime_load", "loaded_zero")
	}
	hasAuthRuntime := authBundle != nil && authBundle.PackCount() > 0
	if !hasPacks && !required && !hasAuthRuntime {
		diagnostic.record("policy_load", "skipped")
		diagnostic.record("auth_store", "skipped")
		diagnostic.record("host_auth_runtime", "skipped")
		diagnostic.record("driver", "skipped")
		diagnostic.record("dispatcher", "skipped")
		diagnostic.finish("no_runtime_inputs")
		return nil
	}

	var hostPolicyResolver extractor.HostPolicyResolver
	if hasPacks || required {
		hostPolicyResolver, err = deps.loadHostPolicyResolver()
		if err != nil {
			diagnostic.record("policy_load", "invalid")
			diagnostic.record("auth_store", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("driver", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("load host policy resolver", err)
		}
		if hostPolicyResolver != nil {
			diagnostic.record("policy_load", "loaded")
		} else {
			diagnostic.record("policy_load", "skipped")
		}
	} else {
		diagnostic.record("policy_load", "skipped")
	}

	var store extractor.AuthProfileStore
	if hasPacks || hasAuthRuntime {
		storePath, err := deps.defaultAuthProfileStorePath()
		if err != nil {
			diagnostic.record("auth_store", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("driver", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("locate auth profile store", err)
		}
		store, err = deps.newFileAuthProfileStore(storePath)
		if err != nil {
			diagnostic.record("auth_store", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("driver", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("load auth profile store", err)
		}
		if store == nil {
			diagnostic.record("auth_store", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("driver", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("load auth profile store", errors.New("auth profile store is nil"))
		}
		store = wrapDiagnosticAuthProfileStore(store)
		diagnostic.record("auth_store", "configured")
	} else {
		diagnostic.record("auth_store", "skipped")
	}

	var authResolver extractor.AuthProfileResolver
	var hostRuntime *extractor.HostAuthRuntime
	var driver extractor.AuthWebViewDriver
	switch {
	case hasAuthRuntime:
		driver = deps.newAuthWebViewDriver(appService)
		if driver == nil {
			diagnostic.record("driver", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("create auth webview driver", errors.New("auth webview driver is nil"))
		}
		diagnostic.record("driver", "configured")
		coordinator := extractor.NewWebViewAuthCoordinator(store, driver)
		hostRuntime = extractor.NewHostAuthRuntime(extractor.HostAuthRuntimeConfig{
			Bundle:             authBundle,
			Store:              store,
			Coordinator:        coordinator,
			HostPolicyResolver: hostPolicyResolver,
		})
		authResolver = hostRuntime
		diagnostic.record("host_auth_runtime", "configured")
	case store != nil:
		authResolver = store
		diagnostic.record("driver", "skipped")
		diagnostic.record("host_auth_runtime", "skipped")
	default:
		diagnostic.record("driver", "skipped")
		diagnostic.record("host_auth_runtime", "skipped")
	}
	appService.setHostAuthState(store, hostRuntime, driver)

	adapter, err := deps.newEmbeddedReleaseAddTaskAdapter(extractor.EmbeddedReleaseDispatcherConfig{
		AuthResolver:       authResolver,
		HostPolicyResolver: hostPolicyResolver,
		AuthRuntimeBundle:  authBundle,
	}, hostRuntime)
	if err != nil {
		diagnostic.record("dispatcher", "failed")
		diagnostic.finish("activation_missing_or_skipped")
		return sanitizedEmbeddedExtractorConfigError("create embedded extractor dispatcher", err)
	}
	if adapter != nil {
		appService.setExtractorAdapter(adapter)
		diagnostic.record("dispatcher", "configured")
		diagnostic.finish("activation_proved")
		return nil
	}
	diagnostic.record("dispatcher", "skipped")
	diagnostic.finish("activation_missing_or_skipped")

	return nil
}

func normalizeEmbeddedExtractorConfigDeps(deps embeddedExtractorConfigDeps) embeddedExtractorConfigDeps {
	defaults := defaultEmbeddedExtractorConfigDeps()
	if deps.hasEmbeddedReleasePacks == nil {
		deps.hasEmbeddedReleasePacks = defaults.hasEmbeddedReleasePacks
	}
	if deps.embeddedReleaseRequired == nil {
		deps.embeddedReleaseRequired = defaults.embeddedReleaseRequired
	}
	if deps.loadHostPolicyResolver == nil {
		deps.loadHostPolicyResolver = defaults.loadHostPolicyResolver
	}
	if deps.loadAuthRuntimeBundle == nil {
		deps.loadAuthRuntimeBundle = defaults.loadAuthRuntimeBundle
	}
	if deps.defaultAuthProfileStorePath == nil {
		deps.defaultAuthProfileStorePath = defaults.defaultAuthProfileStorePath
	}
	if deps.newFileAuthProfileStore == nil {
		deps.newFileAuthProfileStore = defaults.newFileAuthProfileStore
	}
	if deps.newAuthWebViewDriver == nil {
		deps.newAuthWebViewDriver = defaults.newAuthWebViewDriver
	}
	if deps.newEmbeddedReleaseAddTaskAdapter == nil {
		deps.newEmbeddedReleaseAddTaskAdapter = defaults.newEmbeddedReleaseAddTaskAdapter
	}

	return deps
}

func sanitizedEmbeddedExtractorConfigError(action string, err error) error {
	return fmt.Errorf("%s failed", action)
}

func loadEmbeddedExtractorHostPolicyResolver() (extractor.HostPolicyResolver, error) {
	return extractor.LoadPrivatePolicyBundleResolverFromRuntimeSources()
}
