package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/settings"
)

func tutorialConfig(t *testing.T) (dbPath, cfgPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "run", "control.db")
	cfgPath = filepath.Join(dir, "config.yaml")
	body := `
server:
  allowed_origins: ["http://console.test"]
auth:
  mode: "trusted_proxy"
  trusted_proxy:
    addresses: ["127.0.0.1"]
database:
  path: "` + dbPath + `"
projects:
  allowed_roots: ["` + dir + `"]
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOISEDECK_DATABASE_PATH", dbPath)
	return dbPath, cfgPath
}

func TestTutorialHistoryPrintsTimeSpentPerStep(t *testing.T) {
	dbPath, cfgPath := tutorialConfig(t)
	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	repo := settings.NewRepository(d)
	for _, p := range []settings.TutorialPreference{
		{Version: 1, Lesson: "lesson-1", Step: 0, Done: []string{}},
		{Version: 1, Lesson: "lesson-1", Step: 1, Done: []string{}},
		{Version: 1, Lesson: "lesson-2", Step: 0, Done: []string{"lesson-1"}},
	} {
		if err := repo.SetTutorial(ctx, "", p); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Close()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"tutorial", "history", "-config", cfgPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Installation record", "TIME SPENT", "lesson-1", "lesson-2", "current", "marked done or skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestTutorialHistoryWithoutRecords(t *testing.T) {
	dbPath, cfgPath := tutorialConfig(t)
	d, err := db.Open(context.Background(), db.Config{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"tutorial", "history", "-config", cfgPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no tutorial history recorded") {
		t.Errorf("output = %q", stdout.String())
	}
}

func TestPrintTutorialHistoryDurations(t *testing.T) {
	var out bytes.Buffer
	printTutorialHistory(&out, []settings.TutorialRecord{{Principal: "ann", History: []settings.TutorialStep{
		{Lesson: "a", Step: 0, At: "2026-01-02T03:04:05Z"},
		{Lesson: "a", Step: 1, At: "2026-01-02T03:06:35Z"},
	}}})
	for _, want := range []string{"User ann", "2m30s", "current"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// A done-only update leaves the position alone: the CLI keeps the real
// position as current and charges its time to it, not to the finished lesson.
func TestPrintTutorialHistoryDoneMarkerKeepsPosition(t *testing.T) {
	var out bytes.Buffer
	printTutorialHistory(&out, []settings.TutorialRecord{{History: []settings.TutorialStep{
		{Lesson: "b", Step: 0, At: "2026-01-02T03:00:00Z", Kind: "position"},
		{Lesson: "c", Step: 0, At: "2026-01-02T03:01:00Z", Kind: "done"},
		{Lesson: "b", Step: 1, At: "2026-01-02T03:03:00Z", Kind: "position"},
		{Lesson: "old", Step: 4, At: "2026-01-02T03:04:00Z"},
	}}})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("output:\n%s", out.String())
	}
	for i, want := range []string{"3m0s", "marked done or skipped", "1m0s", "current"} {
		if !strings.HasSuffix(lines[i+2], want) {
			t.Errorf("line %d = %q, want suffix %q", i+2, lines[i+2], want)
		}
	}
}
