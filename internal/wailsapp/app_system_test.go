package wailsapp

import "testing"

func TestIsValidPairingURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "valid URL on fallback port",
			url:  "http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc",
			want: true,
		},
		{
			name: "valid URL on another fallback port",
			url:  "http://127.0.0.1:16814/__goaria_pair__/pair.html?n=xyz",
			want: true,
		},
		{
			name: "https scheme rejected",
			url:  "https://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc",
			want: false,
		},
		{
			name: "wrong host rejected",
			url:  "http://evil.com:16810/__goaria_pair__/pair.html?n=abc",
			want: false,
		},
		{
			name: "wrong port rejected",
			url:  "http://127.0.0.1:9999/__goaria_pair__/pair.html?n=abc",
			want: false,
		},
		{
			name: "wrong path rejected",
			url:  "http://127.0.0.1:16810/evil/path?n=abc",
			want: false,
		},
		{
			name: "malformed URL rejected",
			url:  "://not-a-url",
			want: false,
		},
		{
			name: "arbitrary external URL rejected",
			url:  "https://evil.com/exploit?cmd=rm -rf /",
			want: false,
		},
		{
			name: "userinfo injection rejected",
			url:  "http://evil&calc@127.0.0.1:16810/__goaria_pair__/pair.html?n=abc",
			want: false,
		},
		{
			name: "pipe metacharacter in query rejected",
			url:  "http://127.0.0.1:16810/__goaria_pair__/pair.html?n=a|b",
			want: false,
		},
		{
			name: "normal valid URL accepted",
			url:  "http://127.0.0.1:16810/__goaria_pair__/pair.html?n=normal",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidPairingURL(tt.url)
			if got != tt.want {
				t.Fatalf("isValidPairingURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
