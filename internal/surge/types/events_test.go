package types

import (
	"encoding/json"
	"testing"
)

func TestEventTypeToString_FirstByte(t *testing.T) {
	if got := EventTypeToString(EventFirstByte); got != EventTypeFirstByte {
		t.Fatalf("EventTypeToString(EventFirstByte) = %q, want %q", got, EventTypeFirstByte)
	}
	if EventTypeFirstByte != "first_byte" {
		t.Fatalf("EventTypeFirstByte = %q, want first_byte", EventTypeFirstByte)
	}
}

func TestDownloadEvent_TTFBMsJSONRoundTrip(t *testing.T) {
	orig := DownloadEvent{
		Type:       EventFirstByte,
		DownloadID: "dl-1",
		TTFBMs:     42,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got DownloadEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.TTFBMs != 42 {
		t.Fatalf("TTFBMs = %d, want 42", got.TTFBMs)
	}
	if got.Type != EventFirstByte {
		t.Fatalf("Type = %v, want EventFirstByte", got.Type)
	}
}
