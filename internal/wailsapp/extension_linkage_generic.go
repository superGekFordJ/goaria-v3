//go:build !extractor

package wailsapp

import "goaria-v3/internal/extension"

func ConfigureExtensionLinkage(*App, *extension.Server) {}
