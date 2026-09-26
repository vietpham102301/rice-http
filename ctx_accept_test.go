package rice

import (
	"slices"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// acceptCtx returns a Ctx bound to a request carrying one Accept line per
// argument, and none when there are no arguments.
func acceptCtx(accept ...string) *Ctx {
	fctx := &fasthttp.RequestCtx{}
	for _, a := range accept {
		fctx.Request.Header.Add(fasthttp.HeaderAccept, a)
	}
	c := &Ctx{}
	c.reset(nil, fctx)
	return c
}

func TestAcceptsMatchesByRFC9110(t *testing.T) {
	const (
		J = "application/json"
		H = "text/html"
		P = "text/plain; charset=utf-8"
	)
	var long strings.Builder
	for i := range 50 {
		long.WriteString("image/x-")
		long.WriteString(strings.Repeat("a", i+1))
		long.WriteString(", ")
	}
	long.WriteString("application/json")

	cases := []struct {
		name   string
		accept []string
		offers []string
		want   string
	}{
		{"no header", nil, []string{J, H}, J},
		{"empty header", []string{""}, []string{J, H}, J},
		{"blank header", []string{" \t"}, []string{H, J}, H},
		{"exact", []string{"text/html"}, []string{J, H}, H},
		{"curl star star", []string{"*/*"}, []string{J, H}, J},
		{"type star", []string{"text/*"}, []string{J, H}, H},
		{"specific overrides broad, down", []string{"text/*;q=1, text/html;q=0.5, application/json;q=0.8"}, []string{H, J}, J},
		{"specific overrides broad, up", []string{"*/*;q=0.1, text/html"}, []string{J, H}, H},
		{"q0 excludes through a wildcard", []string{"text/*, text/html;q=0"}, []string{H}, ""},
		{"q0 excludes, another wins", []string{"*/*, application/json;q=0"}, []string{J, H}, H},
		{"wildcard q0 refuses everything", []string{"*/*;q=0"}, []string{J, H}, ""},
		{"tie goes to the earlier offer", []string{"text/html, application/json"}, []string{J, H}, J},
		{"client q beats server order", []string{"application/json;q=0.5, text/html"}, []string{J, H}, H},
		{"case insensitive", []string{"TEXT/HTML"}, []string{J, "Text/Html"}, "Text/Html"},
		{"offer returned with its parameters", []string{"text/plain"}, []string{J, P}, P},
		{"several Accept lines", []string{"text/html;q=0.2", "application/json"}, []string{H, J}, J},
		{"whitespace and tabs", []string{"\ttext/html \t; \tq = 0.3 ,  application/json ; q=0.9"}, []string{H, J}, J},
		{"quoted parameter with , and ;", []string{`text/html;a="x,y;z";q=0.2, application/json;q=0.1`}, []string{J, H}, H},
		{"none match", []string{"image/png"}, []string{J, H}, ""},
		{"q=1.5 invalid", []string{"text/html;q=1.5, application/json;q=0.1"}, []string{H, J}, J},
		{"q=0.1234 invalid", []string{"text/html;q=0.1234, application/json;q=0.1"}, []string{H, J}, J},
		{"q=abc invalid", []string{"text/html;q=abc, application/json;q=0.1"}, []string{H, J}, J},
		{"q= invalid", []string{"text/html;q=, application/json;q=0.1"}, []string{H, J}, J},
		{"q=1.001 invalid", []string{"text/html;q=1.001, application/json;q=0.1"}, []string{H, J}, J},
		{"q=0. is zero", []string{"text/html;q=0., application/json;q=0.1"}, []string{H, J}, J},
		{"q=1.000 is one", []string{"application/json;q=0.9, text/html;q=1.000"}, []string{J, H}, H},
		{"range without subtype", []string{"text, application/json"}, []string{H, J}, J},
		{"range without type", []string{"/html, application/json"}, []string{H, J}, J},
		{"range */html", []string{"*/html, application/json;q=0.5"}, []string{H, J}, J},
		{"empty elements", []string{",,text/html,,"}, []string{J, H}, H},
		{"only commas", []string{",,,"}, []string{J, H}, ""},
		{"no offers", []string{"*/*"}, nil, ""},
		{"chrome", []string{"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"}, []string{J, H}, H},
		{"long header, last element matters", []string{long.String()}, []string{H, J}, J},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acceptCtx(tc.accept...).Accepts(tc.offers...); got != tc.want {
				t.Errorf("Accept %q, offers %q: got %q, want %q", tc.accept, tc.offers, got, tc.want)
			}
		})
	}
}

func TestParseQ(t *testing.T) {
	good := map[string]int{
		"0": 0, "0.": 0, "0.5": 500, "0.25": 250, "0.123": 123,
		"1": 1000, "1.": 1000, "1.0": 1000, "1.000": 1000,
	}
	for in, want := range good {
		if q, ok := parseQ([]byte(in)); !ok || q != want {
			t.Errorf("parseQ(%q) = %d, %v; want %d, true", in, q, ok, want)
		}
	}
	for _, in := range []string{"", "2", "1.5", "0.1234", "abc", ".5", "01", "1.0000", "0.a", "-0"} {
		if _, ok := parseQ([]byte(in)); ok {
			t.Errorf("parseQ(%q) accepted an invalid qvalue", in)
		}
	}
}

func TestAcceptsPanicsOnABadOffer(t *testing.T) {
	cases := map[string]string{
		"":       "is not a media type",
		"json":   "is not a media type",
		"/json":  "is not a media type",
		"text/":  "is not a media type",
		" ; q=1": "is not a media type",
		"*/*":    "is a wildcard",
		"text/*": "is a wildcard",
		"*/html": "is a wildcard",
	}
	for offer, want := range cases {
		t.Run(offer, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: ") || !strings.Contains(msg, want) {
					t.Errorf("offer %q: panic %q, want a rice: panic containing %q", offer, msg, want)
				}
			}()
			acceptCtx("*/*").Accepts("text/html", offer)
		})
	}
}

func TestAcceptsValidatesOffersWithoutAnAcceptHeader(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a wildcard offer did not panic when the request had no Accept header")
		}
	}()
	acceptCtx().Accepts("text/html", "*/*")
}

func TestErrNotAcceptableAnswers406(t *testing.T) {
	app := New()
	app.GET("/u", func(c *Ctx) error {
		if c.Accepts(MIMEApplicationJSON) == "" {
			return ErrNotAcceptable
		}
		return c.String(200, "json")
	})
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/u")
	fctx.Request.Header.Set(fasthttp.HeaderAccept, "text/html")
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != 406 {
		t.Errorf("status %d, want 406", got)
	}
	if got := string(fctx.Response.Body()); got != "Not Acceptable" {
		t.Errorf("body %q, want %q", got, "Not Acceptable")
	}
}

func FuzzAccepts(f *testing.F) {
	for _, seed := range []string{
		"text/html;q=0.5, */*;q=0.1",
		`a/b;x="\"";q=1`,
		"text/*, text/html;q=0",
		",,;;==",
	} {
		f.Add(seed)
	}
	offers := []string{MIMEApplicationJSON, "text/html"}
	f.Fuzz(func(t *testing.T, accept string) {
		got := acceptCtx(accept).Accepts(offers...)
		if got != "" && !slices.Contains(offers, got) {
			t.Fatalf("Accept %q: got %q, which is not an offer", accept, got)
		}
	})
}

// varyLines returns every Vary line on c's response.
func varyLines(c *Ctx) []string {
	var out []string
	for _, v := range c.fctx.Response.Header.PeekAll(fasthttp.HeaderVary) {
		out = append(out, string(v))
	}
	return out
}

func TestAcceptsAddsVaryAccept(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		calls    int
		want     []string
	}{
		{"one call", nil, 1, []string{"Accept"}},
		{"two calls add it once", nil, 2, []string{"Accept"}},
		{"keeps Vary: Origin", []string{"Origin"}, 1, []string{"Origin", "Accept"}},
		{"already listed, any case", []string{"accept"}, 1, []string{"accept"}},
		{"already listed in a list", []string{"Origin, Accept"}, 1, []string{"Origin, Accept"}},
		{"star covers it", []string{"*"}, 1, []string{"*"}},
		{"a longer token is not Accept", []string{"Accept-Language"}, 1, []string{"Accept-Language", "Accept"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := acceptCtx("text/html")
			for _, v := range tc.existing {
				c.fctx.Response.Header.Add(fasthttp.HeaderVary, v)
			}
			for range tc.calls {
				c.Accepts("text/html")
			}
			if got := varyLines(c); !slices.Equal(got, tc.want) {
				t.Errorf("Vary lines %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAcceptsAddsVaryWithoutAnAcceptHeader(t *testing.T) {
	c := acceptCtx()
	c.Accepts("text/html")
	if got := varyLines(c); !slices.Equal(got, []string{"Accept"}) {
		t.Errorf("Vary lines %q, want [Accept]: the response varies by Accept even when the request sent none", got)
	}
}

func TestAcceptsKeepsAVaryTheHandlerSet(t *testing.T) {
	c := acceptCtx("text/html")
	c.SetHeader(fasthttp.HeaderVary, "Origin")
	c.Accepts("text/html")
	if got := varyLines(c); !slices.Equal(got, []string{"Origin", "Accept"}) {
		t.Errorf("Vary lines %q, want [Origin Accept]", got)
	}
}

func TestA406KeepsVaryAccept(t *testing.T) {
	app := New()
	app.GET("/u", func(c *Ctx) error {
		if c.Accepts(MIMEApplicationJSON) == "" {
			return ErrNotAcceptable
		}
		return nil
	})
	app.Build()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/u")
	fctx.Request.Header.Set(fasthttp.HeaderAccept, "text/html")
	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != 406 {
		t.Fatalf("status %d, want 406", got)
	}
	if got := string(fctx.Response.Header.Peek(fasthttp.HeaderVary)); got != "Accept" {
		t.Errorf("Vary %q on the 406, want %q", got, "Accept")
	}
}
