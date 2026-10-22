package project

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func cloneHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	h := NewHandler(repo, []string{root}, nil, zerolog.Nop())
	h.clones.git = fakeGit(t)
	t.Cleanup(h.Shutdown)
	return h, root
}

func postJSON(t *testing.T, h http.HandlerFunc, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return rec
}

func TestStartCloneAdmitsOnlyRemoteURLs(t *testing.T) {
	h, root := cloneHandler(t)
	for _, url := range []string{"/etc", "file://" + root, "../x", "-oProxyCommand=x"} {
		rec := postJSON(t, h.StartClone, "/api/projects/clone", `{"url":"`+url+`","destination":"`+filepath.Join(root, "x")+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d %s", url, rec.Code, rec.Body)
		}
	}
	rec := postJSON(t, h.StartClone, "/api/projects/clone", `{"url":"https://github.com/fable/deck.git","destination":"`+filepath.Join(t.TempDir(), "x")+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outside the roots: %d %s", rec.Code, rec.Body)
	}
}

func TestCloneJobRunsToARegisteredProject(t *testing.T) {
	h, root := cloneHandler(t)
	body := `{"url":"https://github.com/fable/deck.git","destination":"` + filepath.Join(root, "deck") + `","git_author_name":"Test Author","git_author_email":"test@example.invalid"}`
	rec := postJSON(t, h.StartClone, "/api/projects/clone", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var job CloneJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	done := waitFor(t, h.clones, job.ID, CloneDone)
	if done.ProjectID == "" {
		t.Fatalf("done without a project: %+v", done)
	}
	p, err := h.repo.Get(context.Background(), done.ProjectID)
	if err != nil || p.CanonicalPath != filepath.Join(root, "deck") || p.GitAuthorEmail != "test@example.invalid" {
		t.Fatalf("registered project: %+v %v", p, err)
	}
	// The listing carries the finished job for the rail to show.
	rec = httptest.NewRecorder()
	h.ListClones(rec, httptest.NewRequest(http.MethodGet, "/api/project-clones", nil))
	var jobs []CloneJob
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil || len(jobs) != 1 || jobs[0].Status != CloneDone {
		t.Fatalf("list: %s %v", rec.Body, err)
	}
	// A second start into the same, now occupied, destination is a conflict.
	rec = postJSON(t, h.StartClone, "/api/projects/clone", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("occupied destination: %d %s", rec.Code, rec.Body)
	}
}

func TestCloneJobCancelResumeDiscoverAndDiscardOverHTTP(t *testing.T) {
	h, root := cloneHandler(t)
	stall := filepath.Join(root, "STALL")
	if err := os.WriteFile(stall, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, h.StartClone, "/api/projects/clone", `{"url":"git@github.com:fable/deck.git","destination":"`+filepath.Join(root, "deck")+`","git_author_name":"Test Author","git_author_email":"test@example.invalid"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var job CloneJob
	_ = json.Unmarshal(rec.Body.Bytes(), &job)
	waitForPartial(t, filepath.Join(root, "deck", ".git", "pack.tmp"))

	cancel := httptest.NewRequest(http.MethodPost, "/api/project-clones/"+job.ID+"/cancel", nil)
	cancel.SetPathValue("id", job.ID)
	rec = httptest.NewRecorder()
	h.CancelClone(rec, cancel)
	var stopped CloneJob
	if err := json.Unmarshal(rec.Body.Bytes(), &stopped); err != nil || stopped.Status != CloneStopped {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}

	// Discarding while nothing runs removes the partial directory.
	discard := httptest.NewRequest(http.MethodDelete, "/api/project-clones/"+job.ID, nil)
	discard.SetPathValue("id", job.ID)
	rec = httptest.NewRecorder()
	h.DiscardClone(rec, discard)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("discard: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "deck")); err == nil {
		t.Fatalf("the partial clone was removed")
	}
	get := httptest.NewRequest(http.MethodGet, "/api/project-clones/"+job.ID, nil)
	get.SetPathValue("id", job.ID)
	rec = httptest.NewRecorder()
	h.GetClone(rec, get)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a discarded job is gone: %d", rec.Code)
	}
}
