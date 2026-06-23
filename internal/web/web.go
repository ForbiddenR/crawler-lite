// Package web serves the embedded React SPA build.
//
// In production the master binary embeds web/dist (produced by
// `pnpm build`) via //go:embed and serves it directly, so the whole
// control plane is one image with no separate static-file mount.
// Caddy in front only terminates TLS and reverse-proxies to the
// master.
//
// In dev the embedded dist/ contains only a .gitkeep placeholder (no
// index.html); Handler() detects that and returns a 404 handler, so
// the dev server on :5173 with its /api proxy is the only frontend
// surface and the master's /api routes still work standalone.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the embedded SPA build:
//
//   - exact files under dist (e.g. /assets/index-abc.js, /favicon.ico)
//     are served verbatim with correct MIME types via http.FileServer;
//   - any other GET path that is not a real file falls through to
//     index.html (SPA client-side routing: /tasks/123, /spiders, …);
//   - non-GET methods are passed through unchanged (they won't match a
//     file and will hit the index.html fallback, which is harmless).
//
// Returns http.NotFoundHandler when dist/index.html is absent, i.e. in
// a dev build with no frontend bundle.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	// Empty placeholder dist (dev mode): no index.html to fall back to.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return http.NotFoundHandler()
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: a GET for a path with no matching file rewrites
		// to "/" so the FileServer serves index.html. /api/* is owned
		// by the router and never reaches here (NoRoute only fires for
		// unmatched routes), but guard anyway in case this handler is
		// mounted elsewhere.
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
			rel := strings.TrimPrefix(r.URL.Path, "/")
			if rel == "" {
				rel = "index.html"
			}
			if _, statErr := fs.Stat(sub, rel); statErr != nil {
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
