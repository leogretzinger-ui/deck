package nativeid

import "regexp"

var (
	canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	kimiSessionID = regexp.MustCompile(`^session_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	openCodeID    = regexp.MustCompile(`^ses_[0-9A-Za-z]{26}$`)
)

// IsCanonicalUUID reports the complete lower-case UUID shape emitted by the
// measured Codex and Command Code launch hooks.
func IsCanonicalUUID(id string) bool { return canonicalUUID.MatchString(id) }

// IsKimiSession reports Kimi's documented session_-prefixed UUID shape.
func IsKimiSession(id string) bool { return kimiSessionID.MatchString(id) }

// IsOpenCodeSession reports the exact ses_ plus 26-base62 shape emitted by
// opencode and Kilo's measured plugin hooks.
func IsOpenCodeSession(id string) bool { return openCodeID.MatchString(id) }
