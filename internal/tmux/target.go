package tmux

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// target builds an exact-match target usable for both session-scoped and
// pane-scoped tmux commands.
//
// The trailing ':' is required, not decorative. Measured on tmux 3.6a:
//
//	set-option -p -t =ac        -> "no such pane: =ac"
//	display-message -t =ac      -> succeeds, resolves to NOTHING (empty output)
//	display-message -t =ac:     -> resolves to session "ac"
//
// The middle case is the dangerous one: a bare '=name' pane target does not
// error, it silently resolves to no pane, so a caller would read empty fields
// and conclude the session had no identity rather than that it had asked the
// wrong question. The ':' anchors the target to that session's current window
// and pane, and the '=' keeps the name match exact rather than a prefix.
func target(name string) string { return "=" + name + ":" }

// paneIDPattern matches tmux pane ids, which are always '%' followed by
// digits.
var (
	paneIDPattern       = regexp.MustCompile(`^%\d+$`)
	sessionIDPattern    = regexp.MustCompile(`^\$\d+$`)
	identityTextPattern = regexp.MustCompile(`^[A-Za-z0-9-]*$`)
)

// paneTarget builds a target for a specific pane.
//
// A session-scoped target such as "=name:" resolves the session's CURRENT
// window and pane, which is mutable: if the provider opens a window, or the
// active pane changes, that target silently moves. Every operation that
// reads or writes the provider's pane must therefore address the pane id
// captured at launch — which is also the pane id that forms part of session
// identity.
func paneTarget(paneID string) string { return paneID }

// ValidatePaneID rejects anything that is not a tmux pane id.
func ValidatePaneID(paneID string) error {
	if !paneIDPattern.MatchString(paneID) {
		return fmt.Errorf("tmux: %q is not a pane id (expected %%N)", paneID)
	}
	return nil
}

// ValidateSessionName rejects names tmux would interpret as something other
// than a plain session name.
//
// ':' and '.' separate session, window, and pane in a tmux target, and
// '$', '%', '@', '=' introduce IDs or exact-match syntax. A name containing
// any of them could redirect an operation to a different target — which for
// this client means killing or writing into the wrong pane.
func ValidateSessionName(name string) error {
	if name == "" {
		return errors.New("tmux: session name is empty")
	}
	if len(name) > 200 {
		return errors.New("tmux: session name is too long")
	}
	if strings.ContainsAny(name, ":.$%@={}\n\t ") {
		return fmt.Errorf(
			"tmux: session name %q contains a character tmux treats as target "+
				"syntax (one of `:.$%%@={} `, whitespace, or newline)", name)
	}
	return nil
}
