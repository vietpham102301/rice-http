package rice_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	rice "github.com/vietpham102301/rice-http"
)

// TestNoContentSendsNoContentTypeOnTheWire reads the response a real client
// gets. The getter ContentType() would not show whether NoContent's clear did
// anything, because fasthttp's getter substitutes a default
// (text/plain; charset=utf-8) whenever the field is unset — that default
// would mask a missing clear whether or not one happened. The assertion is
// only meaningful because each handler writes a body (and therefore a
// Content-Type) with JSON before calling NoContent: on the wire, fasthttp
// omits the Content-Type header for a zero-length body regardless of the
// field, so without a prior write this test could not fail even if the
// clear in NoContent were deleted. RFC 9110 has no body to describe for
// these statuses, and a proxy that sees a Content-Type on one may expect a
// body.
func TestNoContentSendsNoContentTypeOnTheWire(t *testing.T) {
	for _, code := range []int{204, 205, 304} {
		t.Run(fmt.Sprintf("%d", code), func(t *testing.T) {
			app := rice.New()
			app.DELETE("/items/:id", func(c *rice.Ctx) error {
				_ = c.JSON(200, map[string]string{"ok": "true"})
				return c.NoContent(code)
			})
			addr, _ := serve(t, app)

			req, err := http.NewRequest(http.MethodDelete, "http://"+addr+"/items/42", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != code {
				t.Errorf("status = %d, want %d", resp.StatusCode, code)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				t.Errorf("Content-Type = %q, want none on a %d", ct, code)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if len(body) != 0 {
				t.Errorf("body = %q, want empty on a %d", body, code)
			}
		})
	}
}

// TestClientIPOverARealConnection checks the accessor against a real socket,
// where the address comes from the connection rather than from a test fixture.
func TestClientIPOverARealConnection(t *testing.T) {
	app := rice.New()
	app.GET("/ip", func(c *rice.Ctx) error { return c.String(200, c.ClientIP().String()) })
	addr, _ := serve(t, app)

	resp, err := http.Get("http://" + addr + "/ip")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := string(body); got != "127.0.0.1" {
		t.Errorf("ClientIP() = %q, want 127.0.0.1", got)
	}
}
