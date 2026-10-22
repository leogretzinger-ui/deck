// Command noisedeck runs the Deck backend service.
//
// The service owns HTTP, authenticated WebSockets, the SQLite control
// database, tmux/process supervision, input injection, reconciliation, and
// static asset serving. It is deliberately not the parent or lifetime owner
// of any provider CLI process: killing this service must leave every
// agent session running.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/boot"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/harnesslogin"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/tasks"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	if filepath.Base(os.Args[0]) == "xdg-open" {
		os.Exit(harnesslogin.BrowserOpener(os.Args[1:]))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && tasks.IsGenerationHandoffCommand(args[0]) {
		if err := tasks.RunGenerationHandoff(args[1:]); err != nil {
			writef(stderr, "noise deck task generation handoff: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "internal-task-exec-v1":
		if err := tasks.RunNativeTaskExec(args[1:]); err != nil {
			writeln(stderr, "native Tasks execution refused")
			return 1
		}
		return 0
	case "internal-clone-exec-v1":
		if err := project.RunNativeCloneExec(args[1:]); err != nil {
			writeln(stderr, "native clone execution refused")
			return 1
		}
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version":
		writeln(stdout, buildVersion())
		return 0
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "backup":
		if err := runBackup(args[1:], stdout, stderr); err != nil {
			writef(stderr, "noise deck backup: %v\n", err)
			return 1
		}
		return 0
	case "restore":
		if err := runRestore(args[1:], stdout, stderr); err != nil {
			writef(stderr, "noise deck restore: %v\n", err)
			return 1
		}
		return 0
	case "sealed-check":
		return runSealedCheck(args[1:], stdout, stderr)
	case "password":
		return runPassword(args[1:], stdout, stderr)
	case "auth-mode":
		return runAuthMode(args[1:], stdout, stderr)
	case "harnesses":
		return runHarnesses(args[1:], stdout, stderr)
	case "agent-handoff":
		return runAgentHandoff(args[1:], stdout, stderr)
	case "preview":
		return runPreview(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "workspace":
		return runWorkspace(args[1:], stdout, stderr)
	case "tutorial":
		return runTutorial(args[1:], stdout, stderr)
	default:
		// Preserve the first-release direct-binary invocation while operators
		// move scripts to the noise plugin spelling.
		if args[0] == "-version" {
			writeln(stdout, buildVersion())
			return 0
		}
		if args[0] == "-config" || args[0] == "--config" {
			return runServe(args, stdout, stderr)
		}
		writef(stderr, "noise deck: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	writeln(w, "usage: noise deck <command> [args...]")
	writeln(w, "\ncommands:")
	writeln(w, "  serve      run the Noisefloor Deck console")
	writeln(w, "  workspace  serve workspace security or issue an enrollment invitation")
	writeln(w, "  version    print the component version")
	writeln(w, "  doctor     check the tools and paths Deck uses")
	writeln(w, "  preview    run a project dev command with its current preview assignment")
	writeln(w, "  backup     create a consistent database backup")
	writeln(w, "  restore    restore a supported database backup")
	writeln(w, "  sealed-check  classify every store Deck writes to as sealed or not, from inside the container")
	writeln(w, "  password   reset your password locally while Deck is stopped")
	writeln(w, "  auth-mode  adopt or transition the durable installation mode")
	writeln(w, "  tutorial   print the local tutorial step history (tutorial history)")
	writeln(w, "  harnesses  inspect configured harnesses and subscription capacity (unconfined experiment)")
	writeln(w, "  agent-handoff  launch and await a same-project delegated agent (unconfined experiment)")
}

func writeln(w io.Writer, values ...any) {
	_, _ = fmt.Fprintln(w, values...)
}

func writef(w io.Writer, format string, values ...any) {
	_, _ = fmt.Fprintf(w, format, values...)
}

func runServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("noise deck serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		writef(stderr, "noise deck serve: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	// Configuration is loaded and validated fail-closed before anything
	// binds.
	cfg, err := config.Load(*configPath)
	if err != nil {
		writef(stderr, "noise deck %s: %v\n", buildVersion(), err)
		return 1
	}

	logger, err := newLogger(cfg.Logging)
	if err != nil {
		writef(stderr, "noise deck %s: %v\n", buildVersion(), err)
		return 1
	}
	logger.Info().Str("version", buildVersion()).Msg("noisedeck starting")

	// The startup sequence: lock, storage, tmux, providers, and the first
	// reconciliation pass, in that order, before readiness flips.
	// A refusal here — another backend holding the lock above all — is the
	// system working, not failing.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h, err := boot.Up(ctx, cfg, logger, boot.WithBuildID(buildVersion()), boot.WithSetupOutput(stderr))
	if err != nil {
		logger.Error().Err(err).Msg("startup refused")
		return 1
	}
	defer h.Close()
	logger.Info().Str("bind", cfg.Server.Bind).Msg("noisedeck ready")

	if err := h.Run(ctx); err != nil {
		logger.Error().Err(err).Msg("server exited")
		return 1
	}
	logger.Info().Msg("noisedeck stopped")
	return 0
}

// newLogger builds the process logger per config: structured, identifiers
// and transitions only, never secrets or terminal contents.
func newLogger(cfg config.Logging) (zerolog.Logger, error) {
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		return zerolog.Logger{}, fmt.Errorf("log level %q: %w", cfg.Level, err)
	}
	var w io.Writer = os.Stderr
	if cfg.Format == "console" {
		w = zerolog.ConsoleWriter{Out: os.Stderr}
	}
	return zerolog.New(w).Level(level).With().Timestamp().Logger(), nil
}

// buildVersion reports the ldflags-injected version, falling back to VCS
// metadata recorded by the Go toolchain.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return "dev+" + s.Value[:12]
		}
	}
	return version
}
