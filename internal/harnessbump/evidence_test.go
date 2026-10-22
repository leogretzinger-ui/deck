package harnessbump

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const evidenceTestHead = "0123456789012345678901234567890123456789"

type evidenceFixture struct {
	artifact map[string]any
	run      map[string]any
	tip      string
	marker   string
}

func freshEvidenceFixture() evidenceFixture {
	return evidenceFixture{
		artifact: map[string]any{
			"id": 7, "name": EvidenceArtifact("cline", "3.0.61"), "expired": false,
			"workflow_run": map[string]any{"id": 42, "head_branch": "main"},
		},
		run: map[string]any{
			"path": BumpWorkflowPath, "event": "schedule", "head_branch": "main",
			"status": "completed", "conclusion": "success",
		},
		tip: evidenceTestHead, marker: "2\n",
	}
}

func evidenceZIP(t *testing.T, tip, marker string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries := map[string]string{"tip": tip + "\n", "probe.txt": "ok   --help names --tui\n"}
	if marker != "" {
		entries["probe-gate-version"] = marker
	}
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Exercise RunEvidence through its real gh boundary without network or
// credentials. Only the expected read-only API calls have fixture replies.
func evidenceGH(t *testing.T, artifacts []map[string]any, run map[string]any, archives map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, body []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), body, mode); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(name, raw, 0o600)
	}
	writeJSON("artifacts.json", map[string]any{"artifacts": artifacts})
	writeJSON("run.json", run)
	for name, raw := range archives {
		write(name, raw, 0o600)
	}
	write("gh", []byte(`#!/bin/sh
set -eu
case "$1:$2" in
  'api:repos/o/r/actions/artifacts?name=harness-bump-evidence-cline-3.0.61&per_page=100')
    cat "$EVIDENCE_TEST_DIR/artifacts.json" ;;
  'api:repos/o/r/actions/runs/42')
    cat "$EVIDENCE_TEST_DIR/run.json" ;;
  'api:repos/o/r/actions/artifacts/7/zip'|'api:repos/o/r/actions/artifacts/8/zip')
    test "$#" -eq 2
    id="${2%/zip}"
    id="${id##*/}"
    cat "$EVIDENCE_TEST_DIR/$id.zip" ;;
  *) echo 'unexpected evidence API call' >&2; exit 1 ;;
esac
`), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EVIDENCE_TEST_DIR", dir)
}

func TestRunEvidenceTrustsCompletedPerHarnessGates(t *testing.T) {
	tests := []struct {
		name   string
		change func(*evidenceFixture)
		want   bool
	}{
		{"successful run", func(*evidenceFixture) {}, true},
		{"other matrix harness failed", func(f *evidenceFixture) { f.run["conclusion"] = "failure" }, true},
		{"dispatch failed after upload", func(f *evidenceFixture) { f.run["conclusion"] = "failure" }, true},
		{"cancelled after upload", func(f *evidenceFixture) { f.run["conclusion"] = "cancelled" }, true},
		{"manual main run", func(f *evidenceFixture) { f.run["event"] = "workflow_dispatch" }, true},
		{"unfinished run", func(f *evidenceFixture) { f.run["status"] = "in_progress" }, false},
		{"queued run", func(f *evidenceFixture) { f.run["status"] = "queued" }, false},
		{"missing run status", func(f *evidenceFixture) { delete(f.run, "status") }, false},
		{"other workflow", func(f *evidenceFixture) { f.run["path"] = ".github/workflows/ci-fast.yaml" }, false},
		{"run on feature branch", func(f *evidenceFixture) { f.run["head_branch"] = "feature" }, false},
		{"artifact from feature branch", func(f *evidenceFixture) {
			f.artifact["workflow_run"] = map[string]any{"id": 42, "head_branch": "feature"}
		}, false},
		{"push run", func(f *evidenceFixture) { f.run["event"] = "push" }, false},
		{"pull request run", func(f *evidenceFixture) { f.run["event"] = "pull_request" }, false},
		{"expired artifact", func(f *evidenceFixture) { f.artifact["expired"] = true }, false},
		{"other harness artifact", func(f *evidenceFixture) { f.artifact["name"] = EvidenceArtifact("kimi", "3.0.61") }, false},
		{"other version artifact", func(f *evidenceFixture) { f.artifact["name"] = EvidenceArtifact("cline", "3.0.60") }, false},
		{"wrong tip", func(f *evidenceFixture) { f.tip = "abcdefabcdefabcdefabcdefabcdefabcdefabcd" }, false},
		{"empty tip", func(f *evidenceFixture) { f.tip = "" }, false},
		{"legacy marker absent", func(f *evidenceFixture) { f.marker = "" }, false},
		{"old marker", func(f *evidenceFixture) { f.marker = "1\n" }, false},
		{"unknown future marker", func(f *evidenceFixture) { f.marker = "3\n" }, false},
		{"incomplete marker", func(f *evidenceFixture) { f.marker = "2" }, false},
		{"extra marker bytes", func(f *evidenceFixture) { f.marker = "2\nextra" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := freshEvidenceFixture()
			tt.change(&f)
			evidenceGH(t, []map[string]any{f.artifact}, f.run, map[string][]byte{"7.zip": evidenceZIP(t, f.tip, f.marker)})
			got, err := RunEvidence("o/r")(context.Background(), Harness{Name: "cline"}, "3.0.61", evidenceTestHead)
			if err != nil || got != tt.want {
				t.Fatalf("evidence = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestRunEvidenceSkipsLegacyArtifactForNewProofOfSameHead(t *testing.T) {
	f := freshEvidenceFixture()
	next := freshEvidenceFixture().artifact
	next["id"] = 8
	f.run["conclusion"] = "failure"
	evidenceGH(t, []map[string]any{f.artifact, next}, f.run, map[string][]byte{
		"7.zip": evidenceZIP(t, f.tip, ""),
		"8.zip": evidenceZIP(t, f.tip, "2\n"),
	})
	got, err := RunEvidence("o/r")(context.Background(), Harness{Name: "cline"}, "3.0.61", evidenceTestHead)
	if err != nil || !got {
		t.Fatalf("new proof after legacy artifact = %v, %v", got, err)
	}
}

func TestRunEvidenceRequiresAnArtifact(t *testing.T) {
	f := freshEvidenceFixture()
	evidenceGH(t, nil, f.run, nil)
	got, err := RunEvidence("o/r")(context.Background(), Harness{Name: "cline"}, "3.0.61", evidenceTestHead)
	if err != nil || got {
		t.Fatalf("absent artifact = %v, %v", got, err)
	}
}

func TestRunEvidenceReportsCorruptArtifact(t *testing.T) {
	f := freshEvidenceFixture()
	evidenceGH(t, []map[string]any{f.artifact}, f.run, map[string][]byte{"7.zip": []byte("not a zip")})
	got, err := RunEvidence("o/r")(context.Background(), Harness{Name: "cline"}, "3.0.61", evidenceTestHead)
	if err == nil || got {
		t.Fatalf("corrupt artifact = %v, %v", got, err)
	}
}
