package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// marker that only appears in the compile-time placeholder, never in a real
// Vite build, so the assertions below fail if the fallback regresses.
const fallbackMarker = "This is a placeholder page"

// realIndexMarker stands in for a Vite build's index.html.
const realIndexMarker = "real console index"

// serveFrontend takes the asset filesystem as a parameter, so these tests are
// deterministic: they do not depend on whether static/ happens to hold a Vite
// build (after `make webui`/`make build`) or only the .keep anchor (fresh
// clone). Before the parameter existed, the placeholder assertions passed on a
// fresh clone and failed once the console was embedded.

func TestFrontendFallbackIndexWhenConsoleNotBuilt(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Empty filesystem == a fresh clone with no embedded Vite build.
	serveFrontend(fstest.MapFS{}, recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("GET / content type = %q, want text/html; charset=utf-8", got)
	}
	if body := recorder.Body.String(); !strings.Contains(body, fallbackMarker) {
		t.Fatalf("GET / body = %q, want it to contain %q", body, fallbackMarker)
	}
}

func TestFrontendServesEmbeddedIndexWhenConsoleBuilt(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>" + realIndexMarker + "</html>")},
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	serveFrontend(fsys, recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); !strings.Contains(body, realIndexMarker) {
		t.Fatalf("GET / body = %q, want it to contain %q", body, realIndexMarker)
	}
	if strings.Contains(recorder.Body.String(), fallbackMarker) {
		t.Fatalf("GET / served the placeholder although index.html is embedded")
	}
}

func TestFrontendNormalisesExtensionlessPath(t *testing.T) {
	// serveFrontend normalises an extension-less path (a hypothetical SPA deep
	// link) to index.html. The real router never forwards such a path here —
	// index() only routes "/" and "/assets/*" to frontend() — so this pins the
	// normalisation in isolation. TestRouterRejectsUnknownRoute covers what the
	// router actually returns for /dashboard.
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>" + realIndexMarker + "</html>")},
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)

	serveFrontend(fsys, recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /dashboard status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); !strings.Contains(body, realIndexMarker) {
		t.Fatalf("GET /dashboard body = %q, want it to contain %q", body, realIndexMarker)
	}
}

func TestFrontendServesEmbeddedAsset(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("<html></html>")},
		"assets/app-abc.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/app-abc.js", nil)

	serveFrontend(fsys, recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET asset status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("GET asset content type = %q, want text/javascript; charset=utf-8", got)
	}
	if body := recorder.Body.String(); body != "console.log(1)" {
		t.Fatalf("GET asset body = %q, want %q", body, "console.log(1)")
	}
}

func TestFrontendHeadOmitsBody(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>" + realIndexMarker + "</html>")},
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/", nil)

	serveFrontend(fsys, recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("HEAD / body = %q, want empty", body)
	}
}

// The handler wiring is exercised through the real router, so a broken
// frontendFS (for example a wrong embed subdirectory) fails here. The
// assertion holds for both a fresh clone and a build with the console
// embedded, so it never depends on static/ contents.
func TestFrontendRouteWiring(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	NewHandler(nil).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("GET / content type = %q, want text/html; charset=utf-8", got)
	}
	if recorder.Body.Len() == 0 {
		t.Fatalf("GET / body is empty")
	}
}

func TestFrontendRejectsUnsupportedMethods(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/", nil)

		serveFrontend(fstest.MapFS{}, recorder, req)

		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s / status = %d, want %d", method, recorder.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestFrontendMissingAssetIsNotFound(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/index-missing.js", nil)

	serveFrontend(fstest.MapFS{}, recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET missing asset status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestMimeType(t *testing.T) {
	cases := []struct {
		assetPath string
		want      string
	}{
		{assetPath: "index.html", want: "text/html; charset=utf-8"},
		{assetPath: "assets/index-abc.js", want: "text/javascript; charset=utf-8"},
		{assetPath: "assets/index-abc.css", want: "text/css; charset=utf-8"},
		{assetPath: "manifest.json", want: "application/json"},
		{assetPath: "logo.svg", want: "image/svg+xml"},
		{assetPath: "logo.png", want: "image/png"},
		{assetPath: "favicon.ico", want: "image/x-icon"},
		{assetPath: "data.bin", want: "application/octet-stream"},
	}
	for _, tc := range cases {
		if got := mimeType(tc.assetPath); got != tc.want {
			t.Errorf("mimeType(%q) = %q, want %q", tc.assetPath, got, tc.want)
		}
	}
}

// TestRouterRejectsUnknownRoute pins the router-level behaviour: only "/" and
// /assets/* reach the frontend handler. A deep link like /dashboard is an
// unknown API path and returns 404, not the SPA index.
func TestRouterRejectsUnknownRoute(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)

	NewHandler(nil).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /dashboard status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
