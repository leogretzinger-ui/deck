package project

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := Canonicalize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAssetsInstructionsCreateAppendAndSkip(t *testing.T) {
	t.Run("absent file is created", func(t *testing.T) {
		dir := canonicalTempDir(t)
		added, err := AddAssetsInstructions(dir)
		if err != nil || !added {
			t.Fatalf("added=%v err=%v", added, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, AgentInstructionsFile))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != assetsInstructions {
			t.Fatalf("AGENTS.md = %q", got)
		}
		if !strings.Contains(string(got), "`.deck/assets/`") || !strings.Contains(string(got), "Assets panel") {
			t.Fatalf("block does not name the folder and the panel: %q", got)
		}
	})

	t.Run("existing content is kept and the block appended", func(t *testing.T) {
		dir := canonicalTempDir(t)
		prior := "# Project rules\n\nRun the tests before you commit."
		path := filepath.Join(dir, AgentInstructionsFile)
		if err := os.WriteFile(path, []byte(prior), 0o644); err != nil {
			t.Fatal(err)
		}
		added, err := AddAssetsInstructions(dir)
		if err != nil || !added {
			t.Fatalf("added=%v err=%v", added, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := prior + "\n\n" + assetsInstructions; string(got) != want {
			t.Fatalf("AGENTS.md = %q, want %q", got, want)
		}
	})

	t.Run("a file that carries the block is left alone", func(t *testing.T) {
		dir := canonicalTempDir(t)
		path := filepath.Join(dir, AgentInstructionsFile)
		edited := "# Rules\n\n" + assetsInstructionsStart + "\nThe person's own wording.\n" + assetsInstructionsEnd + "\n"
		if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			added, err := AddAssetsInstructions(dir)
			if err != nil || added {
				t.Fatalf("added=%v err=%v", added, err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != edited {
			t.Fatalf("AGENTS.md = %q, want it unchanged", got)
		}
	})

	t.Run("a link is not followed", func(t *testing.T) {
		dir := canonicalTempDir(t)
		target := filepath.Join(dir, "elsewhere.md")
		if err := os.WriteFile(target, []byte("other"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, AgentInstructionsFile)); err != nil {
			t.Fatal(err)
		}
		added, err := AddAssetsInstructions(dir)
		if err != nil || added {
			t.Fatalf("added=%v err=%v", added, err)
		}
		if got, _ := os.ReadFile(target); string(got) != "other" {
			t.Fatalf("link target = %q", got)
		}
	})
}

func TestRegisteringAProjectAddsTheAssetsInstructions(t *testing.T) {
	h, root := inspectHandler(t)
	dir := filepath.Join(root, "existing")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prior := "Keep it small.\n"
	if err := os.WriteFile(filepath.Join(dir, AgentInstructionsFile), []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	body := `{"name":"existing","path":"` + dir + `","plain":true}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	got, err := os.ReadFile(filepath.Join(dir, AgentInstructionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := prior + "\n" + assetsInstructions; string(got) != want {
		t.Fatalf("AGENTS.md = %q, want %q", got, want)
	}
}

func importRepository(t *testing.T, h *Handler, root, name, prior string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "init", "--quiet", "--initial-branch=main")
	if prior != "" {
		if err := os.WriteFile(filepath.Join(dir, AgentInstructionsFile), []byte(prior), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	body := `{"name":"` + name + `","path":"` + dir + `","git_author_name":"Test Author","git_author_email":"test@example.invalid"}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return filepath.Join(dir, AgentInstructionsFile)
}

func TestImportingARepositoryCreatesTheAssetsInstructions(t *testing.T) {
	h, root := inspectHandler(t)
	got, err := os.ReadFile(importRepository(t, h, root, "fresh", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != assetsInstructions {
		t.Fatalf("AGENTS.md = %q", got)
	}
}

func TestImportingARepositoryKeepsItsInstructionsAndAppendsOnce(t *testing.T) {
	h, root := inspectHandler(t)
	prior := "# Rules\n\nRun the tests before you commit.\n"
	path := importRepository(t, h, root, "existing", prior)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := prior + "\n" + assetsInstructions; string(got) != want {
		t.Fatalf("AGENTS.md = %q, want %q", got, want)
	}
	if added, err := AddAssetsInstructions(filepath.Dir(path)); err != nil || added {
		t.Fatalf("second pass added=%v err=%v", added, err)
	}
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), assetsInstructionsStart) != 1 || string(again) != string(got) {
		t.Fatalf("block repeated: %q", again)
	}
}
