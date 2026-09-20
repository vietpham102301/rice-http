package rice_test

import (
	"net/http"
	"testing"

	rice "github.com/vietpham102301/rice-http"
)

// TestNoContentSendsNoContentTypeOnTheWire reads the response a real client
// gets. fasthttp fills in a default Content-Type, so asserting on the response
// object would not show whether one is sent; RFC 9110 has no body to describe
// for a 204, and a proxy that sees a Content-Type on one may expect a body.
func TestNoContentSendsNoContentTypeOnTheWire(t *testing.T) {
	app := rice.New()
	app.DELETE("/items/:id", func(c *rice.Ctx) error { return c.NoContent(204) })
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

	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want none on a 204", ct)
	}
	if resp.ContentLength > 0 {
		t.Errorf("Content-Length = %d, want 0 or absent", resp.ContentLength)
	}
}
