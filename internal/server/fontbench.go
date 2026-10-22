package server

import (
	"io"
	"io/fs"
	"net/http"
)

// fontBenchCSP relaxes the static policy for the BENCH ROUTE ONLY: the bench
// loads its free candidates at runtime from Google Fonts, which needs the
// stylesheet host (fonts.googleapis.com) under style-src and the font-file
// host it pulls from (fonts.gstatic.com) under font-src. Nothing else is
// widened, and the app's own pages keep staticCSP — a candidate catalogue is
// the bench's business, not the console's.
const fontBenchCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; connect-src 'self'; img-src 'self' data:; font-src 'self' https://fonts.gstatic.com; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// FontBenchHandler serves the font-substitution bench page. It is mounted
// only when the font-bench experiment is enabled, behind the ordinary browser
// authentication; StaticHandler refuses bench.html unconditionally, so with
// the experiment off the path 404s exactly like any unknown one.
func FontBenchHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setStaticSecurityHeaders(w.Header())
		w.Header().Set("Content-Security-Policy", fontBenchCSP)
		f, err := assets.Open("bench.html")
		if err != nil {
			// The embed build carries the bench page by design (vite.config
			// keeps the bench entry in embed builds); its absence means the
			// frontend was not built, the same condition the shell reports.
			http.Error(w, "frontend assets were not built", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = f.Close() }()
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := io.Copy(w, f); err != nil {
			return
		}
	})
}
