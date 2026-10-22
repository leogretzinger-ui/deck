package harnessbump

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Verdict is what the jailed reviewer asked to post, after the host
// validated it: the review event and its body.
type Verdict struct {
	Event string `json:"event"`
	Body  string `json:"body"`
}

// MaxVerdictBytes bounds a body the host will post.
const MaxVerdictBytes = 64 << 10

var verdictOpening = map[string]string{
	"APPROVE":         "✅ VERDICT: APPROVE",
	"REQUEST_CHANGES": "❌ VERDICT: REQUEST_CHANGES",
	"COMMENT":         "",
}

// secretShapes are the token and key shapes a body must not carry: the
// reviewer saw the pull request, never a credential, so any of these is
// either an injection or a leak.
var secretShapes = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}|\bsk-[A-Za-z0-9_-]{20,}|\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.`)

// ParseVerdict reads the jail's capture: the event on the first line, the
// body after it. It refuses an unknown event, a body that does not open
// with the event's verdict line, an oversized body, and any secret shape.
func ParseVerdict(capture []byte) (Verdict, error) {
	if len(capture) > MaxVerdictBytes+64 {
		return Verdict{}, fmt.Errorf("harnessbump: verdict over %d bytes", MaxVerdictBytes)
	}
	event, body, found := bytes.Cut(capture, []byte("\n"))
	if !found {
		return Verdict{}, errors.New("harnessbump: verdict capture carries no body")
	}
	v := Verdict{Event: strings.TrimSpace(string(event)), Body: strings.TrimSpace(string(body))}
	opening, ok := verdictOpening[v.Event]
	if !ok {
		return Verdict{}, fmt.Errorf("harnessbump: verdict event %q is not APPROVE, REQUEST_CHANGES or COMMENT", v.Event)
	}
	if v.Body == "" {
		return Verdict{}, errors.New("harnessbump: verdict body is empty")
	}
	if opening != "" && !strings.HasPrefix(v.Body, opening) {
		return Verdict{}, fmt.Errorf("harnessbump: a %s verdict must open with %q", v.Event, opening)
	}
	if v.Event == "COMMENT" && strings.Contains(v.Body, "VERDICT:") {
		return Verdict{}, errors.New("harnessbump: a COMMENT carries no verdict line")
	}
	if secretShapes.MatchString(v.Body) {
		return Verdict{}, errors.New("harnessbump: verdict body carries a token or key shape")
	}
	return v, nil
}

// ReadCapture reads the jail's capture file the way a file the jail wrote
// must be read: no symlink is followed, only a regular file is accepted,
// its size is checked before a byte is read, and no more than the bound
// is read whatever the size claims.
func ReadCapture(path string) ([]byte, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("harnessbump: verdict capture: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("harnessbump: verdict capture is not a regular file")
	}
	if info.Size() > MaxVerdictBytes+64 {
		return nil, fmt.Errorf("harnessbump: verdict capture over %d bytes", MaxVerdictBytes)
	}
	return io.ReadAll(io.LimitReader(f, MaxVerdictBytes+65))
}
