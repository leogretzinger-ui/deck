package project

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestUploadVanishedConflictIs409WithoutInventedVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Handler{}).fileError(rec, syscall.EEXIST)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "conflict" || body["existing"] != nil {
		t.Fatalf("vanished target must not offer an invented replacement: %v", body)
	}
}

// uploadHandler wires a real handler over a registered project and returns
// both, so an upload test drives the same path the server does.
func uploadHandler(t *testing.T) (*Handler, string, string) {
	t.Helper()
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	p, _, err := repo.Register(context.Background(), "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return NewHandler(repo, []string{root}, nil, zerolog.Nop()), p.ID, dir
}

func serveUpload(t *testing.T, h *Handler, id string, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.UploadFile(rec, req)
	return rec
}

func TestUploadRefusesOversizeFileWith413OnBothPaths(t *testing.T) {
	t.Run("raw", func(t *testing.T) {
		h, id, dir := uploadHandler(t)
		body := bytes.NewReader(make([]byte, MaxUploadBytes+1))
		req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=big.bin", body)
		rec := serveUpload(t, h, id, req)
		assertUploadError(t, rec, http.StatusRequestEntityTooLarge, "too_large")
		assertNoFileStored(t, dir)
	})

	t.Run("multipart", func(t *testing.T) {
		h, id, dir := uploadHandler(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", "big.bin")
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := part.Write(make([]byte, MaxUploadBytes+1)); err != nil {
			t.Fatalf("write part: %v", err)
		}
		if err := mw.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=big.bin", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := serveUpload(t, h, id, req)
		assertUploadError(t, rec, http.StatusRequestEntityTooLarge, "too_large")
		assertNoFileStored(t, dir)
	})
}

func TestUploadRefusesOversizeMultipartRequestWith413(t *testing.T) {
	h, id, dir := uploadHandler(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormField("metadata")
	if err != nil {
		t.Fatalf("create form field: %v", err)
	}
	if _, err := part.Write(make([]byte, MaxRequestBytes+1)); err != nil {
		t.Fatalf("write form field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=big.bin", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := serveUpload(t, h, id, req)
	assertUploadError(t, rec, http.StatusRequestEntityTooLarge, "too_large")
	assertNoFileStored(t, dir)
}

// The allowance above MaxUploadBytes exists so MIME framing does not make
// a legal maximum-size file look oversize. This pins that a file at the
// limit still round-trips through the multipart path.
func TestUploadAcceptsMaxSizeFileThroughMultipart(t *testing.T) {
	h, id, dir := uploadHandler(t)
	blockedTemp := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedTemp, nil, 0o600); err != nil {
		t.Fatalf("create constrained temp fixture: %v", err)
	}
	t.Setenv("TMPDIR", blockedTemp)
	if probe, err := os.CreateTemp("", "multipart-probe"); err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Fatal("temp fixture still permits spill files")
	}

	payload := bytes.Repeat([]byte{'a'}, MaxUploadBytes)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "max.bin")
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if int64(buf.Len()) <= MaxUploadBytes {
		t.Fatalf("framing added nothing; the test is not exercising the allowance")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=max.bin", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := serveUpload(t, h, id, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("max-size upload refused: %d %s", rec.Code, rec.Body.String())
	}
	fi, err := os.Stat(filepath.Join(dir, "max.bin"))
	if err != nil {
		t.Fatalf("stat stored file: %v", err)
	}
	if fi.Size() != MaxUploadBytes {
		t.Fatalf("stored %d bytes, want %d", fi.Size(), MaxUploadBytes)
	}
}

func TestMultipartUploadRequiresExactlyOneFilePart(t *testing.T) {
	t.Run("form fields and one file", func(t *testing.T) {
		h, id, dir := uploadHandler(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("description", "brand asset"); err != nil {
			t.Fatalf("write form field: %v", err)
		}
		part, err := mw.CreateFormFile("file", "ignored-name.svg")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write([]byte("<svg/>")); err != nil {
			t.Fatalf("write file part: %v", err)
		}
		if err := mw.WriteField("note", "after file"); err != nil {
			t.Fatalf("write trailing form field: %v", err)
		}
		if err := mw.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=chosen.svg", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := serveUpload(t, h, id, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload with form fields = %d %s", rec.Code, rec.Body.String())
		}
		body, err := os.ReadFile(filepath.Join(dir, "chosen.svg"))
		if err != nil || string(body) != "<svg/>" {
			t.Fatalf("stored chosen path = %q, %v", body, err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		h, id, dir := uploadHandler(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("description", "no file"); err != nil {
			t.Fatalf("write form field: %v", err)
		}
		if err := mw.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=missing.bin", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := serveUpload(t, h, id, req)
		assertUploadError(t, rec, http.StatusBadRequest, "bad_request")
		assertNoFileStored(t, dir)
	})

	t.Run("duplicate", func(t *testing.T) {
		h, id, dir := uploadHandler(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for _, name := range []string{"first.bin", "second.bin"} {
			part, err := mw.CreateFormFile("file", name)
			if err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			if _, err := part.Write([]byte(name)); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		if err := mw.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=duplicate.bin", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := serveUpload(t, h, id, req)
		assertUploadError(t, rec, http.StatusBadRequest, "bad_request")
		assertNoFileStored(t, dir)
	})
}

func assertUploadError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("upload status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"`+code+`"`) {
		t.Fatalf("upload error = %s, want code %q", rec.Body.String(), code)
	}
}

func assertNoFileStored(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name() != ".git" {
			names = append(names, e.Name())
		}
	}
	if len(names) != 0 {
		t.Fatalf("refused upload left files behind: %s", strings.Join(names, ", "))
	}
}

// A listing reports each entry's OWN metadata. os.ReadDir's DirEntry.Info
// does not follow symlinks, so a link planted in the project pointing
// outside it discloses the link's size and mode, never the target's — and
// the link is not reported as a directory, so it cannot be descended
// into. Switching this to os.Stat would silently turn the listing into a
// metadata oracle for arbitrary host paths, which is what this pins.
func TestListFilesDoesNotDiscloseSymlinkTargetMetadata(t *testing.T) {
	root := projectWithFiles(t)
	outside := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(outside, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	listing, err := ListFiles(root, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var link *FileEntry
	for i := range listing.Entries {
		if listing.Entries[i].Name == "link" {
			link = &listing.Entries[i]
		}
	}
	if link == nil {
		t.Fatalf("link entry missing from listing: %+v", listing.Entries)
	}
	if link.Size == 4096 {
		t.Fatalf("listing reported the target's size: %+v", link)
	}
	if link.Size != int64(len(outside)) {
		t.Fatalf("size = %d, want the link's own %d", link.Size, len(outside))
	}
	if link.Dir {
		t.Fatalf("symlink reported as a directory: %+v", link)
	}
	if link.Mode == "rw-------" {
		t.Fatalf("listing reported the target's mode: %+v", link)
	}

	// And the escape guard still refuses to read through it.
	if _, err := ReadTextFile(root, "link"); err == nil {
		t.Fatal("reading through a planted symlink was allowed")
	}
}

// A directory whose entries vanish between readdir and Info is not this
// call's problem, but it must not abort the listing either.
func TestListFilesSkipsVanishedEntries(t *testing.T) {
	root := projectWithFiles(t)
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "gone"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	listing, err := ListFiles(root, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listing.Entries) == 0 {
		t.Fatal("listing aborted on a dangling symlink")
	}
	found := false
	for _, e := range listing.Entries {
		if e.Name == "dangling" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dangling symlink dropped from listing: %+v", listing.Entries)
	}
}

func TestFilesEventsStreamsEntryFingerprints(t *testing.T) {
	h, id, dir := uploadHandler(t)
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("one"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Chtimes(file, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatalf("set fixture mtime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		h.FilesEvents(w, r)
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/projects/x/files/events?path=.", nil)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events response = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)
	connected, err := reader.ReadString('\n')
	if err != nil || connected != ": connected\n" {
		t.Fatalf("connected event = %q, %v", connected, err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("connected separator: %v", err)
	}

	noEvent := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		noEvent <- line
	}()
	select {
	case line := <-noEvent:
		t.Fatalf("unchanged directory emitted data: %q", line)
	case <-time.After(filesEventsInterval + 300*time.Millisecond):
	}
	if err := os.WriteFile(file, []byte("changed"), 0o600); err != nil {
		t.Fatalf("change size: %v", err)
	}
	if line := readEventLineFrom(t, noEvent); line != "event: files-changed\n" {
		t.Fatalf("size event = %q", line)
	}
	// Consume the data payload and blank separator before waiting for the next event.
	readEventLine(t, reader)
	readEventLine(t, reader)
	if err := os.Chtimes(file, time.Unix(2, 0), time.Unix(2, 0)); err != nil {
		t.Fatalf("change mtime: %v", err)
	}
	if line := readEventLine(t, reader); line != "event: files-changed\n" {
		t.Fatalf("mtime event = %q", line)
	}
}

func TestFilesEventsNotifiesOnceWhenDirectoryDisappears(t *testing.T) {
	h, id, dir := uploadHandler(t)
	watched := filepath.Join(dir, "watched")
	if err := os.Mkdir(watched, 0o700); err != nil {
		t.Fatalf("mkdir watched: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		h.FilesEvents(w, r)
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/projects/x/files/events?path=watched", nil)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line := readEventLine(t, reader); line != ": connected\n" {
		t.Fatalf("connected event = %q", line)
	}
	readEventLine(t, reader)

	if err := os.Remove(watched); err != nil {
		t.Fatalf("remove watched: %v", err)
	}
	if line := readEventLine(t, reader); line != "event: files-changed\n" {
		t.Fatalf("disappearance event = %q", line)
	}
	readEventLine(t, reader)
	readEventLine(t, reader)

	second := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		second <- line
	}()
	select {
	case line := <-second:
		t.Fatalf("missing directory emitted a second event: %q", line)
	case <-time.After(filesEventsInterval + 300*time.Millisecond):
	}
}

func TestFilesEventsRefusesOutsidePath(t *testing.T) {
	h, id, _ := uploadHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := time.AfterFunc(100*time.Millisecond, cancel)
	defer stop.Stop()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/projects/x/files/events?path=../outside", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.FilesEvents(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"error":"outside_project"`) {
		t.Fatalf("outside events response = %d %s", rec.Code, rec.Body.String())
	}
}

// conflictBody decodes the 409 the upload endpoint answers a collision
// with, so a test can read the ask the way a client would.
func conflictBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("conflict body is not JSON: %v", err)
	}
	return body
}

func existingOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	existing, ok := body["existing"].(map[string]any)
	if !ok {
		t.Fatalf("conflict carries no existing object: %v", body)
	}
	return existing
}

// A collision is answered with the ask: the facts the user needs to
// choose, plus the opaque version that — sent back with replace=1 — is
// what approves the replacement. Size and modification time alone cannot
// authorize replacement of a different file at the same name.
func TestUploadConflictCarriesVersionAndConsentRoundTripReplaces(t *testing.T) {
	h, id, dir := uploadHandler(t)
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	rec := serveUpload(t, h, id, httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=README.md", strings.NewReader("new\n")))
	body := conflictBody(t, rec)
	if body["error"] != "conflict" || body["reason"] != "exists" {
		t.Fatalf("plain collision = %v", body)
	}
	existing := existingOf(t, body)
	version, ok := existing["version"].(string)
	if !ok || version == "" {
		t.Fatalf("conflict carries no version: %v", existing)
	}
	if existing["name"] != "README.md" || existing["dir"] != false {
		t.Fatalf("conflict names the wrong target: %v", existing)
	}

	replace := httptest.NewRequest(http.MethodPost,
		"/api/projects/x/files?path=README.md&replace=1&expected_version="+url.QueryEscape(version),
		strings.NewReader("replaced\n"))
	rec = serveUpload(t, h, id, replace)
	if rec.Code != http.StatusCreated {
		t.Fatalf("consented replace = %d %s", rec.Code, rec.Body.String())
	}
	stored, err := os.ReadFile(readme)
	if err != nil || string(stored) != "replaced\n" {
		t.Fatalf("replaced content = %q, %v", stored, err)
	}
}

// Replace is consent only with the version that names the file being
// destroyed: replace alone, a version alone, and a version for a target
// that changed since the approval are each refused, and the changed
// refusal carries a fresh version so the user can be re-asked.
func TestUploadReplaceDemandsTheVersionNamingTheLiveTarget(t *testing.T) {
	h, id, dir := uploadHandler(t)
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	rec := serveUpload(t, h, id, httptest.NewRequest(http.MethodPost,
		"/api/projects/x/files?path=README.md&replace=1", strings.NewReader("x")))
	assertUploadError(t, rec, http.StatusBadRequest, "bad_request")

	rec = serveUpload(t, h, id, httptest.NewRequest(http.MethodPost,
		"/api/projects/x/files?path=README.md&expected_version=1-abc", strings.NewReader("x")))
	assertUploadError(t, rec, http.StatusBadRequest, "bad_request")

	rec = serveUpload(t, h, id, httptest.NewRequest(http.MethodPost,
		"/api/projects/x/files?path=README.md&replace=1&expected_version=1-stale", strings.NewReader("x")))
	body := conflictBody(t, rec)
	if body["reason"] != "changed" {
		t.Fatalf("stale-version replace = %v, want changed refusal", body)
	}
	fresh, _ := existingOf(t, body)["version"].(string)
	if fresh == "" || fresh == "1-stale" {
		t.Fatalf("changed refusal carries no fresh version: %v", body)
	}
	stored, err := os.ReadFile(readme)
	if err != nil || string(stored) != "original\n" {
		t.Fatalf("refused replace disturbed the original: %q, %v", stored, err)
	}
}

// A directory holding the name is asked about, never replaced: the 409
// says a folder holds it, and consent naming the directory is refused
// the same way.
func TestUploadConflictOverDirectoryIsNeverConsentReplaceable(t *testing.T) {
	h, id, dir := uploadHandler(t)
	if err := os.Mkdir(filepath.Join(dir, "brand"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := serveUpload(t, h, id, httptest.NewRequest(http.MethodPost, "/api/projects/x/files?path=brand", strings.NewReader("x")))
	body := conflictBody(t, rec)
	if body["reason"] != "directory" {
		t.Fatalf("directory collision = %v", body)
	}
	version, _ := existingOf(t, body)["version"].(string)
	rec = serveUpload(t, h, id, httptest.NewRequest(http.MethodPost,
		"/api/projects/x/files?path=brand&replace=1&expected_version="+url.QueryEscape(version),
		strings.NewReader("x")))
	body = conflictBody(t, rec)
	if body["reason"] != "directory" {
		t.Fatalf("consented directory replace = %v, want directory refusal", body)
	}
	if info, err := os.Stat(filepath.Join(dir, "brand")); err != nil || !info.IsDir() {
		t.Fatalf("directory was disturbed: %+v, %v", info, err)
	}
}

func readEventLine(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		return line
	case <-time.After(2 * filesEventsInterval):
		t.Fatal("timed out waiting for event line")
		return ""
	}
}

func readEventLineFrom(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case line := <-lines:
		return line
	case <-time.After(2 * filesEventsInterval):
		t.Fatal("timed out waiting for file event")
		return ""
	}
}
