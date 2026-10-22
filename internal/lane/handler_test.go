package lane

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func serve(h *harness) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(h.svc, zerolog.Nop()).Mount(
		func(pattern string, hd http.Handler) { mux.Handle(pattern, hd) },
		func(next http.Handler) http.Handler { return next })
	return mux
}

func post(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandlerStartListAndDone(t *testing.T) {
	h := newHarness(t)
	mux := serve(h)
	base := "/api/projects/" + h.source.ID

	rec := post(mux, base+"/lanes", `{"tag":"main","task":1,"agent":"claude"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"phase":"open"`) {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "token") {
		t.Fatalf("response leaks a capability: %s", rec.Body)
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, base+"/lanes", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"lane_project_id"`) {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}

	if err := os.WriteFile(filepath.Join(h.engine.worktree, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = post(mux, base+"/taskmaster/tasks/1/done", `{"tag":"main"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"lane_dirty"`) {
		t.Fatalf("dirty done = %d %s", rec.Code, rec.Body)
	}
	if err := os.Remove(filepath.Join(h.engine.worktree, "x")); err != nil {
		t.Fatal(err)
	}
	rec = post(mux, base+"/taskmaster/tasks/1/done", `{"tag":"main"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"lane_closed":true`) {
		t.Fatalf("done = %d %s", rec.Code, rec.Body)
	}
}

func TestHandlerRejectsUnknownFieldsAndBadIDs(t *testing.T) {
	h := newHarness(t)
	mux := serve(h)
	base := "/api/projects/" + h.source.ID
	for _, tc := range []struct{ path, body string }{
		{base + "/lanes", `{"tag":"main","task":1,"agent":"claude","claim_token":5}`},
		{base + "/lanes", `{"tag":"main","task":1,"agent":"claude"} {}`},
		{base + "/taskmaster/tasks/1.2/done", `{"tag":"main"}`},
		{base + "/taskmaster/tasks/abc/done", `{"tag":"main"}`},
	} {
		if rec := post(mux, tc.path, tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s %s = %d, want 400", tc.path, tc.body, rec.Code)
		}
	}
	if rec := post(mux, "/api/projects/missing/lanes", `{"tag":"main","task":1,"agent":"claude"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
}

func TestHandlerMapsClaimAndStaleConflicts(t *testing.T) {
	h := newHarness(t)
	mux := serve(h)
	base := "/api/projects/" + h.source.ID
	h.engine.otherClaim = true
	rec := post(mux, base+"/lanes", `{"tag":"main","task":1,"agent":"claude"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"task_claimed"`) {
		t.Fatalf("claimed = %d %s", rec.Code, rec.Body)
	}
	h.engine.otherClaim = false
	rec = post(mux, base+"/taskmaster/tasks/1/done", `{"tag":"main","revision":0}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"stale_task"`) {
		t.Fatalf("stale = %d %s", rec.Code, rec.Body)
	}
}
