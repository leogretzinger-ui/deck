package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/noisefloor-oss/deck/internal/lock"
	"github.com/noisefloor-oss/deck/internal/server/webassets"
	"github.com/noisefloor-oss/deck/internal/workspacehost"
	"github.com/noisefloor-oss/deck/internal/workspacemigrate"
)

func runWorkspace(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "serve" && args[0] != "setup-link" && args[0] != "invite" && args[0] != "import" && args[0] != "snapshot") {
		writeln(stderr, "usage: noise deck workspace serve|setup-link|snapshot|import --config /absolute/gateway.json")
		return 2
	}
	flags := flag.NewFlagSet("noise deck workspace "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "fixed gateway installation file")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *path == "" {
		return 2
	}
	if args[0] == "snapshot" {
		source, err := workspacemigrate.LoadSourceConfig(*path)
		if err != nil {
			writeln(stderr, "workspace snapshot configuration refused")
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		digest, err := workspacemigrate.Snapshot(ctx, source)
		if err != nil {
			switch {
			case errors.Is(err, workspacemigrate.ErrSource):
				writeln(stderr, "workspace snapshot refused: verify local source inventory, stopped containers, disabled restart policies and exclusive mounts")
			case errors.Is(err, workspacemigrate.ErrDatabase):
				writeln(stderr, "workspace snapshot refused: the copied Deck database requires reconciliation; preserve the original and output")
			default:
				writeln(stderr, "workspace snapshot verification failed: check source stability, permissions and supported file metadata; preserve source and output")
			}
			return 1
		}
		writef(stdout, "Workspace snapshot verified. Pin ManifestSHA256 to %s in the new installation.\n", digest)
		return 0
	}
	cfg, err := workspacehost.Load(*path)
	if err != nil {
		writeln(stderr, "workspace gateway configuration refused")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] == "import" {
		if err := workspacehost.Import(ctx, cfg); err != nil {
			if errors.Is(err, lock.ErrHeld) {
				writeln(stderr, "workspace import refused: stop the gateway before importing")
			} else {
				writeln(stderr, "workspace import refused; preserve source and inspect migration prerequisites")
			}
			return 1
		}
		writeln(stdout, "Workspace data import verified. Restart the gateway and resume owner setup.")
		return 0
	}
	if args[0] == "setup-link" || args[0] == "invite" {
		path, expires, err := workspacehost.Invite(ctx, cfg)
		if err != nil {
			writeln(stderr, "workspace setup link unavailable")
			return 1
		}
		writef(stdout, "Setup link saved to %s; expires %s. Remove this file after private handoff.\n", path, expires.UTC().Format(time.RFC3339))
		return 0
	}
	host, err := workspacehost.Open(ctx, cfg, webassets.FS(), buildVersion())
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			writeln(stderr, "workspace gateway startup refused: this installation already has a running gateway")
		} else {
			writeln(stderr, "workspace gateway startup refused")
		}
		return 1
	}
	defer host.Close()
	writeln(stdout, "Workspace security gateway starting; workload state is observed separately.")
	if host.Run(ctx) != nil {
		writeln(stderr, "workspace gateway stopped unexpectedly")
		return 1
	}
	return 0
}
