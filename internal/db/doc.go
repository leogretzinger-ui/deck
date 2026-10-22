// Package db owns the SQLite control database: schema, migrations, and
// transactional access.
//
// This database is the sole authority for recorded state concerning projects,
// application sessions and their lifecycle, controller leases, Taskmaster
// executions, and lifecycle/audit events.
//
// It is not an authority on reality. A row saying a session is running is a
// record of what was last written, not evidence that a process exists; only
// reconciliation against tmux and the process table can establish that. Never
// present a value read from here as observed state.
//
// The database never stores terminal input or output, and it does not carry
// provider transcripts or token streams. It holds fencing tokens and
// session/audit records, so it is credential-sensitive.
package db
