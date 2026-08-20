package extractor

import "testing"

func TestParseHTTPURLHost(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
		wantOK bool
	}{
		{name: "mixed case host", rawURL: "https://ExAmPle.COM/x", want: "example.com", wantOK: true},
		{name: "port stripped", rawURL: "https://example.com:8080/", want: "example.com", wantOK: true},
		{name: "userinfo rejected", rawURL: "https://user:pass@example.com/x", wantOK: false},
		{name: "trailing dot rejected", rawURL: "https://example.com./x", wantOK: false},
		{name: "ftp rejected", rawURL: "ftp://example.com/x", wantOK: false},
		{name: "one-label localhost rejected", rawURL: "http://localhost/x", wantOK: false},
		{name: "percent host rejected", rawURL: "https://ex%61mple.com/x", wantOK: false},
		{name: "ipv4 rejected", rawURL: "http://127.0.0.1/", wantOK: false},
		{name: "bracketed ipv6 without port rejected", rawURL: "http://[::1]/", wantOK: false},
		{name: "ipv6 with port rejected", rawURL: "http://[2001:db8::1]:443/", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseHTTPURLHost(tc.rawURL)
			if ok != tc.wantOK {
				t.Fatalf("ParseHTTPURLHost(%q) ok=%v, want %v (host=%q)", tc.rawURL, ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				if got != "" {
					t.Fatalf("ParseHTTPURLHost(%q) host=%q, want empty on reject", tc.rawURL, got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("ParseHTTPURLHost(%q) = %q, want %q", tc.rawURL, got, tc.want)
			}
		})
	}
}
