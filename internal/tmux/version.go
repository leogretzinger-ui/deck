package tmux

import (
	"regexp"
	"strconv"
)

// MinimumVersion supports DEC 2026 synchronized redraws in the browser.
const MinimumVersion = "3.4"

var versionPattern = regexp.MustCompile(`^tmux (?:next-)?([0-9]+)\.([0-9]+)(?:[a-z]|-rc[0-9]*)?(?:\s|$)`)

func SupportedVersion(output string) bool {
	parts := versionPattern.FindStringSubmatch(output)
	if parts == nil {
		return false
	}
	major, majorErr := strconv.Atoi(parts[1])
	minor, minorErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > 3 || major == 3 && minor >= 4
}
