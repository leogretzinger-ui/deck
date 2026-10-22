// Package session manages application session lifecycle: launching a provider
// CLI into a dedicated tmux session, recording its runtime identity, stopping
// it, and resuming it natively after death.
//
// A session's displayed status is always derived by reconciling recorded
// state with observed tmux/process state, never read straight from the
// database. Stopping targets the tmux session and process group and
// records the requested action, observed exit, and exit status.
//
// Provider prompts are never executed through an SDK adapter, and
// recovery never replays a prompt that may already have run.
package session
