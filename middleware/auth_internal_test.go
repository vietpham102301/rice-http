package middleware

import "testing"

// The helpers' guards against inputs their callers never pass today: cutScheme
// slices exactly len(scheme) bytes before comparing, and KeyAuth checks an
// empty Header before isToken. Pinned here so a future caller cannot rely on
// behaviour nobody tested.
func TestEqualFoldASCIIRejectsDifferentLengths(t *testing.T) {
	if equalFoldASCII([]byte("Basi"), "Basic") {
		t.Error("equalFoldASCII matched strings of different lengths")
	}
	if !equalFoldASCII([]byte("bAsIc"), "Basic") {
		t.Error("equalFoldASCII did not fold ASCII case")
	}
}

func TestIsTokenRejectsTheEmptyString(t *testing.T) {
	if isToken("") {
		t.Error("the empty string is not a header name")
	}
}
