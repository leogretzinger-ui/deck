package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileInstallsPinnedDevinCLI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		"ARG DEVIN_VERSION=3000.11.92",
		"ARG DEVIN_SHA256_AMD64=2d829139682e038cf122510f1170eee3d7060bcc7ebf92445cbc5d28c9fbcfa0",
		"ARG DEVIN_SHA256_ARM64=881e83b0df06dd57a307004e299407c2c3745f288546e0a339a67fbebe7cb6c0",
		"https://static.devin.ai/cli/${DEVIN_VERSION}/devin-${DEVIN_VERSION}-${devin_arch}-unknown-linux.tar.gz",
		`echo "${devin_sha256}  /tmp/devin.tar.gz" | sha256sum -c -`,
		`tar --no-same-owner -xzf /tmp/devin.tar.gz`,
		`ln -s "/opt/devin/${DEVIN_VERSION}/bin/devin" /usr/local/bin/devin`,
		`test "$(devin --version | awk '{print $2}')" = "${DEVIN_VERSION}"`,
		`grep -Eq -- "(^|[[:space:]])${flag}([[:space:],]|$)" /tmp/devin-help || exit 1`,
		"--resume",
		"--model",
		"--print",
		"--permission-mode",
		"--respect-workspace-trust",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Dockerfile is missing %q", required)
		}
	}
	if strings.Contains(body, "https://cli.devin.ai/install.sh") {
		t.Fatal("Dockerfile follows Devin's moving installer instead of the pinned artifact")
	}
}

func TestComposeExampleDocumentsDevinOperatorAuth(t *testing.T) {
	raw, err := os.ReadFile("compose.env.example")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		"devin auth login",
		"~/.local/share/devin/credentials.toml",
		`"attribution": false`,
		`"auto_update": false`,
		"~/.config/devin/config.json",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("compose.env.example is missing %q", required)
		}
	}
}
