package boot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/sealedstate"
)

// sealedStores is the store list the check runs against; tests swap it for
// fixture paths.
var sealedStores = sealedstate.DefaultStores

// SealedDestinations are the configured paths Deck persists into, resolved
// through any existing symlink prefix, keyed by what they are. The database
// file and its sidecars are listed as well as their directory, because a
// file symlink inside a sealed directory pointing at a plaintext mount is
// the same escape as a directory symlink. Each must lie inside a sealed
// store; the tmux socket is excluded because a socket holds no data and its
// directory is a tmpfs by design.
func SealedDestinations(cfg *config.Config) map[string]string {
	dest := map[string]string{
		"database":             sealedstate.ResolveExisting(filepath.Dir(cfg.Database.Path)),
		"database file":        sealedstate.ResolveExisting(cfg.Database.Path),
		"database wal":         sealedstate.ResolveExisting(cfg.Database.Path + "-wal"),
		"database shm":         sealedstate.ResolveExisting(cfg.Database.Path + "-shm"),
		"database-lock":        sealedstate.ResolveExisting(filepath.Dir(cfg.Database.LockPath)),
		"database-lock file":   sealedstate.ResolveExisting(cfg.Database.LockPath),
		"installation mode":    sealedstate.ResolveExisting(auth.InstallationModePath(cfg.Database.Path)),
		"authentication state": sealedstate.ResolveExisting(filepath.Join(filepath.Dir(cfg.Database.Path), "auth-state.json")),
	}
	if cfg.Server.UnixSocket != "" {
		dest["gateway socket directory"] = sealedstate.ResolveExisting(filepath.Dir(cfg.Server.UnixSocket))
	}
	for i, root := range cfg.Projects.AllowedRoots {
		dest[fmt.Sprintf("projects-root-%d", i)] = sealedstate.ResolveExisting(root)
	}
	return dest
}

// sealedSymlinkFailures refuses a database or lock path that is itself a
// symlink, whatever it points at: SQLite opens the configured name and
// follows the link, so a link is a destination the check did not verify.
func sealedSymlinkFailures(cfg *config.Config) []string {
	var failures []string
	for name, path := range map[string]string{
		"database file":      cfg.Database.Path,
		"database wal":       cfg.Database.Path + "-wal",
		"database shm":       cfg.Database.Path + "-shm",
		"database-lock file": cfg.Database.LockPath,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			failures = append(failures, fmt.Sprintf("configured %s (%s) is a symlink", name, path))
		}
	}
	sort.Strings(failures)
	return failures
}

// SealedCheck classifies the stores and the configured destinations. With
// required it returns an error naming every failure; without it the results
// are for logging only and the non-store failures come back as warnings.
func SealedCheck(cfg *config.Config, facts sealedstate.Facts, required bool) ([]sealedstate.Result, []string, error) {
	facts.SwapPolicy = sealedstate.SwapPolicy(cfg.Sealed.SwapPolicy)
	profile := sealedstate.Profile(cfg.Sealed.Profile)
	results, err := sealedstate.Check(facts, profile, sealedStores(), required)
	failures := append(sealedSymlinkFailures(cfg), sealedstate.DestinationFailures(results, SealedDestinations(cfg))...)
	if !required {
		return results, append(sealedstate.ProfileFailures(facts, profile), failures...), err
	}
	if err == nil && len(failures) == 0 {
		return results, nil, nil
	}
	msg := "sealedstate: encrypted state is required and these checks failed: " + strings.Join(failures, "; ")
	if err != nil {
		if len(failures) == 0 {
			return results, nil, err
		}
		msg = err.Error() + "; " + strings.Join(failures, "; ")
	}
	return results, nil, errors.New(msg)
}

// verifySealedState runs the encrypted-state check against the live kernel
// facts, or the injected ones. With sealed.require_encrypted_state it refuses
// to boot on any plaintext or unresolved store or any destination outside
// the sealed stores; without it, every store is logged at warn level when it
// is not sealed and at info level when it is, and every other failure the
// requirement would refuse is logged as a warning.
func verifySealedState(cfg *config.Config, read func(string) (sealedstate.Facts, error), log zerolog.Logger) error {
	if read == nil {
		read = sealedstate.KernelFacts
	}
	facts, err := read(cfg.Sealed.ReportPath)
	if err != nil {
		if cfg.Sealed.RequireEncryptedState {
			return err
		}
		log.Warn().Err(err).Msg("sealed state: could not read kernel facts; encrypted state is not required, continuing")
		return nil
	}
	results, warnings, err := SealedCheck(cfg, facts, cfg.Sealed.RequireEncryptedState)
	for _, r := range results {
		ev := log.Info()
		if !r.Sealed() {
			ev = log.Warn()
		}
		ev.Str("store", r.Store.Name).Str("path", r.Store.Path).Str("backing", string(r.Backing)).
			Str("fstype", r.Mount.FSType).Str("device", r.Mount.Device).Str("reason", r.Reason).
			Bool("nested", r.Nested).Bool("required", cfg.Sealed.RequireEncryptedState).Msg("sealed state")
	}
	for _, w := range warnings {
		log.Warn().Bool("required", false).Msg("sealed state: " + w)
	}
	return err
}
