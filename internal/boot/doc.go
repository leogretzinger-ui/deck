// Package boot assembles a validated configuration into a ready backend.
// Up in boot.go checks encrypted storage before creating state directories.
// It acquires the backend lock, opens and migrates the database, configures
// tmux and provider services, and completes initial reconciliation before
// wiring authentication and HTTP handlers.
// The lock comes before the database so a second backend refuses startup
// before touching shared state. Missing providers do not prevent startup.
// This package alone marks the backend ready after those checks pass.
//
// lifecycle.go serves requests and releases backend resources. Normal shutdown
// leaves tmux sessions and provider processes running. readiness.go rechecks
// the lock, database, tmux access, and reconciler recency after startup.
//
// providers.go constructs the configured CLI descriptors; auth.go maps login
// configuration and detects existing installation state. sealed.go verifies
// encrypted storage before startup can write persistent state. Project preview,
// workspace lock, and restore integration live in their corresponding files.
package boot
