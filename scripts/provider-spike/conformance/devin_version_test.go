//go:build linux

package conformance

import "testing"

// The guard accepts one shape — the line the real CLI prints — and the
// contract's version as a whole token. Everything close to it is refused:
// a prefix or suffix version, a missing or malformed build, trailing text.
func TestDevinVersionLineAcceptsOnlyTheRealShape(t *testing.T) {
	if !devinVersionLine("devin 3000.6.14 (18033302)\n", "3000.6.14") {
		t.Fatal("the real --version line was refused")
	}
	for _, line := range []string{
		"devin 3000.6.14",
		"devin 3000.6.140 (18033302)",
		"devin 3000.6.1 (18033302)",
		"devin 3000.6.14 (18033302) extra",
		"devin 3000.6.14 18033302",
		"devin 3000.6.14 ()",
		"devin 3000.6.14 (18033302",
		"devin 3000.6.14 18033302)",
		"cognition 3000.6.14 (18033302)",
		"",
	} {
		if devinVersionLine(line, "3000.6.14") {
			t.Errorf("%q was accepted", line)
		}
	}
}
