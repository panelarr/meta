// SPDX-License-Identifier: GPL-3.0-or-later

// Package adminui serves meta's admin web UI, a single-page app built from
// this directory (`make admin`) and embedded into the meta binary.
package adminui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// CSP is the Content-Security-Policy of the UI. Scripts, styles, fonts and
// API calls stay on meta's own origin. Images may also come from https:
// URLs, because entry covers are hosted by the metadata providers, and
// from data: and blob: URLs.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob: https:; " +
	"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>Panelarr Meta</title>
<p>The admin UI isn't built: run <code>make admin</code> and rebuild meta.</p>
`

// Built reports whether the UI was built into this binary.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}

// Handler serves the UI: files from dist, and index.html for every other
// path so the app's own routes work on reload. Hashed assets are cached for
// good; everything else is revalidated.
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed always has dist
	}
	return handler(files)
}

func handler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == ".keep" || !exists(files, name) {
			name = "index.html"
		}
		if !exists(files, name) {
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, notBuilt)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		// files is the embedded build, so no path can leave it.
		http.ServeFileFS(w, r, files, name) //nolint:gosec // see above
	})
}

func exists(files fs.FS, name string) bool {
	st, err := fs.Stat(files, name)
	return err == nil && !st.IsDir()
}
