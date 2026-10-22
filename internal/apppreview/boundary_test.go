package apppreview

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
)

func TestInformationalResponsesCannotBypassHeaderPolicy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "__Host-deck_preview=upstream; Secure; Path=/")
		w.Header().Set("Access-Control-Allow-Origin", "https://sibling.example.net")
		w.WriteHeader(http.StatusEarlyHints)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	g, err := New(Config{DeckOrigins: []string{"https://deck.example.com"}, Targets: []Target{{ProjectID: "p", Origin: "https://app.example.net", Upstream: strings.TrimPrefix(upstream.URL, "http://")}}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	server := httptest.NewServer(g)
	defer server.Close()
	entry, err := g.Issue("p", "operator")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequest("POST", server.URL+entryPath, strings.NewReader(url.Values{"ticket": {entry.Ticket}}.Encode()))
	r.Host = "app.example.net"
	r.Header.Set("Origin", "https://deck.example.com")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 303 || len(res.Cookies()) != 1 {
		t.Fatalf("entry status %d", res.StatusCode)
	}
	cookie := res.Cookies()[0]
	r, _ = http.NewRequest("GET", server.URL+"/", nil)
	r.Host = "app.example.net"
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://app.example.net")
	var earlyCookie, earlyCORS bool
	trace := &httptrace.ClientTrace{Got1xxResponse: func(_ int, h textproto.MIMEHeader) error {
		earlyCookie = earlyCookie || h.Get("Set-Cookie") != ""
		earlyCORS = earlyCORS || h.Get("Access-Control-Allow-Origin") != ""
		return nil
	}}
	r = r.WithContext(httptrace.WithClientTrace(r.Context(), trace))
	res, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if earlyCookie || earlyCORS {
		t.Fatalf("early hints bypassed header filtering: cookie=%v cors=%v", earlyCookie, earlyCORS)
	}
	if len(res.Cookies()) != 0 || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("final response bypassed header filtering")
	}
}

func TestAllCookieHeaderLinesAreFiltered(t *testing.T) {
	h := http.Header{"Cookie": []string{"", "__Host-deck_preview=private; app=value"}}
	filterForwardedCookies(h)
	if strings.Contains(strings.Join(h.Values("Cookie"), ";"), "private") {
		t.Fatal("preview authority survived a second Cookie header")
	}
}

func TestMissingBrowserSignalsCannotAuthorizeSiblingRequests(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/jsonp", nil)
	if browserAllows(r, &target{originStr: "https://app.example.net"}) {
		t.Fatal("request without any same-origin evidence was accepted")
	}
}

func TestResponseTrailersCannotBypassHeaderPolicy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "Access-Control-Allow-Origin, Clear-Site-Data")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "body")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Clear-Site-Data", "\"cookies\"")
	}))
	defer upstream.Close()
	g, _, _, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	// Replace only the test target's fixed dial address with the hostile server.
	target := g.byHost[testHostA]
	target.upstream = strings.TrimPrefix(upstream.URL, "http://")
	server := httptest.NewServer(g)
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	req.Host = testHostA
	req.Header.Set("Origin", "https://"+testHostA)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "body" {
		t.Fatalf("unexpected body: %q", body)
	}
	if len(res.Trailer) != 0 {
		t.Fatalf("upstream trailers escaped: %v", res.Trailer)
	}
}

func TestGatewayRestartInvalidatesTicketsAndSessions(t *testing.T) {
	first, _, a, b := newSetup(t)
	cookie := enterPreview(t, first, testProjectA, testHostA)
	entry, err := first.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, _ := newTestGateway(t, testDeckOrigin, testTargets(a.addr(), b.addr())...)
	if res := authed(t, restarted, cookie, testHostA, http.MethodGet, "/", nil); res.Code != http.StatusForbidden {
		t.Fatalf("old session survived restart: %d", res.Code)
	}
	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	res := httptest.NewRecorder()
	restarted.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("old ticket survived restart: %d", res.Code)
	}
}
