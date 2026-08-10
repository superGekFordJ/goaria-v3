//go:build !extractor

package wailsapp

import (
	"errors"
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func HostAuthCallbackMiddleware(appService *App) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}
		return next
	}
}

func HostAuthRawMessageHandler(window application.Window, message string, origin *application.OriginInfo) {
}

func (a *App) hostAuthSessionAvailable() error {
	return errors.New(appHostAuthUnavailableMessage)
}
