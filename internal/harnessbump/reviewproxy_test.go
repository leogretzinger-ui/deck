package harnessbump

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestReviewProxyRejectsOtherDestinations(t *testing.T) {
	for _, host := range []string{"127.0.0.1:443", "169.254.169.254:443", "github.com:443", "chatgpt.com:80", "chatgpt.com.evil.test:443", "user@chatgpt.com:443", "api.openai.com:443"} {
		r := httptest.NewRequest(http.MethodConnect, "https://"+host, nil)
		r.Host = host
		w := httptest.NewRecorder()
		ReviewProxy{}.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", host, w.Code)
		}
	}
	w := httptest.NewRecorder()
	ReviewProxy{}.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://chatgpt.com", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
}

func TestReviewProxyRejectsPrivateResolution(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "100.100.100.100", "169.254.169.254", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "0.0.0.0", "198.18.0.1", "64:ff9b::a00:1"} {
		if reviewPublicAddress(netip.MustParseAddr(address)) {
			t.Errorf("accepted %s", address)
		}
	}
	if !reviewPublicAddress(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public address rejected")
	}
}
