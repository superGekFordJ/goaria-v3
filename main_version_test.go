package main

import (
	"testing"

	"goaria-v3/internal/wailsapp"
)

func TestVersionInjection(t *testing.T) {
	app := wailsapp.NewApp(wailsapp.Options{Version: version})
	if got := app.GetAppVersion(); got != version {
		t.Fatalf("GetAppVersion() = %q, want %q", got, version)
	}
}
