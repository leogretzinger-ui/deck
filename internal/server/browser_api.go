package server

import (
	"net/http"
	"net/url"
	"strings"
)

// browserAPIGuard prevents preview pages from exercising the signed-in user's API
// through navigation, embedding or credentialed requests, including GETs.
func (s *Server) browserAPIGuard(next http.Handler) http.Handler {
	if !s.cfg.ProtectBrowserAPI {
		return next
	}
	allowed := map[string]bool{}
	for _, origin := range s.cfg.AllowedOrigins {
		allowed[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		deny := func() { http.Error(w, "cross-origin API access is not allowed", http.StatusForbidden) }
		if origin := r.Header.Get("Origin"); origin != "" && !allowed[origin] {
			deny()
			return
		}
		switch r.Header.Get("Sec-Fetch-Site") {
		case "cross-site", "same-site":
			deny()
			return
		}
		if ref := r.Header.Get("Referer"); ref != "" {
			u, err := url.Parse(ref)
			if err != nil || !allowed[u.Scheme+"://"+u.Host] {
				deny()
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
