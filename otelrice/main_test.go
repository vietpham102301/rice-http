package otelrice

import (
	"io"
	"log"
	"os"
	"testing"
)

// TestMain silences the standard logger, where rice reports the panics and
// errors these tests cause on purpose.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}
