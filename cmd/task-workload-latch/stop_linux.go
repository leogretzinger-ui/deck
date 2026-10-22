package main

import (
	"os"
	"runtime"
	"syscall"
)

// A process-directed SIGSTOP may be taken by another thread, letting the caller
// reach Exec first. Stopping this exact thread makes the stop synchronous.
func stopSelf() error {
	runtime.LockOSThread()
	return syscall.Tgkill(os.Getpid(), syscall.Gettid(), syscall.SIGSTOP)
}
