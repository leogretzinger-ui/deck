package terminal

// The reply allowlist: which terminal-emulator replies may be forwarded to
// the pane at all.
//
// A provider can make a real emulator answer its queries automatically —
// print ESC [ 6 n and the browser's emulator sends a cursor position
// report with no user involved. Those answers must flow, or the
// provider's redraw breaks. But the same channel could carry ANY bytes,
// which would let provider output synthesize user input. The
// allowlist is the boundary: machine replies pass, everything else is
// dropped before it reaches the gate.

// maxReplyBytes bounds one reply frame. Genuine replies are short (a DA
// reply is 7 bytes, a colour reply under 30); the cap admits a few
// concatenated replies and nothing resembling a payload.
const maxReplyBytes = 256

// filterReply reports whether data is one whole frame consisting entirely of
// complete allowlisted machine replies. The classifier is built fresh per
// frame and the frame must end at ground: a sequence split across frames, or
// a frame ending mid-sequence, is not a reply and is refused. It is pure: no
// gate, no ledger, no I/O.
func filterReply(data []byte) bool {
	if len(data) == 0 || len(data) > maxReplyBytes {
		return false
	}
	c := &replyClassifier{}
	events := c.feed(data)
	// The frame must end at ground: a partial sequence is not a reply,
	// and allowing one would let arbitrary bytes ride its continuation.
	if !c.atGround() {
		return false
	}
	for _, e := range events {
		if e != replyNone {
			return false
		}
	}
	return true
}
