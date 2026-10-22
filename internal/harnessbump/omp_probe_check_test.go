package harnessbump

import (
	"context"
	"strings"
	"testing"
)

// A reduced fixture in the shape of omp v18.1.11's --help, read from the
// installed CLI on 2026-09-06: the headers and the lines this contract
// names, with the rest of the flag list left out. It pins the matcher
// against the layout omp documents — MESSAGES under ARGUMENTS, never on the
// USAGE line — not the CLI's full surface, which the harness-bump probe
// checks against the built image.
const ompHelp = `omp v18.1.11

USAGE
  $ omp [COMMAND]

ARGUMENTS
  MESSAGES   Messages to send (prefix files with @)

FLAGS
      --model=<value>   Model to use
      --hook=<value>    Hook path
      --resume=<value>  Resume a session
      --auto-approve    Auto approve
      --no-prewalk      Disable prewalk even if prewalk.enabled is set
`

func TestOmpContractCatchesAWithdrawnPositional(t *testing.T) {
	spec, err := LoadProbe("../..", "omp")
	if err != nil || spec == nil {
		t.Fatalf("LoadProbe(omp) = %+v, %v", spec, err)
	}
	run := func(out string) Runner {
		return func(context.Context, []string, string) (string, error) { return out, nil }
	}
	if _, err := Probe(context.Background(), spec, "18.1.11", run(ompHelp)); err != nil {
		t.Fatalf("omp's documented layout failed its own contract: %v", err)
	}
	withdrawn := strings.ReplaceAll(ompHelp, "  MESSAGES   Messages to send (prefix files with @)\n", "")
	if _, err := Probe(context.Background(), spec, "18.1.11", run(withdrawn)); err == nil {
		t.Fatal("a help without the positional passed the contract")
	}
}
