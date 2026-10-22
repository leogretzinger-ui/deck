package server

import (
	"bytes"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

const staticCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// StaticHandler serves one immutable frontend build. It is intentionally
// public so a logged-out browser can render the Google sign-in action; all API
// and terminal authority remains behind the auth middleware mounted on their
// more-specific routes.
func StaticHandler(assets fs.FS, buildID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setStaticSecurityHeaders(w.Header())

		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/")
		root := name == ""
		if root {
			name = "index.html"
		} else if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		if name == "UNBUILT" || strings.HasPrefix(name, ".") || strings.Contains(name, "/.") {
			http.NotFound(w, r)
			return
		}
		// Every build carries the font bench, but only its authenticated
		// experiment route serves it.
		if name == "bench.html" {
			http.NotFound(w, r)
			return
		}

		// A typo under a server-owned namespace must remain a 404; presenting
		// index.html there would disguise a missing API/auth/health route.
		if reservedServerPath(name) {
			http.NotFound(w, r)
			return
		}

		if serveStaticFile(w, r, assets, name, buildID) {
			return
		}
		if root {
			http.Error(w, "frontend assets were not built", http.StatusServiceUnavailable)
			return
		}

		// Only extensionless browser navigations receive the SPA shell. Missing
		// scripts, styles, fonts, maps, and assets stay honest 404s.
		if !strings.HasPrefix(name, "assets/") && !strings.HasPrefix(name, "fonts/") &&
			path.Ext(strings.TrimSuffix(name, "/")) == "" && wantsHTML(r) {
			if serveStaticFile(w, r, assets, "index.html", buildID) {
				return
			}
			http.Error(w, "frontend assets were not built", http.StatusServiceUnavailable)
			return
		}

		http.NotFound(w, r)
	})
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, assets fs.FS, name, buildID string) bool {
	f, err := assets.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		if w.Header().Get("Cache-Control") != "no-store" {
			w.Header().Set("Cache-Control", "no-cache")
		}
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if name == "index.html" {
		raw, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "frontend asset could not be read", http.StatusInternalServerError)
			return true
		}
		raw = bytes.ReplaceAll(raw, []byte("__NOISEDECK_BUILD_ID__"), []byte(html.EscapeString(buildID)))
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(raw))
		return true
	}

	reader, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "frontend asset is not seekable", http.StatusInternalServerError)
		return true
	}
	http.ServeContent(w, r, name, time.Time{}, reader)
	return true
}

func setStaticSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", staticCSP)
	h.Set("Referrer-Policy", "no-referrer")
}

func reservedServerPath(name string) bool {
	// "__dev" is the dev-preview subtree: a GATED route like bench.html,
	// mounted only when the experiment is on. Reserving it here keeps the
	// path a 404 when the experiment is off instead of serving the SPA
	// shell, which would hint at a gated surface and, worse, render the
	// compiled app under a base it was not built for.
	for _, prefix := range []string{"api", "auth", "setup", "livez", "readyz", "__dev"} {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}

func wantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return accept == "" || accept == "*/*" || strings.Contains(accept, "text/html")
}
