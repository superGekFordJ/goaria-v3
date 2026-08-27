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

func TestRequiresAppRestart_Matrix(t *testing.T) {
	base := sampleCanonicalConfig()
	type row struct {
		mut  func(*config.AppConfig)
		want bool
	}
	rows := []row{
		{func(c *config.AppConfig) { c.ShowHistory = false }, false},
		{func(c *config.AppConfig) { c.CloseToTray = true }, false},
		{func(c *config.AppConfig) { c.SmartThreadMode = false }, false},
		{func(c *config.AppConfig) { c.MinThreadLife = 9 }, false},
		{func(c *config.AppConfig) { c.WindowTransparency = "acrylic" }, true},
		{func(c *config.AppConfig) { c.MaxConnections = "64" }, true},
		{func(c *config.AppConfig) { c.MaxConnections = "32" }, true},
		{func(c *config.AppConfig) { c.ConvergenceInterval = 10 }, true},
		{func(c *config.AppConfig) { c.MaxConcurrentDownloads = "8" }, true},
		{func(c *config.AppConfig) { c.RPCPort = "16810" }, false},
		{func(c *config.AppConfig) { c.RPCSecret = "n" }, false},
		{func(c *config.AppConfig) { c.DownloadDir = `E:\dl` }, false},
		{func(c *config.AppConfig) { c.UserAgent = "x" }, false},
		{func(c *config.AppConfig) { c.ExtensionEnabled = false }, true},
		{func(c *config.AppConfig) { c.ExtensionWSPort = 16802 }, true},
	}
	for i, r := range rows {
		next := base
		r.mut(&next)
		next = config.ValidateAndSanitize(next)
		if got := requiresAppRestart(base, next); got != r.want {
			t.Errorf("row %d: got %v want %v", i, got, r.want)
		}
	}
	conc := base
	conc.MaxConcurrentDownloads = "8"
	if !requiresAppRestart(base, conc) {
		t.Fatal("MaxConcurrentDownloads requires app restart")
	}
	if ariaProjection(base) == ariaProjection(conc) {
		t.Fatal("MaxConcurrentDownloads must also change Aria projection")
	}
}

func TestSaveConfigResult_ValueSnapshot(t *testing.T) {
	cfg := sampleCanonicalConfig()
	res := successSaveResult(cfg, true, true)
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
	extPort            int
	extListening       bool
	lastRestart        config.AppConfig
	lastRPCPort        string
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
	if !res.RequiresAppRestart {
		t.Fatal("64→128 requires app restart")
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
	if h.store.cur.RPCPort != cur.RPCPort {
		t.Fatal("config not rolled back")
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
