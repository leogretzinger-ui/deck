// Package project implements the project registry: registering allowlisted
// directories, resolving canonical real paths, and listing sessions by project.
//
// Project paths must be selected from configured allowlisted roots, and are
// canonicalized and re-checked after symlink resolution. Traversal, missing
// directories, and directories not writable by the runtime user produce
// explicit errors rather than silent fallbacks.
//
// Project scope is organization and working-directory selection, not a
// security sandbox: a provider CLI can reach everything the operating-system
// user can. Removing a project never deletes its files.
package project
