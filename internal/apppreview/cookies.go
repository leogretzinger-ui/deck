package apppreview

import (
	"net/http"
	"strings"
)

const cookieName = "__Host-deck_preview"
const loopbackCookieName = "deck_preview"

func (g *Gateway) cookieName() string {
	if g.loopback {
		return loopbackCookieName
	}
	return cookieName
}

// reservedCookieNames never reach an upstream and can never be set by one.
// The preview cookie is the gateway's whole authority, and Deck's session,
// CSRF and OIDC transaction cookies are Deck's: a dev server that sees or
// replaces any of them gains (or forges) an identity it must not have.
// Parent-domain JavaScript cookies an app writes for itself are the
// browser's business and never authority here — only this exact host-only
// name is.
var reservedCookieNames = map[string]bool{
	cookieName:                  true,
	loopbackCookieName:          true,
	"__Host-noisedeck_session":  true,
	"noisedeck_session":         true,
	"__Host-noisedeck_csrf":     true,
	"noisedeck_csrf":            true,
	"__Host-noisedeck_oidc_txn": true,
	"noisedeck_oidc_txn":        true,
}

// filterForwardedCookies rewrites the outbound Cookie header to carry only
// the app's own cookies.
func filterForwardedCookies(h http.Header) {
	values := h.Values("Cookie")
	if len(values) == 0 {
		return
	}
	raw := strings.Join(values, ";")
	kept := make([]string, 0, strings.Count(raw, ";")+1)
	for _, pair := range strings.Split(raw, ";") {
		name, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if reservedCookieNames[strings.TrimSpace(name)] {
			continue
		}
		kept = append(kept, strings.TrimLeft(pair, " \t"))
	}
	if len(kept) == 0 {
		h.Del("Cookie")
		return
	}
	h.Set("Cookie", strings.Join(kept, "; "))
}

// sanitizeSetCookies turns accepted app Set-Cookie lines host-only and
// drops the rest. The app keeps its own cookie names (the reserved set
// excepted) and attributes; a Domain attribute naming the app's own host or
// a parent of it is stripped, and a Domain pointing anywhere else rejects
// the whole line.
func sanitizeSetCookies(h http.Header, hostname string) {
	values := h.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	kept := make([]string, 0, len(values))
	for _, raw := range values {
		if s, ok := sanitizeSetCookie(raw, hostname); ok {
			kept = append(kept, s)
		}
	}
	h.Del("Set-Cookie")
	for _, s := range kept {
		h.Add("Set-Cookie", s)
	}
}

func sanitizeSetCookie(raw, hostname string) (string, bool) {
	parts := strings.Split(raw, ";")
	name, _, ok := strings.Cut(parts[0], "=")
	name = strings.TrimSpace(name)
	if !ok || !validCookieName(name) || reservedCookieNames[name] {
		return "", false
	}
	kept := []string{parts[0]}
	for _, attr := range parts[1:] {
		attrName, attrValue, _ := strings.Cut(attr, "=")
		if strings.EqualFold(strings.TrimSpace(attrName), "Domain") {
			d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(attrValue)), ".")
			if !domainParents(d, hostname) {
				return "", false
			}
			continue
		}
		kept = append(kept, attr)
	}
	return strings.Join(kept, ";"), true
}

// domainParents reports whether d is hostname itself or a parent domain of
// it. Only then may the app even attempt a Domain attribute — and the
// gateway still strips it, so an accepted cookie is always host-only.
func domainParents(d, hostname string) bool {
	if d == "" || strings.ContainsAny(d, ":/%") {
		return false
	}
	return d == hostname || strings.HasSuffix(hostname, "."+d)
}

func validCookieName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?={}`, r) {
			return false
		}
	}
	return true
}
