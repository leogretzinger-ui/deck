// Package reconciler compares recorded database state with tmux and local
// process observations. Every pass inventories the managed namespace,
// joins panes to rows by Deck's per-generation identity marker, and verifies
// the complete local tuple before recording any positive lifecycle fact.
//
// Unknown, foreign, mismatched, or unverifiable runtimes are never adopted,
// signalled, written to, or killed. A lost Deck attachment becomes interrupted
// metadata without claiming provider death; a dead exact pane records only the
// exit evidence tmux preserved. An exact live terminal tuple may restore the
// current controller only until a durable recovery action targets that source
// generation, after which the predecessor remains historical.
//
// After observation, the recovery collaborator may consume one durable
// automatic successor opportunity for each lost verified generation. Candidate
// admission, execution, and controller publication are owned by the session
// layer; the reconciler supplies cadence and exact local observations.
// Every database transition and its content-free audit event commit together.
package reconciler
