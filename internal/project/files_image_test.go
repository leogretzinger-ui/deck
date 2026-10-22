package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

var (
	pngHead  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")
	jpegHead = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifHead  = []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00")
	webpHead = []byte("RIFF\x1a\x00\x00\x00WEBPVP8 ")
	bmpHead  = []byte("BM\x3a\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00")
	// An ftyp box of 28 bytes: major brand avif, minor version, then the
	// compatible brands mif1, miaf, MA1B.
	avifHead = []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00mif1miafMA1B")
	// The same box shape with an AVIF image sequence named only among the
	// compatible brands.
	avisCompatHead = []byte("\x00\x00\x00\x1cftypmif1\x00\x00\x00\x00miafavisMA1B")
	svgBody        = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	htmlBody       = []byte("<!doctype html><html><script>alert(document.cookie)</script></html>")
)

func TestSniffImageTypeAllowlist(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
		ok   bool
	}{
		{"png", pngHead, "image/png", true},
		{"jpeg", jpegHead, "image/jpeg", true},
		{"gif", gifHead, "image/gif", true},
		{"webp", webpHead, "image/webp", true},
		{"bmp", bmpHead, "image/bmp", true},
		{"avif major brand", avifHead, "image/avif", true},
		{"avif sequence compatible brand", avisCompatHead, "image/avif", true},
		{"mp4 is not avif", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42"), "", false},
		{"ftyp box longer than the bytes read", []byte("\x00\x00\x01\x00ftypmif1\x00\x00\x00\x00avif"), "", false},
		{"icon is not allowlisted", []byte("\x00\x00\x01\x00\x01\x00\x10\x10"), "", false},
		{"svg", svgBody, "", false},
		{"html", htmlBody, "", false},
		{"text", []byte("just some words\n"), "", false},
		{"empty", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sniffImageType(tc.head)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("sniffImageType = %q, %t; want %q, %t", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFormatMiB(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{16 << 20, "16 MiB"},
		{46 << 20, "46 MiB"},
		{16<<20 + 1, "16.1 MiB"},
		{17<<20 + 300<<10, "17.3 MiB"},
	}
	for _, tc := range cases {
		if got := formatMiB(tc.n); got != tc.want {
			t.Errorf("formatMiB(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestImageFileHandler(t *testing.T) {
	h, id, dir := uploadHandler(t)
	write := func(rel string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	png := append(append([]byte{}, pngHead...), bytes.Repeat([]byte{0}, 64)...)
	write("pic.png", png)
	write("lies.png", htmlBody)
	write("logo.svg", svgBody)
	mkdir(t, filepath.Join(dir, "assets"))

	// A sparse file past the cap whose first bytes are a real PNG header,
	// so sniffing alone would serve it.
	big, err := os.Create(filepath.Join(dir, "huge.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := big.Write(pngHead); err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(46 << 20); err != nil {
		t.Fatal(err)
	}
	big.Close()

	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.png")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		project string
		path    string
		status  int
		code    string
		message string
	}{
		{name: "png served inline", project: id, path: "pic.png", status: http.StatusOK},
		{name: "png extension over html bytes", project: id, path: "lies.png", status: http.StatusUnsupportedMediaType, code: "not_image"},
		{name: "svg", project: id, path: "logo.svg", status: http.StatusUnsupportedMediaType, code: "not_image"},
		{name: "over the cap", project: id, path: "huge.png", status: http.StatusRequestEntityTooLarge, code: "too_large",
			message: "previews stop at 16 MiB; this file is 46 MiB"},
		{name: "symlink out of the project", project: id, path: "escape.png", status: http.StatusForbidden, code: "outside_project"},
		{name: "walks upward", project: id, path: "../secret.png", status: http.StatusForbidden, code: "outside_project"},
		{name: "missing", project: id, path: "nope.png", status: http.StatusNotFound, code: "not_found"},
		{name: "directory", project: id, path: "assets", status: http.StatusBadRequest, code: "not_a_file"},
		{name: "unknown project", project: "no-such-project", path: "pic.png", status: http.StatusNotFound, code: "not_found",
			message: "no such project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/projects/x/files/image?path="+url.QueryEscape(tc.path), nil)
			req.SetPathValue("id", tc.project)
			rec := httptest.NewRecorder()
			h.ImageFile(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %.200q", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				assertInlineImage(t, rec, "image/png", "pic.png", png)
				return
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("refusal content type = %q", rec.Header().Get("Content-Type"))
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("refusal body %q: %v", rec.Body.String(), err)
			}
			if body["error"] != tc.code {
				t.Fatalf("error = %q, want %q", body["error"], tc.code)
			}
			if tc.message != "" && body["message"] != tc.message {
				t.Fatalf("message = %q, want %q", body["message"], tc.message)
			}
		})
	}
}

func assertInlineImage(t *testing.T, rec *httptest.ResponseRecorder, contentType, leaf string, want []byte) {
	t.Helper()
	headers := map[string]string{
		"Content-Type":           contentType,
		"Content-Disposition":    `inline; filename=` + leaf,
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "private, no-store",
		"Content-Length":         strconv.Itoa(len(want)),
	}
	for name, value := range headers {
		if got := rec.Header().Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %d bytes, want the file's %d", rec.Body.Len(), len(want))
	}
}

func TestOpenImageFileCapBoundary(t *testing.T) {
	_, _, dir := uploadHandler(t)
	big := filepath.Join(dir, "edge.png")
	if err := os.WriteFile(big, pngHead, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxImageViewBytes+1); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := OpenImageFile(dir, "edge.png")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if err.Error() != "previews stop at 16 MiB; this file is 16.1 MiB" {
		t.Fatalf("message = %q", err.Error())
	}
	if err := os.Truncate(big, MaxImageViewBytes); err != nil {
		t.Fatal(err)
	}
	f, info, ct, err := OpenImageFile(dir, "edge.png")
	if err != nil {
		t.Fatalf("a file at the cap must open: %v", err)
	}
	defer f.Close()
	if ct != "image/png" || info.Size() != MaxImageViewBytes {
		t.Fatalf("ct = %q size = %d", ct, info.Size())
	}
}

// growingWriter appends to the served file once the status line is
// committed: the file is already open and its size already announced, so
// whatever the handler copies after this point is the grown file.
type growingWriter struct {
	*httptest.ResponseRecorder
	grow func()
}

func (g *growingWriter) WriteHeader(code int) {
	g.grow()
	g.ResponseRecorder.WriteHeader(code)
}

func TestImageFileStopsAtAnnouncedLengthWhenFileGrows(t *testing.T) {
	h, id, dir := uploadHandler(t)
	png := append(append([]byte{}, pngHead...), bytes.Repeat([]byte{0}, 64)...)
	name := filepath.Join(dir, "growing.png")
	if err := os.WriteFile(name, png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &growingWriter{ResponseRecorder: httptest.NewRecorder(), grow: func() {
		if err := os.Truncate(name, MaxImageViewBytes+1); err != nil {
			t.Errorf("grow: %v", err)
		}
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/x/files/image?path=growing.png", nil)
	req.SetPathValue("id", id)
	h.ImageFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(png)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(png))
	}
	if rec.Body.Len() != len(png) || rec.Body.Len() > MaxImageViewBytes {
		t.Fatalf("body = %d bytes, want the announced %d", rec.Body.Len(), len(png))
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatal("body differs from the file as it was opened")
	}
}
