package terminal

import "strings"

// The reply-grammar state machine: decides, from the OUTBOUND input stream
// only, whether bytes are terminal-emulator machine replies.
//
// This file never sees terminal OUTPUT. Classifying our own client's input
// stream is not interpretation of provider content (the authority
// invariant); it is a fixed allowlist deciding which bytes a provider's
// device/status query may pull back from the browser's emulator without the
// user touching anything.
//
// A sequence is a machine reply ONLY while every byte of it stays inside
// the strict grammar of one: CSI bodies limited to parameter bytes, OSC
// bodies to a numeric selector then printable text, both bounded in
// length. The moment a byte deviates — a C0 control inside a sequence, a
// non-numeric OSC opener, an over-long body — the classifier resets and
// treats the remainder as non-reply, because real terminals abort mangled
// sequences and print the remainder onto the line (measured: ESC [ CAN c
// leaves "^[[^Xc" ON THE LINE while a grammar-trusting parser reads it as
// a clean DA reply).

// replyClass is one classification outcome for a byte run.
type replyClass int

const (
	replyNone  replyClass = iota // machine reply (nothing user-typed)
	replyOther                   // user content or anything else
)

// replyClassifier is a small state machine over ONE outbound frame. filterReply
// builds it fresh per frame and rejects any frame that does not end at ground,
// so no state ever persists across frames: a sequence split across WebSocket
// frames is NOT a reply and is refused wholesale. The security boundary is
// whole-frame, and this type must never grow state that a future change could
// mistake for surviving a frame boundary.
type replyClassifier struct {
	state  cstate
	params []byte // CSI parameter bytes, kept to classify the final byte
	// oscBuf holds the current OSC body (bounded by maxSequenceBytes) so
	// the terminator can validate the reply's EXACT shape — a colour
	// report and nothing else.
	oscBuf []byte
}

// maxSequenceBytes bounds how long any escape sequence may run before the
// classifier stops believing it is one. Genuine replies are short (a DA
// reply is 7 bytes, a colour reply under 30); a "sequence" still open at
// this length is ordinary input being eaten by a parser state.
const maxSequenceBytes = 64

type cstate int

const (
	csGround cstate = iota
	csEsc           // saw ESC
	csCSI           // in ESC [ ...
	csOSC           // in ESC ] ...
	csOSCEsc        // in OSC, saw ESC (ST is ESC \)
	csSS3           // saw ESC O
)

// feed classifies data byte by byte and reports the events observed, in
// order.
func (c *replyClassifier) feed(data []byte) []replyClass {
	var events []replyClass
	for _, b := range data {
		switch c.state {
		case csGround:
			switch {
			case b == 0x1b:
				c.state = csEsc
			case b >= 0x20:
				events = append(events, replyOther)
			default:
				// C0 controls, CR/LF, and paste content are user input.
				events = append(events, replyOther)
			}
		case csEsc:
			switch b {
			case '[':
				c.state, c.params = csCSI, c.params[:0]
			case ']':
				c.state, c.oscBuf = csOSC, c.oscBuf[:0]
			case 'O':
				c.state = csSS3
			default:
				// Alt+key or a stray ESC: user input.
				c.state = csGround
				events = append(events, replyOther)
			}
		case csCSI:
			switch {
			case b >= 0x40 && b <= 0x7e:
				// Final byte: classify the whole sequence. Paste guards
				// (CSI 200 ~ / 201 ~) fall through classifyCSI's default and
				// classify as user input, which they are.
				events = append(events, classifyCSI(c.params, b))
				c.state = csGround
			case (b >= '0' && b <= '9') || b == ';' || b == '?' || b == '<' ||
				b == '>' || b == '=' || b == ':' || b == ' ':
				// Parameter bytes a real reply may contain.
				if len(c.params) >= maxSequenceBytes {
					c.state = csGround
					events = append(events, replyOther)
					break
				}
				c.params = append(c.params, b)
			default:
				// A C0 control or any other byte inside a CSI: terminals
				// abort here and the remainder lands on the line.
				c.state = csGround
				events = append(events, replyOther)
				events = append(events, c.feed([]byte{b})...)
			}
		case csOSC:
			// OSC responses (colour queries) end at BEL or ST. The body is
			// accumulated, bounded, and validated WHOLE at the terminator:
			// a reply is a colour report and nothing else.
			switch {
			case b == 0x07:
				c.state = csGround
				if !validOSCReply(c.oscBuf) {
					events = append(events, replyOther)
				}
			case b == 0x1b:
				c.state = csOSCEsc
			case len(c.oscBuf) >= maxSequenceBytes:
				c.state = csGround
				events = append(events, replyOther)
				events = append(events, c.feed([]byte{b})...)
			case oscBodyByte(c.oscBuf, b):
				c.oscBuf = append(c.oscBuf, b)
			default:
				c.state = csGround
				events = append(events, replyOther)
				events = append(events, c.feed([]byte{b})...)
			}
		case csOSCEsc:
			c.state = csGround
			if b == '\\' && !validOSCReply(c.oscBuf) {
				events = append(events, replyOther)
			}
			if b != '\\' {
				events = append(events, replyOther)
				c.state = csEsc
			}
		case csSS3:
			// SS3 finals are keypad/arrow keys: user navigation.
			c.state = csGround
			events = append(events, replyOther)
		}
	}
	return events
}

// atGround reports that no escape sequence is mid-way through: a partial
// sequence is not a reply, and allowing one would let arbitrary bytes ride
// its continuation.
func (c *replyClassifier) atGround() bool { return c.state == csGround }

// oscBodyByte reports whether b may continue an OSC body: selector digits
// and separators, or — once the selector's first ';' has appeared — the
// printable text of a payload. A C0 control or anything else aborts the
// sequence, exactly as a real terminal aborts it.
func oscBodyByte(buf []byte, b byte) bool {
	if b >= '0' && b <= '9' || b == ';' {
		return true
	}
	if b >= 0x20 && b <= 0x7e {
		for _, c := range buf {
			if c == ';' {
				return true
			}
		}
	}
	return false
}

// validOSCReply reports that a complete OSC body is a genuine emulator
// colour report and nothing else: selector 10, 11, or 12 (default
// foreground/background/cursor), or 4 with a palette index, answering with
// rgb:hex/hex/hex (one to four hex digits per channel, what emulators
// actually emit). This is the reply boundary in concrete form: a numeric
// selector over ARBITRARY printable text — say a command line — is not a
// reply any emulator sends, and forwarding it would let provider output put
// words into the provider's own stdin.
func validOSCReply(b []byte) bool {
	s := string(b)
	var payload string
	switch {
	case strings.HasPrefix(s, "10;"), strings.HasPrefix(s, "11;"), strings.HasPrefix(s, "12;"):
		payload = s[3:]
	case strings.HasPrefix(s, "4;"):
		rest := s[2:]
		i := strings.IndexByte(rest, ';')
		if i <= 0 || !allDigits(rest[:i]) {
			return false
		}
		payload = rest[i+1:]
	default:
		return false
	}
	chans, ok := strings.CutPrefix(payload, "rgb:")
	if !ok {
		return false
	}
	parts := strings.Split(chans, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if len(p) < 1 || len(p) > 4 || !allHex(p) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func allHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isHexLetter := c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
		if !isDigit && !isHexLetter {
			return false
		}
	}
	return true
}

// classifyCSI decides one complete CSI sequence.
//
// Replies are ALLOWLISTED by exact shape, not by final byte. Judging on
// the final alone let `ESC [ space c`, `ESC [ : c`, `ESC [ 123 I`, and
// `ESC [ < M` pass as replies while real terminals printed them onto the
// line. A sequence is a reply only if its parameters match what an
// emulator actually emits for that reply; everything else is user
// input.
func classifyCSI(params []byte, final byte) replyClass {
	switch final {
	case 'c':
		// Device attributes: primary "?1;2c", secondary ">0;95;0c". Every
		// field must be a non-empty number — "?;c" and "?1;;2c" are not
		// replies any emulator emits, and they land on the line.
		if len(params) > 1 && (params[0] == '?' || params[0] == '>') &&
			numericFields(params[1:]) > 0 {
			return replyNone
		}
	case 'R':
		// Cursor position report: "12;40R" — exactly two numbers.
		if numericFields(params) == 2 {
			return replyNone
		}
	case 'n':
		// Device status report: "0n" (terminal OK) is the only reply an
		// emulator emits here, answering a "5n" query. Anything else is
		// typing.
		if string(params) == "0" {
			return replyNone
		}
	case 'I', 'O':
		// Focus in/out carry NO parameters.
		if len(params) == 0 {
			return replyNone
		}
	case 'M', 'm':
		// SGR mouse: "<0;4;5M" — '<' then exactly three numbers.
		if len(params) > 1 && params[0] == '<' && numericFields(params[1:]) == 3 {
			return replyNone
		}
	}
	// Arrows, home/end, delete (~), malformed lookalikes, and everything
	// unrecognized: user input. Conservative by design.
	return replyOther
}

// numericFields counts ';'-separated fields when every field is a
// non-empty run of digits, and reports 0 for anything else.
func numericFields(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	fields, digits := 1, 0
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == ';':
			if digits == 0 {
				return 0
			}
			fields, digits = fields+1, 0
		default:
			return 0
		}
	}
	if digits == 0 {
		return 0
	}
	return fields
}
