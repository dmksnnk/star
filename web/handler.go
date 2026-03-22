package web

import (
	"io/fs"
	"net/http"
)

// StaticHandler serves static files for the web UI.
func StaticHandler() http.Handler {
	staticFS, _ := fs.Sub(Static, "static")

	return http.FileServer(http.FS(staticFS))
}
