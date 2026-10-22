//go:build !linux && !darwin

package tasks

import (
	"net"
	"os"
)

func runGenerationHandoff([]string) error {
	return ErrGenerationUnsupported
}

func generationHandoffExecutable() (string, error)                   { return "", ErrGenerationUnsupported }
func handoffSocketDirectory(*os.File) string                         { return "" }
func checkHandoffDirectory(*os.File) error                           { return ErrGenerationUnsupported }
func sendHandoffDescriptors(*net.UnixConn, *os.File, *os.File) error { return ErrGenerationUnsupported }
