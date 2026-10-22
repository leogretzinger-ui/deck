package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/doctor"
)

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("noise deck doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config file (same as serve)")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		writef(stderr, "noise deck doctor: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	cfg, err := config.Load(*configPath)
	var report doctor.Report
	if err != nil {
		report = doctor.Report{Checks: []doctor.Check{{Name: "config", Required: true, Message: err.Error(), Fix: "Correct your config file or NOISEDECK_* settings and run doctor again."}}}
	} else {
		report = doctor.Run(context.Background(), cfg, false)
	}
	if *asJSON {
		err = json.NewEncoder(stdout).Encode(report)
	} else {
		err = report.WriteText(stdout)
	}
	if err != nil {
		writef(stderr, "noise deck doctor: %v\n", err)
		return 1
	}
	if !report.OK {
		return 1
	}
	return 0
}
