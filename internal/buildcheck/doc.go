// Package buildcheck exists only to hold toolchain tests.
//
// The scaffold's third-party dependencies (SQLite, WebSocket, structured
// logging) are not yet imported by the packages that will use them. Rather
// than record unused requires in go.mod — which `go mod tidy` would strip,
// and which would prove nothing — the tests here exercise each dependency so
// the requires are real and the toolchain is verified.
//
// The SQLite check matters most: github.com/mattn/go-sqlite3 is a cgo package,
// so a passing test proves this environment has a working C toolchain. It runs
// against a file-backed database and reads the settings back, so it establishes
// that WAL, synchronous=FULL, the busy timeout, and foreign keys take effect
// here — not merely that the statements are accepted.
//
// This package contains no production code and must not acquire any. Delete it
// once every dependency is imported by the package that owns it.
package buildcheck
