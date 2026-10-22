package tmux

import (
	"context"
	"fmt"
)

// maxBytesPerSend bounds how many bytes go in one send-keys invocation, since
// each byte becomes its own argv element.
const maxBytesPerSend = 512

// SendLiteral writes text to the pane as literal input.
//
// The text is hex-encoded and delivered with `send-keys -H`, NOT passed as a
// string to `send-keys -l`. That is not defensive styling — `-l -- ";"` is
// accepted, reports success, and delivers NOTHING, because tmux treats a
// standalone ";" argument as a command-list separator even after "--"
// (measured on 3.6a; ";" inside a larger token such as "x;y" is unaffected).
// A pasted message consisting of a single semicolon would therefore be
// silently swallowed while the caller was told it succeeded — silent loss of
// user input, which is the one thing this product must never do.
//
// Hex encoding sidesteps tmux's argument parser entirely: every byte is
// delivered as itself. Nothing expands the text — no shell, no tmux key
// parsing — so pasted content cannot become a command.
//
// Long text is split across several invocations. A split is not atomic: a
// crash between chunks leaves partial input in the pane. That partial state
// is resolved by user inspection of the native TUI; this layer does not
// paper over it.
func (c *Client) SendLiteral(ctx context.Context, paneID, text string) error {
	if err := ValidatePaneID(paneID); err != nil {
		return err
	}
	return c.sendBytes(ctx, paneID, []byte(text))
}

// sendBytes delivers raw bytes to a pane via hex-encoded send-keys.
func (c *Client) sendBytes(ctx context.Context, paneID string, data []byte) error {
	for start := 0; start < len(data); start += maxBytesPerSend {
		end := start + maxBytesPerSend
		if end > len(data) {
			end = len(data)
		}
		args := make([]string, 0, 5+(end-start))
		args = append(args, "send-keys", "-t", paneTarget(paneID), "-H", "--")
		for _, b := range data[start:end] {
			args = append(args, fmt.Sprintf("%02x", b))
		}
		// Report every attempt before the non-transactional tmux boundary.
		// An error or timeout cannot prove that bytes were not delivered, so
		// observing only acknowledged writes would make a zero-write claim
		// unsafely optimistic.
		chunk := append([]byte(nil), data[start:end]...)
		c.observeWrite(WriteObservation{PaneID: paneID, Data: chunk})
		if _, err := c.run(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// SendKey sends one named key (for example "Enter" or "C-c"). The name is
// validated against an allowlist so caller data can never reach tmux's key
// parser.
func (c *Client) SendKey(ctx context.Context, paneID, key string) error {
	if err := ValidatePaneID(paneID); err != nil {
		return err
	}
	if !allowedKeys[key] {
		return fmt.Errorf("tmux: key %q is not in the permitted set", key)
	}
	c.observeWrite(WriteObservation{PaneID: paneID, Key: key})
	if _, err := c.run(ctx, "send-keys", "-t", paneTarget(paneID), key); err != nil {
		return err
	}
	return nil
}

// allowedKeys is the fixed set of named keys this client will send. Input
// text always goes through SendLiteral; this is only for control keys the
// product itself decides to send.
var allowedKeys = map[string]bool{
	"Enter": true, "Escape": true, "Tab": true, "BSpace": true,
	"Up": true, "Down": true, "Left": true, "Right": true,
	"C-c": true, "C-d": true, "C-u": true,
}

// Bracketed paste markers, sent as raw bytes so tmux does not interpret them.
var (
	pasteStart = []byte{0x1b, '[', '2', '0', '0', '~'}
	pasteEnd   = []byte{0x1b, '[', '2', '0', '1', '~'}
)

// SendBracketedPaste wraps text in bracketed-paste markers and sends it.
//
// The markers tell a TUI that the enclosed bytes are pasted content rather
// than typed keys, which is what stops a multi-line message from being
// interpreted as a sequence of submissions.
//
// Paste and submit are separate, non-transactional operations and either
// may fail; this function performs only the paste. A partial failure leaves
// the native TUI partially updated and is resolved by user
// inspection; this layer does not paper over it.
func (c *Client) SendBracketedPaste(ctx context.Context, paneID, text string) error {
	if err := ValidatePaneID(paneID); err != nil {
		return err
	}
	if err := ValidatePasteBody(text); err != nil {
		return err
	}
	if err := c.sendBytes(ctx, paneID, pasteStart); err != nil {
		return fmt.Errorf("tmux: send paste-start marker: %w", err)
	}
	if err := c.SendLiteral(ctx, paneID, text); err != nil {
		return fmt.Errorf("tmux: send paste body: %w", err)
	}
	if err := c.sendBytes(ctx, paneID, pasteEnd); err != nil {
		return fmt.Errorf("tmux: send paste-end marker: %w", err)
	}
	return nil
}

// ValidatePasteBody refuses text that could escape bracketed paste.
//
// The paste body is bounded by ESC[200~ and ESC[201~. A body containing the
// terminating sequence ends paste mode early, after which the REMAINING bytes
// arrive as ordinary typed input — so a crafted paste body could close the
// paste and then deliver keystrokes the provider treats as a real
// submission, or as an answer to whatever prompt is on screen. Paste content
// is attacker-influenced whenever the user pastes something they did not
// write.
//
// Rather than trying to neutralise escape sequences, this refuses any C0
// control character outside the set that legitimately appears in a message:
// newline, carriage return, and tab. That is a much smaller thing to get
// right, and there is no legitimate reason for pasted text to carry ESC.
func ValidatePasteBody(text string) error {
	for i, r := range text {
		switch r {
		case '\n', '\r', '\t':
			continue
		}
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf(
				"tmux: paste body contains control character %#x at byte %d; only "+
					"newline, carriage return, and tab are permitted (an embedded "+
					"ESC could terminate bracketed paste early and turn the rest of "+
					"the message into live keystrokes)", r, i)
		}
	}
	return nil
}
