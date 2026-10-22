// Package lock provides the exclusive backend lock: exactly one backend may
// own a control database and tmux socket, and one namespace never has two
// active backends.
//
// The startup sequence acquires it before anything else and keeps readiness
// false until it holds; a second backend must fail fast and visibly, unable
// to become ready or to inject, which is why Acquire is non-blocking —
// waiting politely for the lock would leave the second process looking alive
// while silently doing nothing.
//
// # Why flock
//
// Ownership must die with the process, unconditionally. BSD flock locks
// belong to the kernel's open file description, so they evaporate when the
// holder exits — including on SIGKILL, where no userspace cleanup runs.
// Measured on this host (ext4 under /workspace): a SIGKILLed holder's lock
// is immediately acquirable by the next process. That property removes the
// entire "stale lock recovery" problem, and with it the heuristics
// (pid-liveness probes, timeout takeovers) that make hand-rolled lockfiles
// go wrong.
//
// Two more measured properties this package leans on:
//
//   - flock excludes across separate open descriptions of the same file even
//     within one process, so a double Acquire in a single process fails like
//     a second backend would — and the tests exercise real contention
//     without needing a subprocess for every case.
//   - the lock file's byte content is irrelevant to the kernel. Acquire
//     writes the holder's PID into it purely as a post-mortem hint for a
//     human with lsof/ps. Nothing may ever read it back: the kernel owns
//     liveness, and a recorded PID is stale the moment it is written.
//
// # What Release does not do
//
// Release never unlinks the lock file. Unlink-on-release is the classic
// flock defect: another process can be blocked on (or about to open) the old
// inode while a third creates and locks a fresh file at the same path, after
// which two backends both believe they hold "the" lock. A lock file that
// persists between runs — holding nothing but the previous holder's stale
// PID hint — is the correct steady state.
//
// The caller keeps the returned lock for the life of the process. Dropping
// it to the garbage collector would eventually close the descriptor via the
// os.File finalizer and silently release exclusivity.
package lock
