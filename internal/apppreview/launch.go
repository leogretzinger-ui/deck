package apppreview

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// LaunchConfig belongs to the project. Deck's server never reads or executes
// it; only the local helper invoked by the developer or agent does.
type LaunchConfig struct {
	Version int      `json:"version"`
	Command []string `json:"command"`
}

func ReadLaunchConfig(path string) (LaunchConfig, error) {
	f, err := os.Open(path) // #nosec G304 -- the helper explicitly reads its project-owned command declaration.
	if err != nil {
		return LaunchConfig{}, fmt.Errorf("read preview command: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return LaunchConfig{}, errors.New("preview command configuration must be a regular JSON file of at most 16 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	var config LaunchConfig
	if err := decoder.Decode(&config); err != nil {
		return LaunchConfig{}, fmt.Errorf("read preview command: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || config.Version != 1 || len(config.Command) == 0 || len(config.Command) > 128 || strings.TrimSpace(config.Command[0]) == "" {
		return LaunchConfig{}, errors.New("preview configuration requires version 1 and a nonempty command argv array")
	}
	for _, arg := range config.Command {
		if strings.ContainsRune(arg, '\x00') {
			return LaunchConfig{}, errors.New("preview command arguments cannot contain NUL")
		}
	}
	return config, nil
}

// CheckLaunchPort catches a server already using the assignment. The probe
// releases the socket before exec: the dev command must still bind strictly
// and fail on a subsequent collision instead of trying another port.
func CheckLaunchPort(a Assignment) error {
	if err := a.validate(a.ProjectID); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(a.Host, strconv.Itoa(a.Port)))
	if err != nil {
		return fmt.Errorf("assigned preview port %d is unavailable; stop the conflicting server before retrying: %w", a.Port, err)
	}
	return listener.Close()
}

// LaunchEnvironment replaces stale preview values without pretending to
// sanitize the ordinary credentials of the existing unconfined process.
func LaunchEnvironment(environment []string, a Assignment) []string {
	result := make([]string, 0, len(environment)+7)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "DECK_PREVIEW_") {
			result = append(result, entry)
		}
	}
	u, _ := url.Parse(a.Origin)
	port := originPort(u)
	return append(result,
		"DECK_PREVIEW_HOST="+a.Host,
		"DECK_PREVIEW_PORT="+strconv.Itoa(a.Port),
		"DECK_PREVIEW_ORIGIN="+a.Origin,
		"DECK_PREVIEW_HOSTNAME="+u.Hostname(),
		"DECK_PREVIEW_HTTPS_PORT="+port,
		"DECK_PREVIEW_SCHEME="+u.Scheme,
		"DECK_PREVIEW_ORIGIN_PORT="+port,
	)
}
