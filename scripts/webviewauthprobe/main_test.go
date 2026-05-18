package main

import (
	"bytes"
	"encoding/json"
	"reflect"
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

func TestAnalyzeAcceptsAllAllowedCategories(t *testing.T) {
	var input strings.Builder
	for category := range probeCategories {
		input.WriteString(`{"category":"`)
		input.WriteString(category)
		input.WriteString(`"}`)
		input.WriteByte('\n')
	}

	summary, err := analyze(strings.NewReader(input.String()))
	if err != nil {
		t.Fatalf("analyze() error = %v", err)
	}
	if summary.EventsTotal != len(probeCategories) || !reflect.DeepEqual(summary.Categories, oneCountPerProbeCategory()) {
		t.Fatalf("summary = %#v, want one count per category", summary)
	}
}

func TestAnalyzeRejectsCategoryValueShapeWithGenericError(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		forbidden []string
	}{
		{name: "empty", input: `{"category":""}`},
		{name: "leading whitespace", input: `{"category":" post_accepted"}`},
		{name: "trailing whitespace", input: `{"category":"post_accepted "}`},
		{name: "non string", input: `{"category":7}`},
		{name: "null", input: `{"category":null}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := run([]string{"analyze"}, strings.NewReader(tt.input), &output)
			if err == nil {
				t.Fatal("run(analyze) error = nil, want invalid category failure")
			}
			if err.Error() != "diagnostic category is invalid" {
				t.Fatalf("error = %q, want generic category error", err.Error())
			}
			assertNoProbeRawText(t, err.Error(), "post_accepted", "fixture.invalid", "raw-token")
		})
	}
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
		{name: "source id field", input: `{"category":"post_accepted","source_id":"xpk-alpha001"}`},
		{name: "callback route field", input: `{"category":"post_accepted","callback_route":"/_goaria/auth/callback/synthetic"}`},
		{name: "raw field", input: `{"category":"post_accepted","raw":"raw-token"}`},
		{name: "raw header field", input: `{"category":"post_accepted","raw_header":"Cookie: sid=raw-token"}`},
		{name: "transport field", input: `{"category":"post_accepted","transport":"local_post"}`},
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
			assertNoProbeRawText(t, err.Error(), "fixture.invalid", "raw-token", "synthetic-session-token", "xpk-alpha001", "Authorization", "Cookie", "C:/tmp/synthetic", "/_goaria/auth/callback/synthetic", "local_post")
		})
	}
}

func TestAnalyzeRejectsTrailingJSONWithGenericError(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"analyze"}, strings.NewReader(`{"category":"post_accepted"}{"category":"session_closed"}`), &output)
	if err == nil {
		t.Fatal("run(analyze) error = nil, want trailing JSON failure")
	}
	if err.Error() != "diagnostic event is invalid" {
		t.Fatalf("error = %q, want generic invalid event", err.Error())
	}
	assertNoProbeRawText(t, err.Error(), "post_accepted", "session_closed")
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

func oneCountPerProbeCategory() map[string]int {
	counts := make(map[string]int, len(probeCategories))
	for category := range probeCategories {
		counts[category] = 1
	}

	return counts
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
