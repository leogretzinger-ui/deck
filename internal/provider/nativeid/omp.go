// Package nativeid validates provider-documented opaque identity shapes.
// It does not interpret provider state or assign meaning beyond the launch
// contract required before an identity can be replayed as one argv value.
package nativeid

import "regexp"

var ompUUIDv7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// IsOmpUUIDv7 reports whether id is the complete canonical UUIDv7 form emitted
// by omp's measured session_start hook.
func IsOmpUUIDv7(id string) bool {
	return ompUUIDv7.MatchString(id)
}
