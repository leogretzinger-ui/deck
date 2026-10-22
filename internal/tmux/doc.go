// Package tmux wraps the tmux CLI within a dedicated Deck socket namespace.
// It creates and respawns panes, observes their process identity, and delivers
// literal terminal input. It never infers provider status from terminal output.
//
// Each application session owns one tmux session. The provider executable is
// the pane command, with remain-on-exit enabled, so input after CLI exit cannot
// fall through to an interactive shell. Session names and pane IDs can be
// reused; callers must verify the recorded process identity before signalling
// or removing a pane.
//
// Session termination policy lives in internal/session, where exit evidence
// can be recorded before a pane is removed. Runtime status reconciliation
// lives in internal/reconciler. Losing contact with tmux does not prove that
// the provider process exited.
//
// The client and command runner live in client.go. Server configuration and
// socket checks live in server.go; target validation lives in target.go.
// observe.go reports pane facts and reads or writes pane options. launch.go
// creates and respawns panes; spawn_recovery.go handles partial launches and
// post-spawn observation.
// remove.go contains identity-checked removal. Terminal writes live in
// input.go; pane capture and copy-mode operations live in scroll.go. Test observers
// and failure injection live in test_hooks.go.
package tmux
