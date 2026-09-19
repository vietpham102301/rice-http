package compare

import (
	"bytes"
	"net/http"
)

// discardWriter is an http.ResponseWriter that keeps a reusable header map and
// the status, and throws the body away. httptest.ResponseRecorder is not used
// in benchmarks: it grows a buffer per response, and those allocations would
// be charged to the framework.
type discardWriter struct {
	header http.Header
	status int
	n      int
}

func newDiscardWriter() *discardWriter { return &discardWriter{header: http.Header{}} }

func (w *discardWriter) Header() http.Header { return w.header }

func (w *discardWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *discardWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.n += len(b)
	return len(b), nil
}

// reset prepares w for the next request without allocating: clear keeps the
// map's buckets.
func (w *discardWriter) reset() {
	clear(w.header)
	w.status = 0
	w.n = 0
}

// rewindBody is a request body that can be re-armed without allocating.
type rewindBody struct{ bytes.Reader }

func (*rewindBody) Close() error { return nil }
