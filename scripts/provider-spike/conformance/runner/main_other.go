//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the real Codex provider-contract probe requires Linux")
	os.Exit(1)
}
