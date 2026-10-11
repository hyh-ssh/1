package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// frontendAssets contains the Vite output when the local frontend build (or
// the container build) copies frontend/dist here. The all: prefix is required
// so the tracked static/.keep anchor is embedded and the pattern matches at
// least one file on a fresh clone, keeping `go build` working before the
// frontend toolchain has run.
//
//go:embed all:static
var frontendAssets embed.FS

// frontendFS is the embedded assets rooted at static/, so a lookup is
// "index.html" or "assets/index-abc.js" rather than "static/...". Rooting it
// once here keeps serveFrontend testable against any fs.FS.
var frontendFS = mustSub(frontendAssets, "static")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("httpapi: embedded frontend assets missing " + dir + ": " + err.Error())
	}
	return sub
}

// fallbackIndexHTML is served in place of index.html when the Vite build has
// not been copied into the embedded assets, so a fresh clone stays runnable.
// It is a placeholder rather than the real console. The router only forwards
// "/" (and /assets/*) here, so in production this is reached at "/" alone.
const fallbackIndexHTML = `<!doctype html>
<html lang="en">
  <head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>omni-gateway</title></head>
  <body><main style="font-family:system-ui;max-width:42rem;margin:4rem auto;padding:1rem"><h1>omni-gateway</h1><p>This is a placeholder page. The API is running, but the embedded Vue console was not built. Run <code>make webui</code> to build the frontend and recompile, or copy <code>frontend/dist/</code> into the embedded frontend assets for the full console.</p></main></body>
</html>`

// frontend serves the console from the embedded assets.
func frontend(w http.ResponseWriter, r *http.Request) {
	serveFrontend(frontendFS, w, r)
}

// serveFrontend resolves r.URL.Path against fsys. fsys is a parameter rather
// than a package-level global so tests can exercise both branches — a build
// with the real console embedded and a fresh clone with only the placeholder —
// without depending on the state of static/ at test time.
func serveFrontend(fsys fs.FS, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed")
		return
	}
	assetPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	// Extension-less paths normalise to index.html. The router only ever hands
	// "/" and "/assets/*" to frontend(), so in production this branch serves
	// "/"; the normalisation keeps serveFrontend self-consistent for callers
	// that pass an arbitrary path (see TestFrontendNormalisesExtensionlessPath).
	if assetPath == "." || assetPath == "" || !strings.Contains(path.Base(assetPath), ".") {
		assetPath = "index.html"
	}
	if assetPath == "index.html" {
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			// No embedded Vite build: serve the compile-time placeholder.
			writeAsset(w, r, "index.html", []byte(fallbackIndexHTML))
			return
		}
		writeAsset(w, r, "index.html", data)
		return
	}
	data, err := fs.ReadFile(fsys, assetPath)
	if err != nil {
		// A concrete static resource (for example assets/index-abc.js) that is
		// not embedded stays a 404 rather than the SPA fallback.
		notFound(w)
		return
	}
	writeAsset(w, r, assetPath, data)
}

func writeAsset(w http.ResponseWriter, r *http.Request, assetPath string, data []byte) {
	if contentType := mimeType(assetPath); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func mimeType(assetPath string) string {
	switch {
	case strings.HasSuffix(assetPath, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(assetPath, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(assetPath, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(assetPath, ".json"):
		return "application/json"
	case strings.HasSuffix(assetPath, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(assetPath, ".png"):
		return "image/png"
	case strings.HasSuffix(assetPath, ".ico"):
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}
