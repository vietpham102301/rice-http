package rice

import (
	"bytes"
	"io"
	"log"
	"os"
	"testing"
)

// TestMain silences the standard logger for the whole package.
//
// From M5 the default error handler logs every unhandled error and every
// recovered panic, and this package's tests produce a great many of both on
// purpose. Without this, a real failure is buried in expected noise.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// captureLog redirects the standard logger into a buffer for the duration of
// one test and returns a function yielding what was written. The output is
// restored to io.Discard on cleanup, not to os.Stderr, so a test that forgets
// to assert does not start leaking into the next one.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	return buf.String
}
