package frontend

import (
	"net/http/httptest"
	"testing"
)

// TestImmutableAssetsAreCachedForever guards the fix for a real production
// incident (2026-08-25): a redeploy that regenerates every content-hashed
// build asset must not leave clients (browsers or any CDN in front) unsure
// whether it's safe to keep reusing an old copy. Every response under
// immutableAssetPrefix must say so explicitly.
func TestImmutableAssetsAreCachedForever(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/_app/immutable/assets/12.DBMNKZ_C.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 for a real immutable asset, got %d", rec.Code)
	}
	got := rec.Header().Get("Cache-Control")
	want := "public, max-age=31536000, immutable"
	if got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
}

// TestIndexHTMLIsNeverCached guards the same incident from the other side:
// the document that names which hashed asset paths are currently valid must
// never be cached, or a client can keep referencing assets a later deploy
// has already replaced.
func TestIndexHTMLIsNeverCached(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 for index.html, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
}

// TestSPAFallbackIsNeverCached is the specific regression test for the
// 2026-08-25 incident: a request for a path that doesn't exist in the
// current build (e.g. a since-superseded JS chunk from a previous deploy)
// falls back to serving index.html for SvelteKit's client-side router. That
// fallback response must never be cached, or a CDN/browser that cached "this
// path serves index.html" while the path was genuinely missing goes on
// serving that same fallback forever -- even after a later deploy adds a
// real file at that exact path -- which the browser then rejects outright
// (a JS module import expecting text/javascript, getting text/html).
// Confirmed live: this exact mechanism broke every route in production whose
// lazily-loaded chunk got served this way after several redeploys in quick
// succession regenerated every asset hash.
func TestSPAFallbackIsNeverCached(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/_app/immutable/chunks/this-path-does-not-exist-in-this-build.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected the SPA fallback to still serve 200 (index.html), got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q -- a cacheable fallback response is exactly the 2026-08-25 production bug", got, "no-store")
	}
}
