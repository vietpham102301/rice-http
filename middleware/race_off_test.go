//go:build !race

package middleware_test

// raceDetector reports whether this test binary was built with -race. See
// race_on_test.go.
const raceDetector = false
