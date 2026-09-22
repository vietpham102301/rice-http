package middleware

import (
	"testing"
)

// TestValidRequestIDRejectsControlBytes pins that validRequestID rejects control
// bytes an attacker would use for log injection. The end-to-end test path can
// never deliver these bytes because fasthttp neutralises them (e.g., rewrites
// \n to a space), so this test pins the control itself, independent of fasthttp's
// current behaviour.
func TestValidRequestIDRejectsControlBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   []byte
		want bool
	}{
		{"newline", []byte("abc\nFORGED"), false},
		{"carriage return", []byte("abc\rFORGED"), false},
		{"tab", []byte("abc\tFORGED"), false},
		{"NUL byte", []byte("abc\x00FORGED"), false},
		{"valid id", []byte("valid-id_123"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := validRequestID(tc.id)
			if got != tc.want {
				t.Errorf("validRequestID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}
