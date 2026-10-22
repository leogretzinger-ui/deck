package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPreviewOverlayLoopbackSlots(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "compose.previews.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var overlay composeFile
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		t.Fatalf("compose.previews.yaml does not parse: %v", err)
	}
	app := overlay.Services["app"]
	base := readCompose(t).Services["app"].Environment
	for key, value := range map[string]string{
		"NOISEDECK_PROJECT_PREVIEWS_LOOPBACK":   "true",
		"NOISEDECK_PROJECT_PREVIEWS_UNCONFINED": "true",
		"NOISEDECK_PROJECT_PREVIEWS_BIND":       "0.0.0.0:8840",
		"NOISEDECK_PROJECT_PREVIEWS_PORT_MIN":   "15000",
		"NOISEDECK_PROJECT_PREVIEWS_PORT_MAX":   "15007",
	} {
		want := value
		if strings.HasPrefix(base[key], "${"+key+":-") {
			want = "${" + key + ":-" + value + "}"
		}
		if got := app.Environment[key]; got != want {
			t.Errorf("preview %s = %q, want %q", key, got, want)
		}
	}

	wantPorts := make(map[string]bool)
	for slot := 2; slot <= 9; slot++ {
		wantPorts[fmt.Sprintf("127.0.0.%d:8840:8840", slot)] = true
	}
	if len(app.Ports) != len(wantPorts) {
		t.Errorf("preview publishes %d ports, want %d", len(app.Ports), len(wantPorts))
	}
	for _, port := range app.Ports {
		if !wantPorts[port] {
			t.Errorf("unexpected or duplicate preview port %q", port)
		}
		delete(wantPorts, port)
	}
	for port := range wantPorts {
		t.Errorf("missing preview port %q", port)
	}

	pool := make([]int, 2)
	for i, key := range []string{"NOISEDECK_PROJECT_PREVIEWS_PORT_MIN", "NOISEDECK_PROJECT_PREVIEWS_PORT_MAX"} {
		value := strings.TrimSuffix(strings.TrimPrefix(app.Environment[key], "${"+key+":-"), "}")
		pool[i], err = strconv.Atoi(value)
		if err != nil {
			t.Fatalf("preview pool default %s = %q: %v", key, value, err)
		}
	}
	if size := pool[1] - pool[0] + 1; size != len(app.Ports) {
		t.Errorf("preview pool has %d ports for %d published slots", size, len(app.Ports))
	}
}
