package harnessbump

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrNoUpstreamIndex marks a harness whose releases the job cannot list.
var ErrNoUpstreamIndex = errors.New("harnessbump: no upstream release index")

// Resolver asks upstreams what they publish. The endpoints are fields so a
// test can point them at a local server.
type Resolver struct {
	HTTP            *http.Client
	NPMRegistry     string
	ClaudeReleases  string
	CursorInstaller string
	DevinManifest   string
}

// NewResolver returns a resolver on the real upstreams.
func NewResolver() *Resolver {
	return &Resolver{
		HTTP:            &http.Client{Timeout: 10 * time.Minute},
		NPMRegistry:     "https://registry.npmjs.org",
		ClaudeReleases:  "https://downloads.claude.ai/claude-code-releases",
		CursorInstaller: "https://cursor.com/install",
		DevinManifest:   "https://static.devin.ai/cli/current/manifest.json",
	}
}

var (
	semverToken      = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	cursorAssignment = regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+)?DOWNLOAD_URL=(.*)$`)
	cursorLabVersion = regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[0-9a-f]+$`)
)

// Latest returns the version the harness's upstream publishes today.
func (r *Resolver) Latest(ctx context.Context, h Harness) (string, error) {
	switch h.Source {
	case SourceNPM:
		return r.npmLatest(ctx, h.Package)
	case SourceClaudeReleases:
		body, err := r.get(ctx, r.ClaudeReleases+"/latest", 1<<10)
		if err != nil {
			return "", err
		}
		v := strings.TrimSpace(string(body))
		if !semverToken.MatchString(v) {
			return "", fmt.Errorf("harnessbump: claude release index returned %q, not a version", v)
		}
		return v, nil
	case SourceCursorInstaller:
		body, err := r.get(ctx, r.CursorInstaller, 1<<20)
		if err != nil {
			return "", err
		}
		return cursorInstallerVersion(body)
	case SourceManual:
		return "", ErrNoUpstreamIndex
	case SourceDevinManifest:
		body, err := r.get(ctx, r.DevinManifest, 1<<20)
		if err != nil {
			return "", err
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			return "", err
		}
		if !ValidVersion(manifest.Version) {
			return "", fmt.Errorf("invalid Devin manifest version %q", manifest.Version)
		}
		return manifest.Version, nil
	}
	return "", fmt.Errorf("harnessbump: unknown source %q", h.Source)
}

// cursorInstallerVersion reads the lab version from the installer script's
// DOWNLOAD_URL assignment. The value is parsed as a URL, never searched, so
// text that merely contains the download host cannot pass for it.
func cursorInstallerVersion(script []byte) (string, error) {
	assignments := cursorAssignment.FindAllSubmatch(script, -1)
	if len(assignments) == 0 {
		return "", errors.New("harnessbump: cursor installer has no DOWNLOAD_URL assignment")
	}
	version := ""
	for _, a := range assignments {
		v, err := cursorAssignmentVersion(string(a[1]))
		if err != nil {
			return "", err
		}
		if version != "" && v != version {
			return "", fmt.Errorf("harnessbump: cursor installer names lab versions %s and %s", version, v)
		}
		version = v
	}
	return version, nil
}

func cursorAssignmentVersion(value string) (string, error) {
	word, err := shellWord(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("harnessbump: cursor installer DOWNLOAD_URL: %w", err)
	}
	u, err := url.Parse(word)
	if err != nil {
		return "", fmt.Errorf("harnessbump: cursor installer DOWNLOAD_URL: %w", err)
	}
	if u.Scheme != "https" || u.Host != "downloads.cursor.com" || u.User != nil {
		return "", fmt.Errorf("harnessbump: cursor installer downloads from %q, not https://downloads.cursor.com", u.Scheme+"://"+u.Host)
	}
	segments := strings.Split(u.Path, "/")
	if len(segments) < 3 || segments[0] != "" || segments[1] != "lab" || !cursorLabVersion.MatchString(segments[2]) {
		return "", fmt.Errorf("harnessbump: cursor installer path %q names no lab version", u.Path)
	}
	return segments[2], nil
}

// shellWord returns the one quoted or unquoted token a shell assignment value
// holds, and refuses anything it would have to interpret further.
func shellWord(value string) (string, error) {
	if value == "" {
		return "", errors.New("empty value")
	}
	if q := value[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(value[1:], q)
		if end < 0 {
			return "", errors.New("unterminated quote")
		}
		if strings.TrimSpace(value[end+2:]) != "" {
			return "", errors.New("text after the quoted value")
		}
		word := value[1 : end+1]
		if q == '"' && strings.ContainsAny(word, "\\`") {
			return "", errors.New("escape or command substitution in the value")
		}
		return word, nil
	}
	if strings.ContainsAny(value, " \t\"'\\`;&|<>()") {
		return "", errors.New("not a single token")
	}
	return value, nil
}

func (r *Resolver) npmLatest(ctx context.Context, pkg string) (string, error) {
	// A scoped name keeps its slash; only the @ needs escaping in a path.
	body, err := r.get(ctx, r.NPMRegistry+"/"+strings.ReplaceAll(url.PathEscape(pkg), "%2F", "/")+"/latest", 1<<20)
	if err != nil {
		return "", err
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", fmt.Errorf("harnessbump: npm manifest for %s: %w", pkg, err)
	}
	if !ValidVersion(manifest.Version) {
		return "", fmt.Errorf("harnessbump: npm manifest for %s names version %q", pkg, manifest.Version)
	}
	return manifest.Version, nil
}

// Digests downloads each artifact of the harness at the version and returns
// its sha256, keyed by SHAArg. The digest is measured from the bytes, never
// copied from an upstream manifest, so the Dockerfile's check verifies the
// same thing the job saw.
func (r *Resolver) Digests(ctx context.Context, h Harness, version string) (map[string]string, error) {
	digests := map[string]string{}
	for _, a := range h.Artifacts {
		d, err := r.Digest(ctx, a.URL(version))
		if err != nil {
			return nil, fmt.Errorf("harnessbump: %s: %w", a.SHAArg, err)
		}
		digests[a.SHAArg] = d
	}
	return digests, nil
}

// Digest streams a URL through sha256.
func (r *Resolver) Digest(ctx context.Context, address string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", address, resp.Status)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, resp.Body); err != nil {
		return "", fmt.Errorf("GET %s: %w", address, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func (r *Resolver) get(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", address, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", address, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s: body over %d bytes", address, limit)
	}
	return body, nil
}

// Status is one harness's pin against its upstream.
type Status struct {
	Harness  string `json:"harness"`
	Pinned   string `json:"pinned"`
	Latest   string `json:"latest,omitempty"`
	Outdated bool   `json:"outdated"`
	Note     string `json:"note,omitempty"`
}

// Check compares every catalogue pin with its upstream. A harness without
// an index, or whose upstream cannot be reached, is reported in Note and
// never counted outdated: the job bumps what it can prove, not what it
// cannot read.
func Check(ctx context.Context, root string, r *Resolver, harnesses []Harness) ([]Status, error) {
	var out []Status
	for _, h := range harnesses {
		pinned, _, err := Pinned(root, h)
		if err != nil {
			return nil, err
		}
		s := Status{Harness: h.Name, Pinned: pinned}
		latest, err := r.Latest(ctx, h)
		switch {
		case errors.Is(err, ErrNoUpstreamIndex):
			s.Note = "no upstream release index; the pin moves by hand"
		case err != nil:
			s.Note = "upstream unreachable: " + err.Error()
		default:
			s.Latest = latest
			s.Outdated = latest != pinned
		}
		out = append(out, s)
	}
	return out, nil
}
