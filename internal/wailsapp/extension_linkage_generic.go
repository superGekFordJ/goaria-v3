//go:build !extractor

package wailsapp

import "goaria-v3/internal/extension"

func ConfigureExtensionLinkage(app *App, srv *extension.Server) {
	if app == nil || srv == nil {
		return
	}
	srv.SetLinkage(attachDirectBatchCommitter(extension.Linkage{}, app))
}
