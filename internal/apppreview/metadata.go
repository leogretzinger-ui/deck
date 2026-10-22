package apppreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	metadataVersion = 1
	metadataTimeout = 2 * time.Second
	metadataLimit   = 4096
)

// Assignment is launch configuration, never a browser credential or proof of
// process ownership. Projects sharing the unconfined UID can read it.
type Assignment struct {
	ProjectID string `json:"project_id"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Origin    string `json:"origin"`
}

type metadataRequest struct {
	Version   int    `json:"version"`
	ProjectID string `json:"project_id"`
}

type metadataResponse struct {
	Version    int         `json:"version"`
	Assignment *Assignment `json:"assignment,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// DefaultMetadataSocket is stable across working directories and already
// running agent sessions; it does not depend on per-session temporary paths.
func DefaultMetadataSocket() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot locate preview socket: set --socket explicitly")
	}
	return filepath.Join(home, ".local", "state", "noisedeck", "preview.sock"), nil
}

func decodeMetadata(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func validPreviewProjectID(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

func (a Assignment) validate(projectID string) error {
	if a.ProjectID != projectID || !validPreviewProjectID(a.ProjectID) || a.Port < 1 || a.Port > 65535 {
		return errors.New("invalid preview assignment")
	}
	if _, err := normalizeUpstream(net.JoinHostPort(a.Host, strconv.Itoa(a.Port))); err != nil {
		return errors.New("preview assignment must bind a loopback address")
	}
	origin, err := normalizeOrigin(a.Origin, true)
	if err != nil {
		return errors.New("invalid preview origin")
	}
	u, _ := url.Parse(a.Origin)
	if origin != a.Origin && (u.Scheme != "http" || u.Port() != "80" || origin+":80" != a.Origin) {
		return errors.New("invalid preview origin")
	}
	if validatePreviewOrigin(a.Origin, u.Scheme == "http") != nil {
		return errors.New("invalid preview hostname")
	}
	return nil
}

// ReadAssignment fetches current state on every invocation. The socket has no
// registration, ticket, process-launch, or general Deck API operations.
func ReadAssignment(ctx context.Context, socket, projectID string) (Assignment, error) {
	if !validPreviewProjectID(projectID) {
		return Assignment{}, errors.New("set --project-id or run from a Deck project session")
	}
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return Assignment{}, fmt.Errorf("preview metadata unavailable; confirm Deck is running and the socket path matches: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return Assignment{}, err
	}
	if err := json.NewEncoder(conn).Encode(metadataRequest{Version: metadataVersion, ProjectID: projectID}); err != nil {
		return Assignment{}, err
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return Assignment{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(conn, metadataLimit+1))
	if err != nil || len(raw) > metadataLimit {
		return Assignment{}, errors.New("invalid or incomplete preview metadata response")
	}
	var response metadataResponse
	if decodeMetadata(raw, &response) != nil || response.Version != metadataVersion || (response.Assignment == nil) == (response.Error == "") {
		return Assignment{}, errors.New("invalid preview metadata response")
	}
	if response.Error != "" {
		switch response.Error {
		case "preview_disabled":
			return Assignment{}, errors.New("enable this project's preview in Deck before starting its dev server")
		case "not_found":
			return Assignment{}, errors.New("preview project does not exist or was removed")
		default:
			return Assignment{}, errors.New("could not read the current preview assignment from Deck")
		}
	}
	if err := response.Assignment.validate(projectID); err != nil {
		return Assignment{}, err
	}
	return *response.Assignment, nil
}
