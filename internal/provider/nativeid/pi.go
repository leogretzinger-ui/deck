package nativeid

import "regexp"

var piUUIDv7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// IsPiUUIDv7 reports whether id is the complete canonical UUIDv7 form pi's
// session manager mints (createSessionId() is uuidv7()) and its measured
// session_start hook delivers.
func IsPiUUIDv7(id string) bool {
	return piUUIDv7.MatchString(id)
}
