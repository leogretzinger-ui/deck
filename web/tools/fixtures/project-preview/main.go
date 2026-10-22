// Command project-preview-fixture is the backend half of the project app
// preview acceptance harness (web/tools/project-app-preview-probe.mjs).
//
// It boots the REAL backend — the same config.Defaults/Validate/boot.Up/
// Run/Close path a deployment uses — against one scratch directory the
// harness created inside the authorizing worktree, with password
// authentication and the project app preview gateway configured for the
// harness's synthetic origins. The sample project directories are registered
// as plain projects in a fresh database before boot, so the gateway's
// targets are real project rows.
//
// Nothing here is a deployment. The sign-in password is a fixed synthetic
// constant shared with the harness (bcrypt-hashed at startup, never
// printed), the session signing secret is random per run, and shutdown is
// only ever SIGTERM → ctx cancel → Run drain → Close. Close never signals
// child processes; the harness asserts the sample dev servers survive it.
//
// Usage:
//
//	project-preview-fixture serve -plan plan.json
//	project-preview-fixture cert CERT KEY HOST...
//
// stdout is the harness protocol (one JSON object per line: ready, done);
// logs go to stderr.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/boot"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
)

// operatorPassword is the harness's synthetic credential. It names no person
// and unlocks nothing outside the scratch run; both halves of the harness
// embed it so no secret ever crosses an output stream.
const operatorPassword = "preview-acceptance-operator"

type samplePlan struct {
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Origin   string `json:"origin"`
	Upstream string `json:"upstream"`
}

type planFile struct {
	Run                   string       `json:"run"`
	Backend               string       `json:"backend"`
	PreviewBind           string       `json:"preview_bind"`
	PreviewMetadataSocket string       `json:"preview_metadata_socket"`
	DeckOrigin            string       `json:"deck_origin"`
	Loopback              bool         `json:"loopback"`
	LoopbackPort          int          `json:"loopback_port"`
	BaseDomain            string       `json:"base_domain"`
	PortMin               int          `json:"port_min"`
	PortMax               int          `json:"port_max"`
	Samples               []samplePlan `json:"samples"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "project-preview-fixture: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: project-preview-fixture serve -plan FILE | cert CERT KEY HOST [HOST...]")
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		planPath := fs.String("plan", "", "JSON plan file describing the synthetic run")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *planPath == "" {
			return errors.New("serve: -plan is required")
		}
		return serve(*planPath)
	case "cert":
		if len(args) < 4 {
			return errors.New("cert: usage project-preview-fixture cert CERT KEY HOST [HOST...]")
		}
		return generateCert(args[1], args[2], args[3:]...)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func serve(planPath string) error {
	raw, err := os.ReadFile(planPath) // #nosec G304 -- the harness writes this file beside the binary
	if err != nil {
		return err
	}
	var plan planFile
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}
	if plan.Run == "" || plan.Backend == "" || (plan.PreviewBind == "" && !plan.Loopback) || plan.DeckOrigin == "" || len(plan.Samples) == 0 {
		return errors.New("plan: run, backend, preview_bind, deck_origin and samples are required")
	}

	// Reuse the exact synthetic bcrypt hash across restarts: bootstrap
	// correctly rejects a different hash against an initialized database.
	hashPath := filepath.Join(plan.Run, "synthetic-password.hash")
	hash, err := os.ReadFile(hashPath) // #nosec G304 -- confined to the synthetic harness scratch directory
	if errors.Is(err, os.ErrNotExist) {
		hash, err = bcrypt.GenerateFromPassword([]byte(operatorPassword), bcrypt.DefaultCost)
		if err == nil {
			err = os.WriteFile(hashPath, hash, 0o600)
		}
	}
	if err != nil {
		return fmt.Errorf("prepare synthetic password hash: %w", err)
	}
	if err := auth.EnsureInstallationMode(auth.InstallationModePath(filepath.Join(plan.Run, "deck.db")), "password", false); err != nil {
		return err
	}

	ids, err := registerSamples(plan)
	if err != nil {
		return err
	}

	cfg := config.Defaults()
	cfg.Server.Bind = plan.Backend
	cfg.Server.AllowedOrigins = []string{plan.DeckOrigin}
	if plan.Loopback {
		origin, err := url.Parse(plan.DeckOrigin)
		if err != nil {
			return err
		}
		cfg.Server.AllowedOrigins = append(cfg.Server.AllowedOrigins, "http://localhost:"+origin.Port())
	}
	cfg.Auth.Mode = "password"
	cfg.Auth.PasswordHash = string(hash)
	// Match the cookie transport config.normalize derives from allowed origins.
	cfg.Auth.SecureCookies = !plan.Loopback
	cfg.Database.Path = filepath.Join(plan.Run, "deck.db")
	cfg.Database.LockPath = filepath.Join(plan.Run, "backend.lock")
	cfg.Tmux.SocketPath = filepath.Join(plan.Run, "tmux.sock")
	cfg.Tmux.SessionPrefix = "pvwfix-"
	cfg.Projects.AllowedRoots = []string{plan.Run}
	cfg.Reconciler.Interval = config.Duration(time.Second)
	// Host-installed harness binaries are not this fixture's subject; an
	// empty provider set is a valid configuration that offers no launches.
	cfg.Providers = config.Providers{}
	cfg.Chats.Executable = filepath.Join(plan.Run, "no-a2a")
	cfg.ProjectPreviews = config.ProjectPreviews{
		Bind:           plan.PreviewBind,
		Loopback:       plan.Loopback,
		LoopbackPort:   plan.LoopbackPort,
		MetadataSocket: plan.PreviewMetadataSocket,
		DeckOrigin:     plan.DeckOrigin,
		Unconfined:     true,
		BaseDomain:     plan.BaseDomain,
		PortMin:        plan.PortMin,
		PortMax:        plan.PortMax,
	}
	if plan.Loopback {
		cfg.ProjectPreviews.DeckOrigin = ""
	}
	if plan.BaseDomain == "" && !plan.Loopback {
		for i, s := range plan.Samples {
			cfg.ProjectPreviews.Targets = append(cfg.ProjectPreviews.Targets, config.PreviewTarget{
				ProjectID: ids[i],
				Origin:    s.Origin,
				Upstream:  s.Upstream,
			})
		}
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the harness plan is not a valid configuration:\n%w", err)
	}

	log := zerolog.New(os.Stderr).Level(zerolog.ErrorLevel).With().Timestamp().Logger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h, err := boot.Up(ctx, &cfg, log)
	if err != nil {
		return fmt.Errorf("boot: %w", err)
	}

	ready, err := json.Marshal(map[string]any{
		"ready":    true,
		"backend":  plan.Backend,
		"preview":  plan.PreviewBind,
		"projects": ids,
	})
	if err != nil {
		return err
	}
	fmt.Println(string(ready))

	runErr := make(chan error, 1)
	go func() { runErr <- h.Run(ctx) }()
	select {
	case err := <-runErr:
		h.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	if err := <-runErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		h.Close()
		return fmt.Errorf("drain: %w", err)
	}
	h.Close()
	fmt.Println(`{"done":true}`)
	return nil
}

// registerSamples records the sample directories as plain projects in a
// synthetic database before boot, reusing their IDs on restart. The database is closed again so
// boot.Up can take the exclusive backend lock and reopen it exactly as the
// real service would.
func registerSamples(plan planFile) ([]string, error) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Path: filepath.Join(plan.Run, "deck.db"), BusyTimeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open synthetic database: %w", err)
	}
	defer func() { _ = d.Close() }()
	repo := project.NewRepository(d)
	existing, err := repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list synthetic projects: %w", err)
	}
	ids := make([]string, 0, len(plan.Samples))
	for _, s := range plan.Samples {
		var registered *project.Project
		for _, p := range existing {
			if p.CanonicalPath == s.Dir {
				registered = p
				break
			}
		}
		if registered != nil {
			ids = append(ids, registered.ID)
			continue
		}
		p, err := repo.RegisterPlain(ctx, s.Name, s.Dir, []string{plan.Run})
		if err != nil {
			return nil, fmt.Errorf("register %s (%s): %w", s.Name, s.Dir, err)
		}
		ids = append(ids, p.ID)
	}
	return ids, nil
}

// generateCert writes a short-lived self-signed certificate covering the
// harness's synthetic hosts for the Node TLS edge in front of the backend.
// It exists only inside the harness scratch directory and dies with it.
func generateCert(certPath, keyPath string, hosts ...string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"noisedeck preview acceptance harness"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(4 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	if err := writePem(certPath, "CERTIFICATE", der); err != nil {
		return err
	}
	return writePem(keyPath, "PRIVATE KEY", keyDER)
}

func writePem(path, blockType string, der []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit synthetic fixture output, never overwrite an existing file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
