package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/apppreview"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/project"
)

func TestProjectPreviewEntryRequiresAuthenticationAndRegisteredProject(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Server.AllowedOrigins = []string{"https://deck.example.com"}
	h := upOrFail(t, cfg)
	base := serveServer(t, h)
	repo := project.NewRepository(h.DB)
	p, err := repo.RegisterPlain(context.Background(), "sample", cfg.Projects.AllowedRoots[0], cfg.Projects.AllowedRoots)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "sample") }))
	defer upstream.Close()
	// The entry handler was mounted while previews were disabled. Install a
	// synthetic gateway to exercise that same handler without another backend.
	issue := func(identity, origin string) (int, []byte) {
		r, _ := http.NewRequest("POST", base+"/api/projects/"+p.ID+"/preview", nil)
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
	if status, _ := issue("", "https://deck.example.com"); status != 401 {
		t.Fatalf("anonymous entry: %d", status)
	}
	if status, _ := issue("operator", "https://deck.example.com"); status != 404 {
		t.Fatalf("disabled preview: %d", status)
	}
	h.Preview, err = apppreview.New(apppreview.Config{DeckOrigins: []string{"https://deck.example.com"}, Targets: []apppreview.Target{{ProjectID: p.ID, Origin: "https://one.example.net", Upstream: strings.TrimPrefix(upstream.URL, "http://")}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := issue("operator", "https://one.example.net"); status != 403 {
		t.Fatalf("foreign origin entry: %d", status)
	}
	status, b := issue("operator", "https://deck.example.com")
	var entry apppreview.Entry
	if status != 200 || json.Unmarshal(b, &entry) != nil || entry.Ticket == "" || entry.URL != "https://one.example.net/__deck_preview/enter" {
		t.Fatalf("entry: status=%d", status)
	}
}

func TestProjectPreviewLifecycleLeavesAppAlive(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer upstream.Close()
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Server.AllowedOrigins = []string{"https://deck.example.com"}
	cfg.ProjectPreviews = config.ProjectPreviews{MetadataSocket: filepath.Join(t.TempDir(), "metadata", "preview.sock"), Bind: "127.0.0.1:0", DeckOrigin: "https://deck.example.com", Unconfined: true, Targets: []config.PreviewTarget{{ProjectID: "synthetic", Origin: "https://one.example.net", Upstream: strings.TrimPrefix(upstream.URL, "http://")}}}
	h := upOrFail(t, cfg)
	_, stop := startServer(t, h.Run, h.Server.BoundAddr)
	r, _ := http.NewRequest("GET", "http://"+h.previewListeners[0].Addr().String()+"/", nil)
	r.Host = "one.example.net"
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("anonymous app status: %d", res.StatusCode)
	}
	stop()
	if _, err := os.Lstat(cfg.ProjectPreviews.MetadataSocket); !os.IsNotExist(err) {
		t.Fatalf("metadata socket survived Deck shutdown: %v", err)
	}
	if code, _ := get(t, upstream.URL); code != 204 {
		t.Fatalf("app did not survive Deck shutdown: %d", code)
	}
}

func loopbackPreviewPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 unavailable: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func TestLoopbackPreviewListeners(t *testing.T) {
	port := loopbackPreviewPort(t)
	cfg := config.ProjectPreviews{Loopback: true, LoopbackPort: port, PortMin: 15000, PortMax: 15002}
	listeners, err := listenProjectPreviews(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handles{previewListeners: listeners}
	t.Cleanup(h.Close)
	if len(listeners) != 3 {
		t.Fatalf("listeners = %d", len(listeners))
	}
	for i, l := range listeners {
		if got, want := l.Addr().String(), fmt.Sprintf("127.0.0.%d:%d", 2+i, port); got != want {
			t.Fatalf("address = %s, want %s", got, want)
		}
	}
	h.Close()
	for _, l := range listeners {
		rebound, err := net.Listen("tcp", l.Addr().String())
		if err != nil {
			t.Fatalf("listener survived Close: %v", err)
		}
		_ = rebound.Close()
	}
	blockedAddress := fmt.Sprintf("127.0.0.3:%d", port)
	blocked, err := net.Listen("tcp4", blockedAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	if _, err := listenProjectPreviews(cfg); err == nil || !strings.Contains(err.Error(), blockedAddress) {
		t.Fatalf("bind conflict should name %s: %v", blockedAddress, err)
	}
	first, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.2:%d", port))
	if err != nil {
		t.Fatalf("failed startup leaked earlier listener: %v", err)
	}
	_ = first.Close()
}

func TestLoopbackPreviewUnspecifiedListener(t *testing.T) {
	cfg := config.ProjectPreviews{Loopback: true, Bind: "0.0.0.0:0", PortMin: 15000, PortMax: 15007}
	listeners, err := listenProjectPreviews(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	if len(listeners) != 1 || !listeners[0].Addr().(*net.TCPAddr).IP.IsUnspecified() {
		t.Fatalf("expected one unspecified listener, got %v", listeners)
	}
}

func TestLoopbackPreviewLifecycleAndSettings(t *testing.T) {
	port := loopbackPreviewPort(t)
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Server.AllowedOrigins = []string{"http://127.0.0.1:8837", "http://localhost:8837"}
	cfg.ProjectPreviews = config.ProjectPreviews{Loopback: true, LoopbackPort: port, Unconfined: true, PortMin: 15000, PortMax: 15001, MetadataSocket: filepath.Join(t.TempDir(), "metadata", "preview.sock")}
	h := upOrFail(t, cfg)
	base, stop := startServer(t, h.Run, h.Server.BoundAddr)
	p, err := project.NewRepository(h.DB).RegisterPlain(context.Background(), "sample", cfg.Projects.AllowedRoots[0], cfg.Projects.AllowedRoots)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range cfg.Server.AllowedOrigins {
		req, _ := http.NewRequest("GET", base+"/api/projects/"+p.ID+"/preview/settings", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Forwarded-User", "operator")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var settings projectPreviewSettings
		err = json.NewDecoder(res.Body).Decode(&settings)
		_ = res.Body.Close()
		if err != nil || res.StatusCode != 200 || !settings.Configured || !settings.SelfService {
			t.Fatalf("loopback settings: %d %+v %v", res.StatusCode, settings, err)
		}
	}
	req, _ := http.NewRequest("GET", base+"/api/projects/"+p.ID+"/preview/settings", nil)
	req.Header.Set("X-Forwarded-User", "operator")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("cross-site API navigation: %d", res.StatusCode)
	}
	for i, l := range h.previewListeners {
		id := fmt.Sprintf("app-%d", i)
		origin := "http://" + l.Addr().String()
		if err := h.Preview.Register(apppreview.Target{ProjectID: id, Origin: origin, Upstream: fmt.Sprintf("127.0.0.1:%d", 15000+i)}); err != nil {
			t.Fatal(err)
		}
		if code, _ := get(t, origin); code != 403 {
			t.Fatalf("slot %d not served: %d", i, code)
		}
	}
	stop()
	for _, l := range h.previewListeners {
		conn, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("listener %s survived shutdown", l.Addr())
		}
	}
}
