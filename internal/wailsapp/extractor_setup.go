//go:build extractor

package wailsapp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"

	"goaria-v3/internal/config"
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
	return configureEmbeddedExtractorDispatcherWithDeps(appService, defaultEmbeddedExtractorConfigDeps(appService))
}

type embeddedExtractorConfigDeps struct {
	hasEmbeddedReleasePacks              func() bool
	embeddedReleaseRequired              func() bool
	privatePolicyRuntimeSourceState      func() extractor.PrivateBundleSourceState
	privateAuthRuntimeRuntimeSourceState func() extractor.PrivateBundleSourceState
	loadHostPolicyResolver               func() (extractor.HostPolicyResolver, error)
	loadAuthRuntimeBundle                func() (*extractor.PrivateAuthRuntimeBundle, error)
	defaultAuthProfileStorePath          func() (string, error)
	newFileAuthProfileStore              func(string) (extractor.AuthProfileStore, error)
	newAuthWebViewDriver                 func(*App) extractor.AuthWebViewDriver
	newEmbeddedReleaseAddTaskAdapter     func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error)
	dataRoot                             func() (string, error)
	acceptedEmbeddedPacks                func(extractor.EmbeddedReleaseDispatcherConfig) ([]extractor.EmbeddedPack, error)
	embeddedReleaseTrustedPublicKeys     func() []ed25519.PublicKey
	newRuntimeManager                    func(context.Context, extractor.ExtractorRuntimeManagerConfig) (*extractor.ExtractorRuntimeManager, error)
}

func defaultEmbeddedExtractorConfigDeps(appService *App) embeddedExtractorConfigDeps {
	return embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:              extractor.HasEmbeddedReleasePacks,
		embeddedReleaseRequired:              extractor.EmbeddedReleaseRequired,
		privatePolicyRuntimeSourceState:      extractor.PrivatePolicyBundleRuntimeSourceState,
		privateAuthRuntimeRuntimeSourceState: extractor.PrivateAuthRuntimeBundleRuntimeSourceState,
		loadHostPolicyResolver:               extractor.LoadPrivatePolicyBundleResolverFromRuntimeSources,
		loadAuthRuntimeBundle:                extractor.LoadPrivateAuthRuntimeBundleFromRuntimeSources,
		defaultAuthProfileStorePath:          extractor.DefaultAuthProfileStorePath,
		newFileAuthProfileStore: func(path string) (extractor.AuthProfileStore, error) {
			return extractor.NewFileAuthProfileStore(path)
		},
		newAuthWebViewDriver: func(appService *App) extractor.AuthWebViewDriver {
			return newAppHostAuthDriver(appService)
		},
		dataRoot: func() (string, error) {
			return filepath.Join(filepath.Dir(config.GetConfigPath()), "extractor"), nil
		},
		acceptedEmbeddedPacks:            extractor.AcceptedEmbeddedReleasePacks,
		embeddedReleaseTrustedPublicKeys: extractor.EmbeddedReleaseTrustedPublicKeys,
		newRuntimeManager:                extractor.NewExtractorRuntimeManager,
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
	policySourceState := deps.privatePolicyRuntimeSourceState()
	diagnostic.record("policy_source", string(policySourceState))
	authRuntimeSourceState := deps.privateAuthRuntimeRuntimeSourceState()
	diagnostic.record("auth_runtime_source", string(authRuntimeSourceState))

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

	var hostPolicyResolver extractor.HostPolicyResolver
	if hasPacks || required || policySourceState != extractor.PrivateBundleSourceStateNone {
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

	var acceptedPacks []extractor.EmbeddedPack
	if hasPacks || required {
		acceptedPacks, err = deps.acceptedEmbeddedPacks(extractor.EmbeddedReleaseDispatcherConfig{
			AuthResolver:       nil,
			HostPolicyResolver: hostPolicyResolver,
			AuthRuntimeBundle:  authBundle,
			Required:           &required,
		})
		if err != nil {
			diagnostic.record("auth_store", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("driver", "skipped")
			diagnostic.record("dispatcher", "failed")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("verify embedded release packs", err)
		}
	}

	var store extractor.AuthProfileStore
	var hostRuntime *extractor.HostAuthRuntime
	var driver extractor.AuthWebViewDriver
	var authResolver extractor.AuthProfileResolver

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
		if err != nil || store == nil {
			diagnostic.record("auth_store", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("driver", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("load auth profile store", err)
		}
		store = wrapDiagnosticAuthProfileStore(store)
		diagnostic.record("auth_store", "configured")
	} else {
		diagnostic.record("auth_store", "skipped")
	}

	switch {
	case hasAuthRuntime:
		driver = deps.newAuthWebViewDriver(appService)
		if driver == nil {
			diagnostic.record("driver", "skipped")
			diagnostic.record("host_auth_runtime", "skipped")
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("activation_missing_or_skipped")
			return sanitizedEmbeddedExtractorConfigError("create auth webview driver", errors.New("driver is nil"))
		}
		diagnostic.record("driver", "configured")

		coordinator := extractor.NewWebViewAuthCoordinator(store, driver)
		coordinator.SetObserver(appHostAuthDiagnosticObserver{})
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

	if deps.newEmbeddedReleaseAddTaskAdapter != nil {
		if !hasPacks && !required && !hasAuthRuntime {
			diagnostic.record("dispatcher", "skipped")
			diagnostic.finish("no_runtime_inputs")
			return nil
		}
		dispConfig := extractor.EmbeddedReleaseDispatcherConfig{
			AuthResolver:       authResolver,
			HostPolicyResolver: hostPolicyResolver,
			AuthRuntimeBundle:  authBundle,
		}
		adapter, err := deps.newEmbeddedReleaseAddTaskAdapter(dispConfig, hostRuntime)
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

	dataRoot, err := deps.dataRoot()
	if err != nil {
		diagnostic.record("dispatcher", "failed")
		diagnostic.finish("activation_missing_or_skipped")
		return sanitizedEmbeddedExtractorConfigError("locate extractor data root", err)
	}

	trustPolicy := extractor.DefaultTrustPolicy()
	if deps.embeddedReleaseTrustedPublicKeys != nil {
		trustPolicy.TrustedPublicKeys = deps.embeddedReleaseTrustedPublicKeys()
	} else {
		trustPolicy.TrustedPublicKeys = extractor.EmbeddedReleaseTrustedPublicKeys()
	}

	mgr, err := deps.newRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		EmbeddedPacks:      acceptedPacks,
		TrustPolicy:        trustPolicy,
		HostPolicyResolver: hostPolicyResolver,
		AuthResolver:       authResolver,
		HostAuthRuntime:    hostRuntime,
	})
	if err != nil {
		diagnostic.record("dispatcher", "failed")
		diagnostic.finish("activation_missing_or_skipped")
		return sanitizedEmbeddedExtractorConfigError("create extractor runtime manager", err)
	}

	runtime := newTaggedExtractorRuntime(mgr)
	appService.setExtractorRuntime(runtime)

	if mgr.CurrentSnapshot().TasksAdapter() != nil {
		diagnostic.record("dispatcher", "configured")
		diagnostic.finish("activation_proved")
	} else {
		diagnostic.record("dispatcher", "skipped")
		if !hasPacks && !required && !hasAuthRuntime {
			diagnostic.finish("no_runtime_inputs")
		} else {
			diagnostic.finish("activation_missing_or_skipped")
		}
	}

	return nil
}

func normalizeEmbeddedExtractorConfigDeps(deps embeddedExtractorConfigDeps) embeddedExtractorConfigDeps {
	defaults := defaultEmbeddedExtractorConfigDeps(nil)
	if deps.hasEmbeddedReleasePacks == nil {
		deps.hasEmbeddedReleasePacks = defaults.hasEmbeddedReleasePacks
	}
	if deps.embeddedReleaseRequired == nil {
		deps.embeddedReleaseRequired = defaults.embeddedReleaseRequired
	}
	if deps.privatePolicyRuntimeSourceState == nil {
		deps.privatePolicyRuntimeSourceState = defaults.privatePolicyRuntimeSourceState
	}
	if deps.privateAuthRuntimeRuntimeSourceState == nil {
		deps.privateAuthRuntimeRuntimeSourceState = defaults.privateAuthRuntimeRuntimeSourceState
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
	if deps.dataRoot == nil {
		deps.dataRoot = defaults.dataRoot
	}
	if deps.acceptedEmbeddedPacks == nil {
		deps.acceptedEmbeddedPacks = defaults.acceptedEmbeddedPacks
	}
	if deps.embeddedReleaseTrustedPublicKeys == nil {
		deps.embeddedReleaseTrustedPublicKeys = defaults.embeddedReleaseTrustedPublicKeys
	}
	if deps.newRuntimeManager == nil {
		deps.newRuntimeManager = defaults.newRuntimeManager
	}

	return deps
}

func sanitizedEmbeddedExtractorConfigError(action string, err error) error {
	return fmt.Errorf("%s failed", action)
}

func loadEmbeddedExtractorHostPolicyResolver() (extractor.HostPolicyResolver, error) {
	return extractor.LoadPrivatePolicyBundleResolverFromRuntimeSources()
}
