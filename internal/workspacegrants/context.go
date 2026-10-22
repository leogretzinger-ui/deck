package workspacegrants

import (
	"context"
	"net/http"
)

const CookieName = "__Host-deck_workspace"

type tokenContextKey struct{}
type tokenHash string

func (tokenHash) String() string   { return "workspacegrants.tokenHash" }
func (tokenHash) GoString() string { return "workspacegrants.tokenHash" }

// WithToken hashes the untrusted cookie immediately; context never retains the raw bearer.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, tokenHash(hashToken(token)))
}
func contextHash(ctx context.Context) string {
	value, _ := ctx.Value(tokenContextKey{}).(tokenHash)
	return string(value)
}

// CookieContext adds no authority by itself. Duplicate cookies are ambiguous and refused.
func CookieContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		count := 0
		for _, cookie := range r.Cookies() {
			if cookie.Name == CookieName {
				count++
				token = cookie.Value
			}
		}
		if count != 1 {
			token = ""
		}
		next.ServeHTTP(w, r.WithContext(WithToken(r.Context(), token)))
	})
}

// Cookie is the host-only delivery format; callers also enforce their fixed HTTPS origin.
func (d Delivery) Cookie() *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: d.token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: d.ExpiresAt}
}
