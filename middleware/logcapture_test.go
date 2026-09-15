package middleware_test

import (
	"io"
	"log"
	"os"
	"testing"
)

// TestMain silences the standard logger for the whole package.
//
// rice.DefaultErrorHandler logs every recovered panic with a full stack trace,
// and this package's tests produce those on purpose. Without this silencing,
// a real test failure would be buried in the expected noise.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}
