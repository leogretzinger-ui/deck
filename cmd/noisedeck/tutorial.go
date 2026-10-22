package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/settings"
)

func runTutorial(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "history" {
		writeln(stderr, "usage: noise deck tutorial history [-config path]")
		return 2
	}
	flags := flag.NewFlagSet("noise deck tutorial history", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config file")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		writef(stderr, "noise deck tutorial history: %v\n", err)
		return 1
	}
	ctx := context.Background()
	d, err := db.OpenBackupSource(ctx, db.Config{
		Path:        cfg.Database.Path,
		BusyTimeout: cfg.Database.BusyTimeout.Std(),
	})
	if err != nil {
		writef(stderr, "noise deck tutorial history: %v\n", err)
		return 1
	}
	defer func() { _ = d.Close() }()
	records, err := settings.NewRepository(d).TutorialHistories(ctx)
	if err != nil {
		writef(stderr, "noise deck tutorial history: %v\n", err)
		return 1
	}
	printTutorialHistory(stdout, records)
	return 0
}

func printTutorialHistory(w io.Writer, records []settings.TutorialRecord) {
	printed := false
	for _, rec := range records {
		if len(rec.History) == 0 {
			continue
		}
		if printed {
			writeln(w)
		}
		printed = true
		if rec.Principal == "" {
			writeln(w, "Installation record")
		} else {
			writef(w, "User %s\n", rec.Principal)
		}
		writef(w, "%-20s  %-40s  %4s  %s\n", "REACHED (UTC)", "LESSON", "STEP", "TIME SPENT")
		for i, e := range rec.History {
			if !e.IsPosition() {
				writef(w, "%-20s  %-40s  %4d  %s\n", e.At, e.Lesson, e.Step, "marked done or skipped")
				continue
			}
			spent := "current"
			for _, later := range rec.History[i+1:] {
				if later.IsPosition() {
					spent = tutorialSpent(e.At, later.At)
					break
				}
			}
			writef(w, "%-20s  %-40s  %4d  %s\n", e.At, e.Lesson, e.Step, spent)
		}
	}
	if !printed {
		writeln(w, "no tutorial history recorded")
	}
}

func tutorialSpent(from, to string) string {
	a, errA := time.Parse(time.RFC3339, from)
	b, errB := time.Parse(time.RFC3339, to)
	if errA != nil || errB != nil {
		return "unknown"
	}
	return fmt.Sprint(b.Sub(a).Round(time.Second))
}
