package main

import (
	"flag"
	"io"
	"os"
	"time"

	"github.com/noisefloor-oss/deck/internal/boot"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/sealedstate"
)

// runSealedCheck implements `noisedeck sealed-check`, the in-container half
// of the encrypted-state gate. It loads the same configuration serve loads,
// classifies every store and every configured destination, prints one line
// per store, and exits 0 only when the check passes.
//
// With -gate it is the bootstrap's hold: it takes serve's own arguments, so
// the requirement is decided by the configuration serve will read, in every
// spelling the loader accepts; when nothing requires encrypted state it
// logs the classification and returns at once, and when something does it
// waits for the host report before checking, so provisioning, MemPalace,
// the courier and every CLI stay unstarted until every store verifies.
func runSealedCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sealed-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to config file")
	gate := fs.Bool("gate", false, "bootstrap mode: honor the configured requirement and wait for the host report")
	wait := fs.Duration("wait", 120*time.Second, "how long -gate waits for the host report")
	profile := fs.String("profile", "", "storage profile to check against (default: config)")
	report := fs.String("report", "", "host report path (default: config)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		writef(stderr, "sealed-check: %v\n", err)
		return 1
	}
	if *profile != "" {
		cfg.Sealed.Profile = *profile
	}
	if *report != "" {
		cfg.Sealed.ReportPath = *report
	}
	required := true
	if *gate {
		required = cfg.Sealed.RequireEncryptedState
		if required {
			deadline := time.Now().Add(*wait)
			for {
				if _, err := os.Stat(cfg.Sealed.ReportPath); err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Second)
			}
		}
	}
	facts, err := sealedstate.KernelFacts(cfg.Sealed.ReportPath)
	if err != nil {
		writef(stderr, "sealed-check: %v\n", err)
		return 1
	}
	results, warnings, err := boot.SealedCheck(cfg, facts, required)
	for _, r := range results {
		writef(stdout, "%-34s %-11s %s %s %s\n", r.Store.Name, r.Backing, r.Mount.FSType, r.Mount.Device, r.Reason)
	}
	for _, w := range warnings {
		writef(stderr, "sealed-check: warning: %s\n", w)
	}
	if err != nil {
		writeln(stderr, err.Error())
		return 1
	}
	if !required {
		writef(stdout, "sealed: encrypted state is not required by this configuration; classification logged only (profile %s)\n", cfg.Sealed.Profile)
		return 0
	}
	writef(stdout, "sealed: every store and configured destination is backed by encrypted storage (profile %s)\n", cfg.Sealed.Profile)
	return 0
}
