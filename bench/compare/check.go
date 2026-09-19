package compare

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/valyala/fasthttp"
)

// Do sends one request to srv in-process and returns the status and body.
func Do(srv Server, method, path string, body []byte) (int, string) {
	if srv.HTTP != nil {
		w := httptest.NewRecorder()
		srv.HTTP.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return w.Code, w.Body.String()
	}
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(path)
	if body != nil {
		ctx.Request.SetBody(body)
	}
	srv.Fasthttp(&ctx)
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

// Check is the equivalence gate: it sends s to a fresh app of t and reports how
// the answer differs from what s expects. Every benchmark calls it first, so a
// broken adapter fails the run instead of producing a number for different
// work.
//
// One trailing newline is ignored: Echo's JSON encoder ends its output with
// one and the others do not, which is not a difference in the work done.
func Check(t Target, s Scenario) error {
	status, body := Do(t.Build(s.App), s.Method, s.Path, s.Body)
	if status != s.Status {
		return fmt.Errorf("%s %s: status %d, want %d", t.Name, s.Name, status, s.Status)
	}
	if s.StatusOnly {
		return nil
	}
	if got := strings.TrimSuffix(body, "\n"); got != s.Want {
		return fmt.Errorf("%s %s: body %q, want %q", t.Name, s.Name, got, s.Want)
	}
	return nil
}
