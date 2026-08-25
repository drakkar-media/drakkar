package frontend

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:build
var buildFS embed.FS

// immutableAssetPrefix is where SvelteKit places its content-hashed build
// output (JS chunks, CSS, etc.) -- every filename under here bakes in a hash
// of its own content, so a given path's bytes can never change; a new build
// that changes a file produces a new path instead of overwriting the old
// one. That makes it the one class of response that's always safe to cache
// forever.
const immutableAssetPrefix = "/_app/immutable/"

// Handler returns an http.Handler that serves the SvelteKit static build.
// All routes that don't match a real file fall back to index.html so the
// SvelteKit client-side router handles them.
func Handler() http.Handler {
	sub, err := fs.Sub(buildFS, "build")
	if err != nil {
		panic("frontend: embed sub failed: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		// Try the exact path first; fall back to index.html for SPA routing.
		if f, err := sub.Open(path); err == nil {
			f.Close()
			setCacheControl(w, r.URL.Path)
			fileServer.ServeHTTP(w, r)
			return
		}
		// Serve index.html for any unknown path — the SvelteKit router handles
		// it. Confirmed live (2026-08-25): with no Cache-Control on this
		// response, a CDN/browser that had cached "this exact path returns
		// index.html" from before a deploy (e.g. a JS chunk path that didn't
		// exist in an older build, or has since been superseded by a new
		// content hash) kept serving that same stale index.html fallback
		// indefinitely, even once the origin's current build genuinely had a
		// real file at that path -- the browser then rejected the mismatched
		// text/html response as an invalid JS module, breaking every route
		// whose lazily-loaded chunk got served this way. This response must
		// never be cached by anything.
		w.Header().Set("Cache-Control", "no-store")
		r2 := *r
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, &r2)
	})
}

// setCacheControl marks hashed, content-addressed build assets as safe to
// cache indefinitely (see immutableAssetPrefix) and everything else --
// index.html and any other file served directly, e.g. favicon.ico -- as
// never cacheable, so a client always asks the origin fresh rather than ever
// trusting a copy that might reference build assets that no longer exist.
func setCacheControl(w http.ResponseWriter, urlPath string) {
	if strings.HasPrefix(urlPath, immutableAssetPrefix) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
}
