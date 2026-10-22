package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/noisefloor-oss/deck/internal/apppreview"
)

func runPreview(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		writeln(stdout, "usage: noise deck preview run [--project-id ID] [--socket PATH] [--config .deck/preview.json]")
		return 0
	}
	if len(args) == 0 || args[0] != "run" {
		writeln(stderr, "usage: noise deck preview run [--project-id ID] [--socket PATH] [--config .deck/preview.json]")
		return 2
	}
	flags := flag.NewFlagSet("noise deck preview run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectID := flags.String("project-id", os.Getenv("NOISEDECK_PROJECT_ID"), "registered Deck project ID (defaults to this session's project)")
	socket := flags.String("socket", os.Getenv("NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET"), "local preview metadata socket")
	configPath := flags.String("config", ".deck/preview.json", "project-owned preview command JSON, relative to the current directory")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		writeln(stderr, "noise deck preview run: declare command arguments in the project configuration")
		return 2
	}
	if err := previewRun(context.Background(), *socket, *projectID, *configPath); err != nil {
		writef(stderr, "noise deck preview run: %v\n", err)
		return 1
	}
	return 0
}

func previewRun(ctx context.Context, socket, projectID, configPath string) error {
	if socket == "" {
		var err error
		socket, err = apppreview.DefaultMetadataSocket()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(socket) {
		return errors.New("preview metadata socket must be an absolute path")
	}
	assignment, err := apppreview.ReadAssignment(ctx, socket, projectID)
	if err != nil {
		return err
	}
	config, err := apppreview.ReadLaunchConfig(configPath)
	if err != nil {
		return err
	}
	binary, err := exec.LookPath(config.Command[0])
	if err != nil {
		return err
	}
	if err := apppreview.CheckLaunchPort(assignment); err != nil {
		return err
	}
	return execPreview(binary, config.Command, apppreview.LaunchEnvironment(os.Environ(), assignment))
}
