package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreviewCannotExerciseBrowserAPI(t *testing.T) {
	s := &Server{cfg: Config{ProtectBrowserAPI: true, AllowedOrigins: []string{"https://deck.example.com"}}}
	h := s.browserAPIGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, path, method, origin, site, ref string
		want                                  int
	}{
		{"same origin", "/api/projects", "GET", "https://deck.example.com", "same-origin", "", 204},
		{"preview fetch", "/api/projects", "GET", "https://one.example.net", "cross-site", "", 403},
		{"preview write", "/api/projects", "POST", "https://one.example.net", "cross-site", "", 403},
		{"navigation", "/api/projects", "GET", "", "cross-site", "", 403},
		{"same site sibling", "/api/projects", "GET", "", "same-site", "", 403},
		{"old browser referer", "/api/projects", "GET", "", "", "https://one.example.net/page", 403},
		{"no metadata CLI", "/api/projects", "GET", "", "", "", 204},
		{"OAuth callback", "/auth/callback", "GET", "", "cross-site", "https://accounts.google.com/", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://deck.example.com"+tc.path, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Referer", tc.ref)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}
