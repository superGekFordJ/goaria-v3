package wailsapp

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/process"
)

func sampleCanonicalConfig() config.AppConfig {
	return config.ValidateAndSanitize(config.AppConfig{
		RPCPort:                "16800",
		RPCSecret:              "test-secret",
		DownloadDir:            `C:\downloads`,
		MaxConnections:         "16",
		MaxConcurrentDownloads: "5",
		UserAgent:              "GoAria-Test/1.0",
		ShowHistory:            true,
		WindowTransparency:     "none",
		SmartThreadMode:        true,
		MinThreadLife:          5,
		CloseToTray:            false,
		ConvergenceInterval:    0,
		ExtensionEnabled:       true,
		ExtensionWSPort:        16801,
		ExtensionSecret:        "managed-secret-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
}

func TestAria2Projection_Equality(t *testing.T) {
	base := sampleCanonicalConfig()
	again := sampleCanonicalConfig()
	if ariaProjection(base) != ariaProjection(again) {
		t.Fatal("canonical self inequality")
	}
	padded := base
	padded.MaxConnections = "016"
	if ariaProjection(base) != ariaProjection(padded) {
		t.Fatal("raw 016 vs 16 must be equal after sanitize")
	}
	high := base
	high.MaxConnections = "64"
	higher := base
	higher.MaxConnections = "128"
	if ariaProjection(high) != ariaProjection(higher) {
		t.Fatal("64→128 must keep Aria effective 16")
	}
	if ariaProjection(high).MaxConnections != 16 {
		t.Fatalf("effective = %d", ariaProjection(high).MaxConnections)
	}
}

func TestAria2Projection_Inequality(t *testing.T) {
	base := sampleCanonicalConfig()
	from8 := base
	from8.MaxConnections = "8"
	to32 := base
	to32.MaxConnections = "32"
	p8 := ariaProjection(from8)
	p32 := ariaProjection(to32)
	if p8 == p32 {
		t.Fatal("8→32 must change projection")
	}
	if p32.MaxConnections != 16 {
		t.Fatalf("32 effective = %d, want 16", p32.MaxConnections)
	}

	cases := []config.AppConfig{}
	sec := base
	sec.RPCSecret = "alt-secret"
	cases = append(cases, sec)
	port := base
	port.RPCPort = "16810"
	cases = append(cases, port)
	dir := base
	dir.DownloadDir = `D:\alt-downloads`
	cases = append(cases, dir)
	conc := base
	conc.SmartThreadMode = false
	conc.MaxConcurrentDownloads = "8"
	cases = append(cases, conc)
	ua := base
	ua.UserAgent = "Other/1.0"
	cases = append(cases, ua)
	for i, c := range cases {
		if ariaProjection(base) == ariaProjection(c) {
			t.Errorf("case %d should change Aria projection", i)
		}
	}
}

func TestAria2Projection_IgnoresNonAriaFields(t *testing.T) {
	base := sampleCanonicalConfig()
	other := base
	other.WindowTransparency = "mica"
	other.SmartThreadMode = false
	other.ConvergenceInterval = 10
	other.ExtensionEnabled = false
	other.ExtensionWSPort = 16802
	other.ShowHistory = false
	other.MinThreadLife = 9
	if ariaProjection(base) != ariaProjection(other) {
		t.Fatal("non-Aria fields must not change projection")
	}
}

func TestAriaProjection_MaxConcurrentDownloads(t *testing.T) {
	base := sampleCanonicalConfig() // smart on, raw 5 -> effective 5

	smartRaw8 := base
	smartRaw8.MaxConcurrentDownloads = "8"
	if ariaProjection(base) != ariaProjection(smartRaw8) {
		t.Fatal("smart mode must project the builtin 5 regardless of raw")
	}

	off5 := base
	off5.SmartThreadMode = false
	off8 := off5
	off8.MaxConcurrentDownloads = "8"
	if ariaProjection(off5) == ariaProjection(off8) {
		t.Fatal("off mode must project the user raw value")
	}
	if ariaProjection(smartRaw8) == ariaProjection(off8) {
		t.Fatal("smart raw8 (effective 5) vs off raw8 (effective 8) must differ")
	}
}

func TestSaveConfigResult_ValueSnapshot(t *testing.T) {
	cfg := sampleCanonicalConfig()
	res := successSaveResult(cfg, true)
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RPCPort = "9999"
	var decoded SaveConfigResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Config.RPCPort != "16800" {
		t.Fatalf("snapshot followed later mutation: %q", decoded.Config.RPCPort)
	}
	if decoded.Config.RPCPort == cfg.RPCPort {
		t.Fatal("result config must be a value copy")
	}
}

type saveHarness struct {
	store              fakeConfigStore
	log                []string
	mu                 sync.Mutex
	validateErr        error
	restartErr         error
	rollbackRestartErr error
	readyErr           error
	rollbackReady      error
	restarts           int
	validates          int
	rpcInits           int
	notifiers          int
	stops              int
	notifierStops      int
	portWaits          int
	extPort            int
	extListening       bool
	lastRestart        config.AppConfig
	lastRPCPort        string
	lastNotifierPort   string
}

type fakeConfigStore struct {
	mu          sync.Mutex
	cur         config.AppConfig
	persistErr  error
	rollbackErr error
	updates     int
}

func (f *fakeConfigStore) get() *config.AppConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cur
	return &c
}

func (f *fakeConfigStore) updateChecked(mutate func(*config.AppConfig)) (config.UpdateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := f.cur
	cand := prev
	mutate(&cand)
	cand = config.ValidateAndSanitize(cand)
	if cand == prev {
		return config.UpdateResult{Previous: prev, Current: prev}, nil
	}
	f.updates++
	err := f.persistErr
	if f.updates > 1 {
		err = f.rollbackErr
	}
	if err != nil {
		return config.UpdateResult{Previous: prev, Current: prev}, err
	}
	f.cur = cand
	return config.UpdateResult{Previous: prev, Current: cand, Changed: true}, nil
}

func (h *saveHarness) add(step string) {
	h.mu.Lock()
	h.log = append(h.log, step)
	h.mu.Unlock()
}

func (h *saveHarness) steps() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.log))
	copy(out, h.log)
	return out
}

func (h *saveHarness) app() *App {
	a := &App{eventHub: &events.Hub{}}
	a.configDeps = configSaveDeps{
		get: h.store.get,
		updateChecked: func(mutate func(*config.AppConfig)) (config.UpdateResult, error) {
			h.add("update:start")
			res, err := h.store.updateChecked(mutate)
			h.add("update:end")
			return res, err
		},
		validateDir: func(dir string) error {
			h.add("preflight:" + dir)
			h.validates++
			return h.validateErr
		},
		restartAria2: func(cfg *config.AppConfig) error {
			h.add("restart")
			h.restarts++
			if cfg != nil {
				h.lastRestart = *cfg
			}
			if h.restarts == 1 {
				return h.restartErr
			}
			return h.rollbackRestartErr
		},
		stopAria2: func() {
			h.add("stop")
			h.stops++
		},
		afterDaemonStop: func() {
			h.add("port-wait")
			h.portWaits++
		},
		rpcInit: func(port, secret string) {
			h.add("rpc")
			h.rpcInits++
			h.lastRPCPort = port
		},
		waitForReady: func(time.Duration) error {
			h.add("ready")
			if h.restarts == 1 {
				return h.readyErr
			}
			return h.rollbackReady
		},
		initNotifier: func(hub *events.Hub, port, secret string) {
			h.add("notifier")
			h.notifiers++
			h.lastNotifierPort = port
		},
		stopNotifier: func() {
			h.add("stop-notifier")
			h.notifierStops++
		},
		extensionStatus: func() (int, bool) {
			h.add("extension")
			return h.extPort, h.extListening
		},
	}
	return a
}

func TestSaveConfig_ExactNoOp(t *testing.T) {
	h := &saveHarness{store: fakeConfigStore{cur: sampleCanonicalConfig()}}
	app := h.app()
	res := app.SaveConfig(h.store.cur)
	if !res.Success || res.Aria2Restarted {
		t.Fatalf("no-op result %+v", res)
	}
	for _, s := range h.steps() {
		if strings.HasPrefix(s, "preflight") || s == "update:start" || s == "restart" || s == "rpc" || s == "notifier" {
			t.Fatalf("no-op invoked %s: %v", s, h.steps())
		}
	}
}

func TestSaveConfig_CanonicalOnlyDiffNoRestart(t *testing.T) {
	h := &saveHarness{store: fakeConfigStore{cur: sampleCanonicalConfig()}}
	req := h.store.cur
	req.CloseToTray = true
	res := h.app().SaveConfig(req)
	if !res.Success || res.Aria2Restarted {
		t.Fatalf("%+v", res)
	}
	if h.store.updates != 1 {
		t.Fatal("canonical-only diff must persist")
	}
	if h.restarts != 0 {
		t.Fatal("must not restart")
	}
}

func TestSaveConfig_NonAriaFieldsNoRestart(t *testing.T) {
	base := sampleCanonicalConfig()
	cases := []config.AppConfig{}
	a := base
	a.WindowTransparency = "mica"
	cases = append(cases, a)
	b := base
	b.SmartThreadMode = false
	cases = append(cases, b)
	c := base
	c.ConvergenceInterval = 10
	cases = append(cases, c)
	d := base
	d.ExtensionWSPort = 16802
	cases = append(cases, d)
	for i, req := range cases {
		h := &saveHarness{store: fakeConfigStore{cur: base}}
		res := h.app().SaveConfig(req)
		if !res.Success || res.Aria2Restarted {
			t.Fatalf("case %d: %+v", i, res)
		}
		if h.restarts != 0 {
			t.Fatalf("case %d restarted", i)
		}
	}
}

func TestSaveConfig_MaxConnections64To128NoRestart(t *testing.T) {
	cur := sampleCanonicalConfig()
	cur.MaxConnections = "64"
	h := &saveHarness{store: fakeConfigStore{cur: cur}}
	req := cur
	req.MaxConnections = "128"
	res := h.app().SaveConfig(req)
	if !res.Success || res.Aria2Restarted {
		t.Fatalf("%+v", res)
	}
	if h.store.cur.MaxConnections != "128" {
		t.Fatal("did not persist")
	}
}

func TestSaveConfig_MaxConnections8To32RestartsAt16(t *testing.T) {
	cur := sampleCanonicalConfig()
	cur.MaxConnections = "8"
	h := &saveHarness{store: fakeConfigStore{cur: cur}}
	req := cur
	req.MaxConnections = "32"
	res := h.app().SaveConfig(req)
	if !res.Success || !res.Aria2Restarted {
		t.Fatalf("%+v", res)
	}
	if h.lastRestart.MaxConnections != "32" {
		t.Fatalf("restart cfg connections %q", h.lastRestart.MaxConnections)
	}
	if process.EffectiveAria2MaxConnections(h.lastRestart.MaxConnections) != 16 {
		t.Fatal("effective must be 16")
	}
}

func TestSaveConfig_SmartToggleRestartFollowsEffective(t *testing.T) {
	base := sampleCanonicalConfig()
	off := func(raw string) config.AppConfig {
		c := base
		c.SmartThreadMode = false
		c.MaxConcurrentDownloads = raw
		return c
	}
	on := func(raw string) config.AppConfig {
		c := base
		c.MaxConcurrentDownloads = raw
		return c
	}
	cases := []struct {
		name        string
		cur         config.AppConfig
		mutate      func(*config.AppConfig)
		wantRestart bool
		wantRaw     string
		wantSmart   bool
	}{
		{
			name: "off raw5 to smart keeps effective 5",
			cur:  off("5"),
			mutate: func(c *config.AppConfig) {
				c.SmartThreadMode = true
			},
			wantRestart: false, wantRaw: "5", wantSmart: true,
		},
		{
			name: "smart raw5 to off keeps effective 5",
			cur:  on("5"),
			mutate: func(c *config.AppConfig) {
				c.SmartThreadMode = false
			},
			wantRestart: false, wantRaw: "5", wantSmart: false,
		},
		{
			name: "smart raw8 to raw12 stays effective 5",
			cur:  on("8"),
			mutate: func(c *config.AppConfig) {
				c.MaxConcurrentDownloads = "12"
			},
			wantRestart: false, wantRaw: "12", wantSmart: true,
		},
		{
			name: "off raw8 to smart restarts effective 8 to 5",
			cur:  off("8"),
			mutate: func(c *config.AppConfig) {
				c.SmartThreadMode = true
			},
			wantRestart: true, wantRaw: "8", wantSmart: true,
		},
		{
			name: "smart raw8 to off restarts effective 5 to 8",
			cur:  on("8"),
			mutate: func(c *config.AppConfig) {
				c.SmartThreadMode = false
			},
			wantRestart: true, wantRaw: "8", wantSmart: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &saveHarness{store: fakeConfigStore{cur: tc.cur}}
			req := tc.cur
			tc.mutate(&req)
			res := h.app().SaveConfig(req)
			if !res.Success || res.Aria2Restarted != tc.wantRestart {
				t.Fatalf("%+v", res)
			}
			wantRestarts := 0
			if tc.wantRestart {
				wantRestarts = 1
			}
			if h.restarts != wantRestarts {
				t.Fatalf("restarts = %d, want %d", h.restarts, wantRestarts)
			}
			if h.store.cur.MaxConcurrentDownloads != tc.wantRaw || h.store.cur.SmartThreadMode != tc.wantSmart {
				t.Fatalf("store raw=%q smart=%v", h.store.cur.MaxConcurrentDownloads, h.store.cur.SmartThreadMode)
			}
			if res.Config.MaxConcurrentDownloads != tc.wantRaw {
				t.Fatalf("result raw %q, want %q", res.Config.MaxConcurrentDownloads, tc.wantRaw)
			}
			if tc.wantRestart && h.lastRestart.MaxConcurrentDownloads != tc.wantRaw {
				t.Fatalf("restart cfg raw %q, want %q (raw must survive)", h.lastRestart.MaxConcurrentDownloads, tc.wantRaw)
			}
		})
	}
}

func TestSaveConfig_SmartToggleReadinessFailureRestoresRawAndFlag(t *testing.T) {
	cur := sampleCanonicalConfig()
	cur.SmartThreadMode = false
	cur.MaxConcurrentDownloads = "8"
	h := &saveHarness{store: fakeConfigStore{cur: cur}, readyErr: errors.New("not ready")}
	req := cur
	req.SmartThreadMode = true
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeAriaReadinessRolledBack {
		t.Fatalf("%+v", res)
	}
	if h.store.cur.SmartThreadMode || h.store.cur.MaxConcurrentDownloads != "8" {
		t.Fatalf("rollback must restore off/raw8, got smart=%v raw=%q",
			h.store.cur.SmartThreadMode, h.store.cur.MaxConcurrentDownloads)
	}
	if res.Config.SmartThreadMode || res.Config.MaxConcurrentDownloads != "8" {
		t.Fatalf("result must be old off/raw8, got smart=%v raw=%q",
			res.Config.SmartThreadMode, res.Config.MaxConcurrentDownloads)
	}
}

func TestSaveConfig_DirectoryPreflightBeforeCommit(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}, validateErr: errors.New("offline")}
	req := cur
	req.DownloadDir = `Z:\offline`
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeDownloadDirUnavailable {
		t.Fatalf("%+v", res)
	}
	if h.store.updates != 0 {
		t.Fatal("preflight failure must not persist")
	}
	if h.restarts != 0 {
		t.Fatal("must not restart")
	}
}

func TestSaveConfig_UnchangedDirSkipsPreflight(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}, validateErr: errors.New("offline")}
	req := cur
	req.WindowTransparency = "mica"
	res := h.app().SaveConfig(req)
	if !res.Success {
		t.Fatalf("unrelated save blocked by old dir: %+v", res)
	}
	if h.validates != 0 {
		t.Fatal("must not preflight unchanged dir")
	}
}

func TestSaveConfig_LiveExtensionConflict(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}, extPort: 16801, extListening: true}
	req := cur
	req.RPCPort = "16801"
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeRPCExtensionPort {
		t.Fatalf("%+v", res)
	}
	if h.store.updates != 0 {
		t.Fatal("conflict must not persist")
	}
}

func TestSaveConfig_PersistFailure(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur, persistErr: errors.New("disk")}}
	req := cur
	req.UserAgent = "new-agent"
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodePersistFailed {
		t.Fatalf("%+v", res)
	}
	if h.store.cur.UserAgent != cur.UserAgent {
		t.Fatal("published on persist failure")
	}
	if h.restarts != 0 {
		t.Fatal("restarted on persist failure")
	}
	if strings.Contains(res.Message, cur.RPCSecret) {
		t.Fatal("message leaked secret")
	}
}

func TestSaveConfig_RestartFailureRollsBack(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}, restartErr: errors.New("start fail")}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.Aria2Restarted || res.ErrorCode != errCodeAriaRestartRolledBack {
		t.Fatalf("%+v", res)
	}
	if h.store.cur.RPCPort != cur.RPCPort {
		t.Fatalf("config not rolled back: %q", h.store.cur.RPCPort)
	}
	if h.restarts < 2 {
		t.Fatal("expected restore restart after a failed candidate restart")
	}
	if h.notifiers > 1 {
		t.Fatalf("notifier calls %d", h.notifiers)
	}
}

func TestSaveConfig_ReadinessFailureRollsBackNoEarlyNotifier(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}, readyErr: errors.New("not ready")}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.Aria2Restarted || res.ErrorCode != errCodeAriaReadinessRolledBack {
		t.Fatalf("%+v", res)
	}
	readies := 0
	for _, s := range h.steps() {
		if s == "ready" {
			readies++
		}
		if s == "notifier" && readies < 2 {
			t.Fatalf("candidate notifier before rollback readiness: %v", h.steps())
		}
	}
	if readies < 2 {
		t.Fatal("expected candidate then restore readiness checks")
	}
	if h.store.cur.RPCPort != cur.RPCPort {
		t.Fatal("not rolled back")
	}
}

func TestSaveConfig_ReadyFailRollbackRestartFailRebindsRPC(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store:              fakeConfigStore{cur: cur},
		readyErr:           errors.New("not ready"),
		rollbackRestartErr: errors.New("restore start fail"),
	}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.Aria2Restarted || res.ErrorCode != errCodeAriaRollbackFailed {
		t.Fatalf("%+v", res)
	}
	if res.Config.RPCPort != cur.RPCPort {
		t.Fatalf("config %q, want old", res.Config.RPCPort)
	}
	if h.lastRPCPort != cur.RPCPort {
		t.Fatalf("last rpcInit %q, want old %q", h.lastRPCPort, cur.RPCPort)
	}
}

func TestSaveConfig_ReadyFailRestoreStoppedFalseStopsCandidate(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store:    fakeConfigStore{cur: cur},
		readyErr: errors.New("not ready"),
		rollbackRestartErr: &process.Aria2RestartError{
			Err:     errors.New("old download dir offline"),
			Stopped: false,
		},
	}
	req := cur
	req.DownloadDir = `C:\new-downloads`
	res := h.app().SaveConfig(req)
	if res.Success || res.Aria2Restarted || res.ErrorCode != errCodeAriaRollbackFailed {
		t.Fatalf("%+v", res)
	}
	if h.restarts != 2 {
		t.Fatalf("candidate then restore restart, got %d", h.restarts)
	}
	if h.stops != 1 {
		t.Fatalf("leftover candidate must be stopped, stops=%d", h.stops)
	}
	if h.portWaits != 1 {
		t.Fatalf("port-release wait after leftover stop, waits=%d", h.portWaits)
	}
	if h.lastRPCPort != cur.RPCPort {
		t.Fatalf("rpc rebound %q, want old", h.lastRPCPort)
	}
	if res.Config.DownloadDir != cur.DownloadDir {
		t.Fatalf("config %q, want old dir", res.Config.DownloadDir)
	}
}

func TestSaveConfig_RestartFailBeforeStopSkipsRestoreRestart(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store: fakeConfigStore{cur: cur},
		restartErr: &process.Aria2RestartError{
			Err:     errors.New("download dir unavailable"),
			Stopped: false,
		},
		readyErr: errors.New("should not wait"),
	}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.Aria2Restarted || res.ErrorCode != errCodeAriaRestartRolledBack {
		t.Fatalf("%+v", res)
	}
	if h.restarts != 1 {
		t.Fatalf("restore restart should be skipped, restarts=%d", h.restarts)
	}
	if h.lastRPCPort != cur.RPCPort {
		t.Fatalf("rpc rebound %q, want old", h.lastRPCPort)
	}
	if h.stops != 0 {
		t.Fatalf("old daemon was never killed, unexpected stop: %d", h.stops)
	}
	if h.notifierStops != 0 {
		t.Fatalf("skip-restore must not stop notifier, got %d", h.notifierStops)
	}
	if h.store.cur.RPCPort != cur.RPCPort {
		t.Fatal("config not rolled back")
	}
	for _, s := range h.steps() {
		if s == "ready" {
			t.Fatalf("skip-restore must not WaitForReady: %v", h.steps())
		}
	}
}

func TestSaveConfig_RollbackPersistFailureSkipRestoreKeepsNotifier(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store: fakeConfigStore{cur: cur, rollbackErr: errors.New("rollback disk")},
		restartErr: &process.Aria2RestartError{
			Err:     errors.New("download dir unavailable"),
			Stopped: false,
		},
	}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeConfigRollbackFailed {
		t.Fatalf("%+v", res)
	}
	if h.notifierStops != 0 {
		t.Fatalf("live daemon must keep notifier, got %d", h.notifierStops)
	}
}

func TestSaveConfig_RollbackPersistFailureAfterReadyStopsNotifier(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store:    fakeConfigStore{cur: cur, rollbackErr: errors.New("rollback disk")},
		readyErr: errors.New("not ready"),
	}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeConfigRollbackFailed {
		t.Fatalf("%+v", res)
	}
	if h.notifierStops != 1 {
		t.Fatalf("replaced daemon should stop notifier, got %d", h.notifierStops)
	}
}

func TestSaveConfig_RollbackPersistFailureKeepsCandidate(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store:      fakeConfigStore{cur: cur, rollbackErr: errors.New("rollback disk")},
		restartErr: errors.New("start fail"),
	}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeConfigRollbackFailed {
		t.Fatalf("%+v", res)
	}
	if res.Config.RPCPort != "16810" {
		t.Fatalf("must return candidate, got %q", res.Config.RPCPort)
	}
	if h.store.cur.RPCPort != "16810" {
		t.Fatal("current must remain candidate")
	}
	if h.notifierStops != 1 {
		t.Fatalf("rollback persist fail should stop notifier, got %d", h.notifierStops)
	}
}

func TestSaveConfig_RollbackDaemonFailureReturnsOldConfig(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{
		store:              fakeConfigStore{cur: cur},
		restartErr:         errors.New("fail"),
		rollbackRestartErr: errors.New("fail"),
	}
	req := cur
	req.RPCPort = "16810"
	res := h.app().SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeAriaRollbackFailed {
		t.Fatalf("%+v", res)
	}
	if res.Config.RPCPort != cur.RPCPort {
		t.Fatalf("want old snapshot, got %q", res.Config.RPCPort)
	}
	if h.lastRPCPort != cur.RPCPort {
		t.Fatalf("rpc rebound %q, want old", h.lastRPCPort)
	}
}

func TestSaveConfig_PreservesRotatedExtensionSecret(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}}
	rotated := "rotated-secret-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	app := h.app()
	origUpdate := app.configDeps.updateChecked
	app.configDeps.updateChecked = func(mutate func(*config.AppConfig)) (config.UpdateResult, error) {
		h.store.mu.Lock()
		h.store.cur.ExtensionSecret = rotated
		h.store.mu.Unlock()
		return origUpdate(mutate)
	}
	req := cur
	req.UserAgent = "after-rotate"
	req.ExtensionSecret = "frontend-must-not-win"
	res := app.SaveConfig(req)
	if !res.Success {
		t.Fatalf("%+v", res)
	}
	if h.store.cur.ExtensionSecret != rotated {
		t.Fatalf("secret overwritten: %q", h.store.cur.ExtensionSecret)
	}
	if res.Config.ExtensionSecret != rotated {
		t.Fatalf("result snapshot missing rotated secret: %q", res.Config.ExtensionSecret)
	}
}

func TestSaveConfig_ConcurrentSerialized(t *testing.T) {
	cur := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: cur}}
	app := h.app()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		req := cur
		req.WindowTransparency = "mica"
		_ = app.SaveConfig(req)
	}()
	go func() {
		defer wg.Done()
		req := cur
		req.SmartThreadMode = false
		_ = app.SaveConfig(req)
	}()
	wg.Wait()
	steps := h.steps()
	depth := 0
	for _, s := range steps {
		switch s {
		case "update:start":
			if depth != 0 {
				t.Fatalf("interleaved updates: %v", steps)
			}
			depth++
		case "update:end":
			if depth != 1 {
				t.Fatalf("unbalanced update end: %v", steps)
			}
			depth--
		case "restart", "rpc":
			t.Fatalf("unexpected aria activation in non-aria saves: %v", steps)
		}
	}
	if depth != 0 {
		t.Fatalf("unclosed update: %v", steps)
	}
}

func TestSaveConfig_ErrorMessageOmitsSecret(t *testing.T) {
	cur := sampleCanonicalConfig()
	cur.RPCSecret = "super-secret-value"
	h := &saveHarness{store: fakeConfigStore{cur: cur, persistErr: errors.New("disk")}}
	req := cur
	req.UserAgent = "x"
	res := h.app().SaveConfig(req)
	if strings.Contains(res.Message, "super-secret-value") {
		t.Fatalf("leaked secret: %q", res.Message)
	}
}

func TestSaveConfig_NilCurrentUsesNotLoaded(t *testing.T) {
	app := &App{}
	app.configDeps = configSaveDeps{
		get: func() *config.AppConfig { return nil },
	}
	res := app.SaveConfig(sampleCanonicalConfig())
	if res.Success || res.ErrorCode != errCodeNotLoaded {
		t.Fatalf("%+v", res)
	}
}

func TestAriaStartup_ReadyCompletionInitsNotifierOnce(t *testing.T) {
	h := &saveHarness{store: fakeConfigStore{cur: sampleCanonicalConfig()}}
	app := h.app()
	launch := sampleCanonicalConfig()
	id := RegisterAriaStartup(app)
	CompleteAriaStartup(app, &events.Hub{}, id, launch, nil)
	if h.notifiers != 1 || h.lastNotifierPort != launch.RPCPort {
		t.Fatalf("notifiers=%d port=%q", h.notifiers, h.lastNotifierPort)
	}
}

func TestAriaStartup_FailedReadinessSkipsNotifier(t *testing.T) {
	h := &saveHarness{store: fakeConfigStore{cur: sampleCanonicalConfig()}}
	app := h.app()
	id := RegisterAriaStartup(app)
	CompleteAriaStartup(app, &events.Hub{}, id, sampleCanonicalConfig(), errors.New("not ready"))
	if h.notifiers != 0 {
		t.Fatalf("notifiers=%d", h.notifiers)
	}
}

func TestAriaStartup_SameEffectiveSaveKeepsLaunchAttempt(t *testing.T) {
	launch := sampleCanonicalConfig() // smart on, raw 5
	h := &saveHarness{store: fakeConfigStore{cur: launch}}
	app := h.app()
	id := RegisterAriaStartup(app)
	req := launch
	req.SmartThreadMode = false // effective stays 5
	res := app.SaveConfig(req)
	if !res.Success || res.Aria2Restarted {
		t.Fatalf("%+v", res)
	}
	CompleteAriaStartup(app, &events.Hub{}, id, launch, nil)
	if h.notifiers != 1 {
		t.Fatalf("startup completion wrongly invalidated, notifiers=%d", h.notifiers)
	}
	if h.store.cur.SmartThreadMode != false {
		t.Fatal("completion must not write back launch-era raw values")
	}
}

func TestAriaStartup_PreflightOrPersistFailureKeepsAttempt(t *testing.T) {
	launch := sampleCanonicalConfig()
	cases := []struct {
		name   string
		h      *saveHarness
		mutate func(*config.AppConfig)
	}{
		{
			name: "preflight",
			h: &saveHarness{
				store:       fakeConfigStore{cur: launch},
				validateErr: errors.New("offline"),
			},
			mutate: func(c *config.AppConfig) { c.DownloadDir = `Z:\offline` },
		},
		{
			name: "persist",
			h: &saveHarness{
				store: fakeConfigStore{cur: launch, persistErr: errors.New("disk")},
			},
			mutate: func(c *config.AppConfig) { c.UserAgent = "x" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.h.app()
			id := RegisterAriaStartup(app)
			req := launch
			tc.mutate(&req)
			res := app.SaveConfig(req)
			if res.Success {
				t.Fatalf("%+v", res)
			}
			CompleteAriaStartup(app, &events.Hub{}, id, launch, nil)
			if tc.h.notifiers != 1 {
				t.Fatalf("failed save must not invalidate launch attempt, notifiers=%d", tc.h.notifiers)
			}
		})
	}
}

func TestAriaStartup_NewActivationRejectsStaleCompletion(t *testing.T) {
	launch := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: launch}}
	app := h.app()
	id := RegisterAriaStartup(app)
	req := launch
	req.RPCPort = "16810"
	res := app.SaveConfig(req)
	if !res.Success || !res.Aria2Restarted {
		t.Fatalf("%+v", res)
	}
	CompleteAriaStartup(app, &events.Hub{}, id, launch, nil)
	if h.notifiers != 1 {
		t.Fatalf("stale completion must not rebuild notifier, notifiers=%d", h.notifiers)
	}
	if h.lastNotifierPort != "16810" {
		t.Fatalf("notifier bound to %q, want new endpoint 16810", h.lastNotifierPort)
	}
}

func TestAriaStartup_RollbackToSameProjectionStillRejects(t *testing.T) {
	launch := sampleCanonicalConfig()
	h := &saveHarness{
		store:      fakeConfigStore{cur: launch},
		restartErr: errors.New("start fail"),
	}
	app := h.app()
	id := RegisterAriaStartup(app)
	req := launch
	req.RPCPort = "16810"
	res := app.SaveConfig(req)
	if res.Success || res.ErrorCode != errCodeAriaRestartRolledBack {
		t.Fatalf("%+v", res)
	}
	CompleteAriaStartup(app, &events.Hub{}, id, launch, nil)
	if h.notifiers != 1 {
		t.Fatalf("stale completion must stay rejected after rollback, notifiers=%d", h.notifiers)
	}
	if h.lastNotifierPort != launch.RPCPort {
		t.Fatalf("notifier bound to %q, want restored endpoint", h.lastNotifierPort)
	}
}

func TestAriaStartup_CompletionSerializedWithActivation(t *testing.T) {
	launch := sampleCanonicalConfig()
	h := &saveHarness{store: fakeConfigStore{cur: launch}}
	app := h.app()
	enteredRestart := make(chan struct{})
	releaseRestart := make(chan struct{})
	origRestart := app.configDeps.restartAria2
	app.configDeps.restartAria2 = func(cfg *config.AppConfig) error {
		close(enteredRestart)
		<-releaseRestart
		return origRestart(cfg)
	}
	id := RegisterAriaStartup(app)

	saveDone := make(chan SaveConfigResult, 1)
	go func() {
		req := launch
		req.RPCPort = "16810"
		saveDone <- app.SaveConfig(req)
	}()
	<-enteredRestart // SaveConfig holds configSaveMu inside the restart call

	completeDone := make(chan struct{})
	go func() {
		CompleteAriaStartup(app, &events.Hub{}, id, launch, nil)
		close(completeDone)
	}()
	close(releaseRestart)
	res := <-saveDone
	<-completeDone

	if !res.Success || !res.Aria2Restarted {
		t.Fatalf("%+v", res)
	}
	if h.notifiers != 1 {
		t.Fatalf("only the current activation may init the notifier, got %d", h.notifiers)
	}
	if h.lastNotifierPort != "16810" {
		t.Fatalf("old endpoint rebuilt, notifier port %q", h.lastNotifierPort)
	}
}
