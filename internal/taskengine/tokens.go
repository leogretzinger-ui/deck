package taskengine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func engineTokenFlag(arg string) bool {
	switch arg {
	case "-claim-token", "-attempt-token", "-token":
		return true
	}
	return false
}

// engineTokenFiles keeps task capabilities out of the process table. The
// pinned engine reads these owner-only files before the controller removes
// them after the child has exited.
func engineTokenFiles(args []string) ([]string, func(), error) {
	private := append([]string(nil), args...)
	dir := ""
	cleanup := func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	for i := 0; i < len(private); i++ {
		if !engineTokenFlag(private[i]) {
			continue
		}
		if i+1 == len(private) {
			cleanup()
			return nil, nil, fmt.Errorf("missing engine token value")
		}
		if dir == "" {
			var err error
			dir, err = os.MkdirTemp("", "ciheal-engine-")
			if err != nil {
				return nil, nil, err
			}
		}
		path := filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(path, []byte(private[i+1]), 0o600); err != nil {
			cleanup()
			return nil, nil, err
		}
		private[i], private[i+1] = private[i]+"-file", path
		i++
	}
	return private, cleanup, nil
}

// Engine diagnostics can be copied into workflow logs and comments.
// Scrub capability values even if an engine failure repeats its inputs.
func engineTokenRedactor(args []string) func(string) string {
	var values []string
	for i := 0; i+1 < len(args); i++ {
		if engineTokenFlag(args[i]) && args[i+1] != "" {
			values = append(values, args[i+1])
			i++
		}
	}
	// A shorter numeric fence may be a prefix of another live fence.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	var pairs []string
	for _, value := range values {
		pairs = append(pairs, value, "[redacted]")
	}
	return strings.NewReplacer(pairs...).Replace
}

type engineCommandError struct {
	message string
	cause   error
}

func (e *engineCommandError) Error() string { return e.message }
func (e *engineCommandError) Unwrap() error { return e.cause }
