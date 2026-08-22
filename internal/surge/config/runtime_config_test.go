package config

import (
	"reflect"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// TestToRuntimeConfig_AllFieldsExplicit is the test-period safety net
// described in upstream_engine_management.md §3.5.
//
// It sets every Setting to a non-default (non-zero) value, calls
// ToRuntimeConfig(), and verifies that every exported field of
// types.RuntimeConfig is non-zero. If upstream adds a new RuntimeConfig
// field and the adapter layer doesn't explicitly assign it, the field
// will be zero-valued and this test will fail.
func TestToRuntimeConfig_AllFieldsExplicit(t *testing.T) {
	s := DefaultSettings()

	s.Network.MaxConnectionsPerDownload.Value = 16
	s.Network.UserAgent.Value = "test-agent"
	s.Network.ProxyURL.Value = "http://proxy:8080"
	s.Network.CustomDNS.Value = "8.8.8.8"
	s.Network.SequentialDownload.Value = true
	s.Network.MinChunkSize.Value = int64(4 * 1024 * 1024)
	s.Network.WorkerBufferSize.Value = 256 * 1024
	s.Network.DialHedgeCount.Value = 8
	s.Network.GlobalRateLimit.Value = "10MB/s"
	s.Network.DefaultDownloadRateLimit.Value = "2MB/s"

	s.Performance.MaxTaskRetries.Value = 5
	s.Performance.SlowWorkerThreshold.Value = 0.5
	s.Performance.SlowWorkerGracePeriod.Value = 10 * time.Second
	s.Performance.StallTimeout.Value = 5 * time.Second
	s.Performance.SpeedEmaAlpha.Value = 0.5

	rc := s.ToRuntimeConfig()
	rcVal := reflect.ValueOf(rc).Elem()
	rcType := rcVal.Type()

	for i := 0; i < rcType.NumField(); i++ {
		field := rcType.Field(i)
		if !field.IsExported() {
			continue
		}
		// Workers is a per-task override field; 0 = "use √size heuristic" is the
		// correct global default, so it is intentionally zero-valued here.
		if field.Name == "Workers" {
			continue
		}
		if rcVal.Field(i).IsZero() {
			t.Errorf("RuntimeConfig field %q is zero-valued after ToRuntimeConfig() "+
				"with all settings set to non-default values; the adapter layer "+
				"likely needs to explicitly assign this field "+
				"(see upstream_engine_management.md §3.5)", field.Name)
		}
	}
}

// TestToRuntimeConfig_RateLimitDefaults verifies rate limit fields default
// to 0 (unlimited) when settings are at their default values.
func TestToRuntimeConfig_RateLimitDefaults(t *testing.T) {
	s := DefaultSettings()
	rc := s.ToRuntimeConfig()
	if rc.GlobalRateLimitBps != 0 {
		t.Errorf("GlobalRateLimitBps = %d, want 0 for default", rc.GlobalRateLimitBps)
	}
	if rc.DefaultDownloadRateLimitBps != 0 {
		t.Errorf("DefaultDownloadRateLimitBps = %d, want 0 for default", rc.DefaultDownloadRateLimitBps)
	}
}

// TestToRuntimeConfig_RateLimitParsed verifies rate limit string values
// are correctly parsed into bytes/sec.
func TestToRuntimeConfig_RateLimitParsed(t *testing.T) {
	s := DefaultSettings()
	s.Network.GlobalRateLimit.Value = "10MB/s"
	s.Network.DefaultDownloadRateLimit.Value = "2MB/s"
	rc := s.ToRuntimeConfig()
	if rc.GlobalRateLimitBps <= 0 {
		t.Errorf("GlobalRateLimitBps = %d, want >0 for 10MB/s", rc.GlobalRateLimitBps)
	}
	if rc.DefaultDownloadRateLimitBps <= 0 {
		t.Errorf("DefaultDownloadRateLimitBps = %d, want >0 for 2MB/s", rc.DefaultDownloadRateLimitBps)
	}
}

// TestRuntimeConfig_FieldCount tracks the expected number of exported
// fields in types.RuntimeConfig. When upstream adds a field, update this
// count and ensure ToRuntimeConfig assigns it.
func TestRuntimeConfig_FieldCount(t *testing.T) {
	rcType := reflect.TypeFor[types.RuntimeConfig]()
	expected := 16
	if got := rcType.NumField(); got != expected {
		t.Errorf("RuntimeConfig has %d fields, expected %d; if upstream added a field, "+
			"update this count and ToRuntimeConfig()", got, expected)
	}
}
