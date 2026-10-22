package nativeid

import "testing"

func TestIsDevinSession(t *testing.T) {
	for _, id := range []string{"short-slug", "abcdefgh-abcdef", "word-verylongword"} {
		if !IsDevinSession(id) {
			t.Errorf("valid Devin id %q rejected", id)
		}
	}
	for _, id := range []string{"", "single", "-leading", "trailing-", "two--parts", "UPPER-case", "word-123"} {
		if IsDevinSession(id) {
			t.Errorf("invalid Devin id %q accepted", id)
		}
	}
}
