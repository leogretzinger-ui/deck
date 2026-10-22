package boot

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/apppreview"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/project"
)

func TestManagedProjectPreviewAPI(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Server.AllowedOrigins = []string{"https://deck.example.com"}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cfg.ProjectPreviews = config.ProjectPreviews{MetadataSocket: filepath.Join(t.TempDir(), "metadata", "preview.sock"), Bind: "127.0.0.1:0", DeckOrigin: "https://deck.example.com", Unconfined: true, BaseDomain: "apps.example.net", PortMin: port, PortMax: min(port+8, 65535)}
	h := upOrFail(t, cfg)
	base := serveServer(t, h)
	repo := project.NewRepository(h.DB)
	root := cfg.Projects.AllowedRoots[0]
	ids := []string{}
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		p, err := repo.RegisterPlain(context.Background(), name, dir, cfg.Projects.AllowedRoots)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	request := func(method, path, body, identity, origin string) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		r.Header.Set("X-Forwarded-User", identity)
		r.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	settings := func(method, id string) projectPreviewSettings {
		t.Helper()
		code, body := request(method, "/api/projects/"+id+"/preview/settings", "", "operator", "https://deck.example.com")
		if code != 200 {
			t.Fatalf("%s settings: %d %s", method, code, body)
		}
		var s projectPreviewSettings
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	path := "/api/projects/" + ids[0] + "/preview/settings"
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if code, _ := request(method, path, "", "", "https://deck.example.com"); code != 401 {
			t.Fatalf("anonymous %s = %d", method, code)
		}
		if code, _ := request(method, path, "", "operator", "https://evil.example.net"); code != 403 {
			t.Fatalf("foreign %s = %d", method, code)
		}
	}
	for _, body := range []string{`{"upstream":"127.0.0.1:22"}`, `{"origin":"https://deck.example.com"}`, `{} {}`, `[]`, `null`} {
		if code, _ := request("PUT", path, body, "operator", "https://deck.example.com"); code != 400 {
			t.Fatalf("caller destination %s = %d", body, code)
		}
	}
	initial := settings("GET", ids[0])
	if !initial.Configured || !initial.SelfService || initial.Enabled || initial.Origin != "" {
		t.Fatalf("initial: %+v", initial)
	}
	one, two := settings("PUT", ids[0]), settings("PUT", ids[1])
	if !one.Enabled || one.Source != "managed" || one.Port == two.Port || one.Origin == two.Origin {
		t.Fatalf("assignments: %+v %+v", one, two)
	}
	if repeated := settings("PUT", ids[0]); repeated != one {
		t.Fatalf("enable is not idempotent: %+v", repeated)
	}
	check := func(want bool) {
		t.Helper()
		code, body := request("POST", "/api/projects/"+ids[0]+"/preview/check", "{}", "operator", "https://deck.example.com")
		var probe struct {
			Reachable bool   `json:"reachable"`
			CheckedAt string `json:"checked_at"`
		}
		if code != 200 || json.Unmarshal(body, &probe) != nil || probe.Reachable != want || probe.CheckedAt == "" {
			t.Fatalf("port observation: %d %s", code, body)
		}
	}
	check(false)
	app, err := net.Listen("tcp", one.Upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	check(true)
	code, body := request("POST", "/api/projects/"+ids[0]+"/preview", "", "operator", "https://deck.example.com")
	var ticket apppreview.Entry
	if code != 200 || json.Unmarshal(body, &ticket) != nil {
		t.Fatalf("entry: %d", code)
	}
	disabled := settings("DELETE", ids[0])
	if disabled.Enabled || disabled.Origin != one.Origin || disabled.Port != one.Port || !h.Preview.HasProject(ids[1]) {
		t.Fatalf("disable: %+v", disabled)
	}
	if code, _ := request("POST", "/api/projects/"+ids[0]+"/preview/check", "", "operator", "https://deck.example.com"); code != 409 {
		t.Fatalf("disabled check: %d", code)
	}
	if again := settings("PUT", ids[0]); again != one {
		t.Fatalf("re-enable changed assignment: %+v", again)
	}
	entryURL, err := url.Parse(ticket.URL)
	if err != nil {
		t.Fatal(err)
	}
	entryRequest := httptest.NewRequest("POST", entryURL.RequestURI(), strings.NewReader(url.Values{"ticket": {ticket.Ticket}}.Encode()))
	entryRequest.Host = entryURL.Host
	entryRequest.Header.Set("Origin", "https://deck.example.com")
	entryRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	entryResponse := httptest.NewRecorder()
	h.Preview.ServeHTTP(entryResponse, entryRequest)
	if entryResponse.Code != 403 {
		t.Fatalf("old ticket survived disable/re-enable: %d", entryResponse.Code)
	}
	if code, _ := request("DELETE", "/api/projects/"+ids[0], "", "operator", "https://deck.example.com"); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if h.Preview.HasProject(ids[0]) || !h.Preview.HasProject(ids[1]) {
		t.Fatal("project removal did not revoke only its preview")
	}
	if code, _ := request("PUT", path, "", "operator", "https://deck.example.com"); code != 404 {
		t.Fatalf("removed project enable: %d", code)
	}
	var enabled int
	if err := h.DB.Reader().QueryRow("SELECT enabled FROM project_preview_registrations WHERE project_id = ?", ids[0]).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatalf("removed registration enabled=%d err=%v", enabled, err)
	}
	if one.Upstream != net.JoinHostPort("127.0.0.1", strconv.Itoa(one.Port)) {
		t.Fatalf("unexpected assigned endpoint: %s", one.Upstream)
	}
}

func TestProjectPreviewSettingsWithoutInstallationSetup(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Server.AllowedOrigins = []string{"https://deck.example.com"}
	h := upOrFail(t, cfg)
	base := serveServer(t, h)
	p, err := project.NewRepository(h.DB).RegisterPlain(context.Background(), "sample", cfg.Projects.AllowedRoots[0], cfg.Projects.AllowedRoots)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method string
		code   int
	}{{"GET", 200}, {"PUT", 409}, {"DELETE", 409}} {
		r, _ := http.NewRequest(tc.method, base+"/api/projects/"+p.ID+"/preview/settings", nil)
		r.Header.Set("X-Forwarded-User", "operator")
		r.Header.Set("Origin", "https://deck.example.com")
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatalf("%s = %d", tc.method, res.StatusCode)
		}
		if tc.method == "GET" {
			var state projectPreviewSettings
			if json.Unmarshal(body, &state) != nil || state.Configured || state.SelfService || state.Enabled {
				t.Fatalf("unconfigured state: %s", body)
			}
		}
	}
}
