package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAnalyzeValidJSONLReturnsCountOnlySummary(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"category":"session_opened"}`,
		`{"category":"preflight_accepted"}`,
		`{"category":"post_accepted"}`,
		`{"category":"terminal_success"}`,
		`{"category":"session_closed"}`,
		`{"category":"post_accepted"}`,
	}, "\n"))
	var output bytes.Buffer

	if err := run([]string{"analyze"}, input, &output); err != nil {
		t.Fatalf("run(analyze) error = %v", err)
	}

	var summary struct {
		SchemaVersion int            `json:"schema_version"`
		EventsTotal   int            `json:"events_total"`
		Categories    map[string]int `json:"categories"`
	}
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatalf("summary is not JSON: %v", err)
	}
	if summary.SchemaVersion != probeSummarySchemaVersion || summary.EventsTotal != 6 || summary.Categories["post_accepted"] != 2 || summary.Categories["terminal_success"] != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	assertNoProbeRawText(t, output.String(), "fixture.invalid", "apr-alpha001", "xpk-alpha001", "authorization", "cookie", "raw-token", "secret")
}

func TestAnalyzeRejectsUnknownCategoryWithGenericError(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"analyze"}, strings.NewReader(`{"category":"unknown_category"}`), &output)
	if err == nil {
		t.Fatal("run(analyze) error = nil, want unknown category failure")
	}
	if !strings.Contains(err.Error(), "diagnostic category is invalid") {
		t.Fatalf("error = %q, want generic category error", err.Error())
	}
	assertNoProbeRawText(t, err.Error(), "unknown_category", "fixture.invalid", "raw-token")
}

func TestAnalyzeRejectsRawLookingFieldsWithoutEcho(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "url field", input: `{"category":"post_accepted","url":"https://fixture.invalid/callback?token=raw-token"}`},
		{name: "header field", input: `{"category":"post_accepted","header":"Authorization: Bearer raw-token"}`},
		{name: "body field", input: `{"category":"post_accepted","body":"secret=raw-token"}`},
		{name: "session field", input: `{"category":"post_accepted","session":"synthetic-session-token"}`},
		{name: "source field", input: `{"category":"post_accepted","source":"xpk-alpha001"}`},
		{name: "path field", input: `{"category":"post_accepted","path":"C:/tmp/synthetic"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := run([]string{"analyze"}, strings.NewReader(tt.input), &output)
			if err == nil {
				t.Fatal("run(analyze) error = nil, want raw-looking field failure")
			}
			if !strings.Contains(err.Error(), "diagnostic event is invalid") {
				t.Fatalf("error = %q, want generic event error", err.Error())
			}
			assertNoProbeRawText(t, err.Error(), "fixture.invalid", "raw-token", "synthetic-session-token", "xpk-alpha001", "Authorization", "C:/tmp/synthetic")
		})
	}
}

func TestAnalyzeRejectsMalformedJSON(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"analyze"}, strings.NewReader(`{"category":"post_accepted"`), &output)
	if err == nil {
		t.Fatal("run(analyze) error = nil, want malformed JSON failure")
	}
	if err.Error() != "diagnostic event is invalid" {
		t.Fatalf("error = %q, want generic invalid event", err.Error())
	}
}

func assertNoProbeRawText(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(value)) {
			t.Fatalf("text leaked %q: %s", value, text)
		}
	}
}
