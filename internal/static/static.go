// Package static serves the embedded web files.
package static

import (
	"net/http"

	"github.com/danielrh/lobbylink/web"
)

// Handler serves the embedded demo page and browser client bundle.
// no-cache keeps clients current across server upgrades without
// defeating conditional requests.
func Handler() http.Handler {
	fs := http.FileServerFS(web.Files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fs.ServeHTTP(w, r)
	})
}
