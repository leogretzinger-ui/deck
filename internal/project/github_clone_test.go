package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubSelectionRevalidatedBeforeCloneCreatesDestination(t *testing.T) {
	for _, tc := range []struct {
		name, account, current string
		id                     int64
		refusal                bool
		code                   int
	}{
		{"access revoked", "operator", "", 42, true, http.StatusConflict},
		{"repository renamed", "operator", "https://github.com/org/new.git", 42, false, http.StatusConflict},
		{"missing account", "", "", 42, false, http.StatusBadRequest},
		{"missing id", "operator", "", 0, false, http.StatusBadRequest},
		{"negative id", "operator", "", -1, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, root := cloneHandler(t)
			dest := filepath.Join(root, "imported")
			h.SetGitHubRepositoryResolver(func(ctx context.Context, account string, id int64) (string, error) {
				if account != "operator" || id != 42 {
					t.Fatalf("unexpected selection %q %d", account, id)
				}
				if tc.refusal {
					return "", errors.New("GitHub access could not be confirmed. Refresh the repository list.")
				}
				return tc.current, nil
			})
			body, _ := json.Marshal(map[string]any{"url": "https://github.com/org/old.git", "destination": dest, "github_account": tc.account, "github_repository_id": tc.id})
			rec := postJSON(t, h.StartClone, "/api/projects/clone", string(body))
			if rec.Code != tc.code {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("refused selection changed destination: %v", err)
			}
			if jobs := h.clones.List(); len(jobs) != 0 {
				t.Fatalf("refused selection created jobs: %d", len(jobs))
			}
		})
	}
}

func TestGitHubSelectionStillUsesCloneRegistrationAndLocalIdentity(t *testing.T) {
	h, root := cloneHandler(t)
	h.SetGitHubRepositoryResolver(func(ctx context.Context, account string, id int64) (string, error) {
		if account != "github-login" || id != 42 {
			t.Fatalf("selection %q %d", account, id)
		}
		return "https://github.com/org/repo.git", nil
	})
	body, _ := json.Marshal(map[string]any{"url": "https://github.com/org/repo.git", "destination": filepath.Join(root, "repo"), "github_account": "github-login", "github_repository_id": 42, "git_author_name": "Commit Author", "git_author_email": "author@example.invalid"})
	rec := postJSON(t, h.StartClone, "/api/projects/clone", string(body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var job CloneJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	done := waitFor(t, h.clones, job.ID, CloneDone)
	p, err := h.repo.Get(context.Background(), done.ProjectID)
	if err != nil || p.GitAuthorName != "Commit Author" {
		t.Fatalf("registered identity %#v %v", p, err)
	}
}

func TestGitHubResumeRefusalPreservesPartialCopy(t *testing.T) {
	h, root := cloneHandler(t)
	if err := os.WriteFile(filepath.Join(root, "STALL"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	allowed := true
	h.SetGitHubRepositoryResolver(func(context.Context, string, int64) (string, error) {
		if !allowed {
			return "", errors.New("access revoked")
		}
		return "https://github.com/org/repo.git", nil
	})
	body, _ := json.Marshal(map[string]any{"url": "https://github.com/org/repo.git", "destination": filepath.Join(root, "repo"), "github_account": "operator", "github_repository_id": 42, "git_author_name": "Author", "git_author_email": "author@example.invalid"})
	rec := postJSON(t, h.StartClone, "/api/projects/clone", string(body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start %d %s", rec.Code, rec.Body)
	}
	var job CloneJob
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	partial := filepath.Join(root, "repo", ".git", "pack.tmp")
	waitForPartial(t, partial)
	if _, err := h.clones.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(partial)
	if err != nil {
		t.Fatal(err)
	}
	allowed = false
	request := httptest.NewRequest(http.MethodPost, "/api/project-clones/"+job.ID+"/resume", nil)
	request.SetPathValue("id", job.ID)
	reply := httptest.NewRecorder()
	h.ResumeClone(reply, request)
	if reply.Code != http.StatusConflict {
		t.Fatalf("resume %d %s", reply.Code, reply.Body)
	}
	after, err := os.ReadFile(partial)
	if err != nil || string(after) != string(before) {
		t.Fatalf("partial changed: %v", err)
	}
	stopped, err := h.clones.Get(job.ID)
	if err != nil || stopped.Status != CloneStopped {
		t.Fatalf("job changed: %+v %v", stopped, err)
	}
}
