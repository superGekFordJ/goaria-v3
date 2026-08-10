//go:build extractor

package wailsapp

import (
	"context"
	"errors"
	"fmt"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/tasks"
)

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
