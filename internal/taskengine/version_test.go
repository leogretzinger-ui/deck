package taskengine

import (
	"os"
	"strings"
	"testing"
)

func TestVersionMatchesPackaging(t *testing.T) {
	for path, pin := range map[string]string{
		"../../Dockerfile": "ARG NOISE_TASKS_VERSION=" + Version,
		"../../Makefile":   "NOISE_TASKS_VERSION      ?= " + Version,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), pin+"\n") {
			t.Errorf("%s must pin Tasks %s", path, Version)
		}
	}
}
