package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPluginHelpNamesNoiseDeckCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run help = %d, stderr %q", code, stderr.String())
	}
	for _, want := range []string{"usage: noise deck", "serve", "version", "backup", "restore", "auth-mode", "preview", "doctor"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestPluginVersionVerbAndLegacyFlag(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"-version"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) = %d, stderr %q", args, code, stderr.String())
		}
		if strings.TrimSpace(stdout.String()) != buildVersion() {
			t.Fatalf("run(%q) output = %q, want %q", args, stdout.String(), buildVersion())
		}
	}
}

func TestPluginRefusesUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"launch"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run unknown = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "launch"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestServeRefusesUnexpectedArgumentsBeforeStartup(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"serve", "extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run serve extra = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unexpected argument "extra"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
