//go:build !linux

package main

import (
	"os"
	"syscall"
)

func stopSelf() error {
	return syscall.Kill(os.Getpid(), syscall.SIGSTOP)
}
