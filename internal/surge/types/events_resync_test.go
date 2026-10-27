package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// ResyncRequired rides the System section of DownloadEvent in snake_case and
// survives a JSON round-trip; when unset it must stay omitted.
func TestDownloadEvent_ResyncRequiredJSONRoundTrip(t *testing.T) {
	orig := DownloadEvent{
		Type:           EventSystem,
		DownloadID:     "dl-1",
		Message:        "live updates fell behind",
		ResyncRequired: true,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"resync_required":true`) {
		t.Fatalf("missing resync_required in %s", data)
	}
	var back DownloadEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.ResyncRequired || back.Message != orig.Message || back.Type != EventSystem {
		t.Fatalf("round trip = %+v, want %+v", back, orig)
	}
}

func TestDownloadEvent_ResyncRequiredOmittedWhenFalse(t *testing.T) {
	data, err := json.Marshal(DownloadEvent{Type: EventSystem, Message: "plain"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "resync_required") {
		t.Fatalf("resync_required not omitted in %s", data)
	}
}
