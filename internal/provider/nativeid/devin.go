package nativeid

// IsDevinSession accepts the lowercase two-word opaque slug emitted by the
// documented Devin SessionStart hook in the pinned CLI. Deck never assigns
// meaning to either component; the shape check only keeps malformed callback
// data out of durable identity state.
func IsDevinSession(id string) bool {
	if len(id) < 3 || len(id) > 128 {
		return false
	}
	hyphen := false
	componentStart := true
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			componentStart = false
		case r == '-' && !componentStart && !hyphen:
			hyphen = true
			componentStart = true
		default:
			return false
		}
	}
	return hyphen && !componentStart
}
