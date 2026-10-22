package deploy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthModeHealthcheckComparesRenderedAndLiveModes(t *testing.T) {
	dir := t.TempDir()
	fakeCurl := `#!/bin/sh
set -eu
headers=
socket=
while [ "$#" -gt 0 ]; do
  case $1 in
    --dump-header) headers=$2; shift 2 ;;
    --unix-socket) socket=$2; shift 2 ;;
    *) shift ;;
  esac
done
test "$socket" = "${NOISEDECK_SERVER_UNIX_SOCKET:-}" || exit 2
if [ -n "$headers" ]; then
  printf 'HTTP/1.1 401 Unauthorized\r\nX-Noisedeck-Auth-Mode: %s\r\n\r\n' "$FAKE_AUTH_MODE" > "$headers"
fi
`
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(fakeCurl), 0o555); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("provider-hook", "noisedeck-auth-mode-healthcheck")
	for _, tt := range []struct {
		name     string
		rendered string
		live     string
		wantOK   bool
		socket   string
	}{
		{name: "matching oidc", rendered: "oidc", live: "oidc", wantOK: true},
		{name: "normalized legacy alias", rendered: "builtin", live: "oidc", wantOK: true},
		{name: "password fallback", rendered: "oidc", live: "password"},
		{name: "private socket", rendered: "oidc", live: "oidc", wantOK: true, socket: "/data/gateway/backend.sock"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", script)
			cmd.Env = []string{
				"PATH=" + dir + ":" + os.Getenv("PATH"),
				"NOISEDECK_AUTH_MODE=" + tt.rendered,
				"FAKE_AUTH_MODE=" + tt.live,
				"NOISEDECK_SERVER_UNIX_SOCKET=" + tt.socket,
			}
			out, err := cmd.CombinedOutput()
			if tt.wantOK {
				if err != nil {
					t.Fatalf("healthcheck failed: %v: %s", err, out)
				}
				return
			}
			if err == nil || !strings.Contains(string(out), "does not match") {
				t.Fatalf("healthcheck result = %v, output %q", err, out)
			}
		})
	}
}

func TestBootstrapRefusesAbsentRenderedAuthModeBeforeMutation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("provider-hook", "noisedeck-bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	reached := filepath.Join(dir, "reached")
	mutation := writeExecutable(t, dir, "mutation", "#!/bin/sh\ntouch "+reached+"\n")
	bootstrap := strings.NewReplacer(
		"/usr/local/bin/noisedeck-deployment-host-guard", mutation,
		"/usr/local/bin/noisedeck-provision", mutation,
		"/usr/bin/python3", mutation,
		"/usr/local/bin/noise", mutation,
	).Replace(string(raw))
	path := filepath.Join(dir, "bootstrap")
	if err := os.WriteFile(path, []byte(bootstrap), 0o555); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 78 {
		t.Fatalf("bootstrap result = %v, output %q", err, out)
	}
	if _, err := os.Stat(reached); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap mutated before auth-mode preflight: %v", err)
	}
}
