//go:build unix

package main

import "syscall"

// Exec retains the invoking session's stdio and process identity, so signals
// and exit status belong to the dev command rather than a Deck supervisor.
func execPreview(path string, argv, environment []string) error {
	return syscall.Exec(path, argv, environment)
}
