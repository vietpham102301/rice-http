# Content Negotiation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `c.Accepts(offers ...string) string` picks the offer the request's `Accept` header prefers by RFC 9110's rules, adds `Vary: Accept`, and allocates nothing; `ErrNotAcceptable` answers 406 through the funnel.

**Architecture:** One file, `ctx_accept.go`. `Accepts` validates the offers, adds `Vary: Accept` unless a `Vary` line already covers it, reads every `Accept` line with `PeekAll`, and for each offer scans those bytes once to find the most specific matching range and its quality in thousandths. The best quality wins; a tie keeps the earlier offer. Nothing is stored on `Ctx`.

**Tech Stack:** Go 1.25, fasthttp v1.73.0 (`RequestHeader.PeekAll`, `ResponseHeader.PeekAll`/`Add`), native Go fuzzing. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-26-content-negotiation-design.md](../specs/2026-09-26-content-negotiation-design.md)

## Global Constraints

- **Branch:** `content-negotiation`, already created, already holding the spec commit. Do not commit to `main`.
- **Exported surface added:** exactly `(*Ctx).Accepts` and `ErrNotAcceptable`. Nothing else exported changes.
- **`Accepts` calls `c.poison.check()` first**, before validating offers.
- **Panics carry the prefix `rice: `**: an offer that is not `type/subtype`, and an offer whose type or subtype is `*`.
- **Matching (spec D2):** no `Accept`, or only blank `Accept` lines → first offer; every `Accept` line counts; the most specific matching range (`type/subtype` 3 > `type/*` 2 > `*/*` 1; first in the header among equals) sets an offer's quality; `q=0` excludes; highest quality wins; tie → earlier offer; nothing acceptable → `""`.
- **Parsing (spec D3):** ASCII case-insensitive type/subtype; space and tab allowed around `,` `;` `=`; `qvalue` exactly RFC 9110's (`0`, `0.` + ≤3 digits, `1`, `1.` + ≤3 zeros); an invalid range or an invalid `q` skips that element only; quoted parameter values may contain `,` and `;`.
- **`Vary` (spec D5):** always add `Vary: Accept` with `Add`, unless an existing `Vary` line lists `Accept` (any case) or `*`.
- **No state on `Ctx`**, no reflection, no new dependency.
- **Budgets:** 0 for every `Accepts` budget, with and without `-race`.
- **Run `make test`, `make test-debug` and `make lint` before each commit; never commit with a failing suite.** Commit messages: subject, blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **curl's `Accept: */*`** — the most common non-browser header. Must return the first offer. (Task 1)
2. **Everything refused by a wildcard `q=0`** — `Accept: */*;q=0` makes nothing acceptable, so `Accepts` returns `""`, not the first offer. (Task 1)
3. **A long header where only the last element matters** — fifty `image/x-N` ranges then `application/json`. Must still find it. (Task 1)
4. **A handler that set `Vary: Origin` with `SetHeader` before calling `Accepts`** — both values must survive. (Task 2)
5. **`Accepts` called on a 406 path** — `Vary: Accept` must still be on the response the funnel writes. (Task 2)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `ctx_accept.go` (create) | `Accepts`, `negotiate`, `splitOffer`, `offerQuality`, `matchRange`, `parseQ`, `nextItem`, OWS and ASCII-fold helpers, `addVaryAccept` | 1–2 |
| `errors.go` (modify) | `ErrNotAcceptable` beside `ErrNotFound` | 1 |
| `ctx_accept_test.go` (create) | matching table, `parseQ`, panics, funnel, fuzz, `Vary` tests | 1–2 |
| `ricedebug_test.go` (modify) | `CallSlice` for variadic methods | 1 |
| `alloc_test.go` (modify) | four budgets | 3 |
| `bench/rice_bench_test.go` (modify) | `BenchmarkCtxAccepts` | 3 |
| `docs/adr/0018-accepts-negotiates-by-q-and-adds-vary.md` (create), `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md` | documentation | 4 |

---

### Task 1: Accepts matches by RFC 9110

**Files:**
- Create: `ctx_accept.go`, `ctx_accept_test.go`
- Modify: `errors.go` (after `ErrNotFound`), `ricedebug_test.go` (`TestEveryCtxMethodPanicsAfterRelease`)

**Interfaces:**
- Consumes: `(*Ctx).reset(app *App, fctx *fasthttp.RequestCtx)`, `c.poison.check()`, `(*App).handle`, `HTTPError`.
- Produces: `func (c *Ctx) Accepts(offers ...string) string`; `var ErrNotAcceptable *HTTPError`; unexported `negotiate(accepts [][]byte, offers []string) string`, `trimOWS([]byte) []byte`, `nextItem(s []byte, sep byte) (item, rest []byte)`, `equalFoldASCII(b []byte, s string) bool` (Task 2 uses the last three). Test helper `acceptCtx(accept ...string) *Ctx`.

- [ ] **Step 1: Confirm the branch**

Run: `git branch --show-current`
Expected: `content-negotiation`

- [ ] **Step 2: Write the failing tests**

Create `ctx_accept_test.go`:

```go
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
		"":      "is not a media type",
		"json":  "is not a media type",
		"/json": "is not a media type",
		"text/": "is not a media type",
		" ; q=1": "is not a media type",
		"*/*":   "is a wildcard",
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
```

In `ricedebug_test.go`, in `TestEveryCtxMethodPanicsAfterRelease`, replace the line `v.Method(i).Call(args)` with:

```go
			// A variadic method's last parameter is a slice; Call would try
			// to pass that zero slice as one element and panic inside reflect
			// before the method runs, hiding the use-after-release panic.
			if m.Type.IsVariadic() {
				v.Method(i).CallSlice(args)
				return
			}
			v.Method(i).Call(args)
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test -run 'Accepts|ParseQ|ErrNotAcceptable' -count=1 .`
Expected: FAIL to compile, `c.Accepts undefined (type *Ctx has no field or method Accepts)`.

- [ ] **Step 4: Write the implementation**

In `errors.go`, directly after `ErrNotFound`:

```go
// ErrNotAcceptable is the error a handler returns when Accepts found none of its
// offers acceptable. It is shared like ErrNotFound, so returning it costs
// nothing, and it must never be mutated for the same reason.
var ErrNotAcceptable = &HTTPError{Code: fasthttp.StatusNotAcceptable, Message: "Not Acceptable"}
```

Create `ctx_accept.go`:

```go
package rice

import (
	"bytes"

	"github.com/valyala/fasthttp"
)

// Accepts returns the offer the request's Accept header prefers, or "" when it
// accepts none of them. An offer is a media type, "type/subtype", optionally
// with parameters: "text/plain; charset=utf-8". Only type/subtype is matched,
// and the offer is returned exactly as passed, so it can go straight to
// SetContentType.
//
// Matching follows RFC 9110 §12.5.1. A request with no Accept header accepts
// anything and gets the first offer. Otherwise each offer takes the quality of
// the most specific range that matches it — text/html over text/* over */* —
// q=0 means not acceptable, the highest quality wins, and a tie goes to the
// earlier offer. Every Accept line counts. A malformed element is skipped.
//
// An offer that is not type/subtype, or whose type or subtype is *, panics: the
// server must name what it produces.
//
// It allocates nothing. It does not write a status; a handler with nothing to
// offer returns ErrNotAcceptable.
func (c *Ctx) Accepts(offers ...string) string {
	c.poison.check()
	return negotiate(c.fctx.Request.Header.PeekAll(fasthttp.HeaderAccept), offers)
}

// negotiate is Accepts' matching, over the Accept header's lines. Every offer is
// validated, even when there is no Accept header, so a bad offer panics on the
// first request rather than on the first request that sends one.
func negotiate(accepts [][]byte, offers []string) string {
	present := false
	for _, line := range accepts {
		if len(trimOWS(line)) > 0 {
			present = true
			break
		}
	}

	best, bestQ := "", 0
	for i, offer := range offers {
		typ, sub := splitOffer(offer)
		if !present {
			if i == 0 {
				best = offer
			}
			continue
		}
		// Strictly greater: an offer with the same quality as an earlier one
		// never displaces it, which is the server's tie-break (ADR-0018).
		if q := offerQuality(accepts, typ, sub); q > bestQ {
			best, bestQ = offer, q
		}
	}
	return best
}

// splitOffer returns an offer's type and subtype, panicking on an offer that is
// not a concrete media type.
func splitOffer(offer string) (typ, sub string) {
	mt := offer
	if i := indexByteString(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	mt = trimOWSString(mt)
	i := indexByteString(mt, '/')
	if i <= 0 || i == len(mt)-1 {
		panic("rice: Accepts offer " + `"` + offer + `"` + " is not a media type of the form type/subtype")
	}
	typ, sub = mt[:i], mt[i+1:]
	if typ == "*" || sub == "*" {
		panic("rice: Accepts offer " + `"` + offer + `"` + " is a wildcard; offer the media types the handler produces")
	}
	return typ, sub
}

// offerQuality returns the quality, in thousandths, that the Accept lines give
// typ/sub: the q of the most specific range that matches it, the first such
// range among equally specific ones, or 0 when none matches.
func offerQuality(accepts [][]byte, typ, sub string) int {
	q, spec := 0, 0
	for _, line := range accepts {
		for rest := line; len(rest) > 0; {
			var elem []byte
			elem, rest = nextItem(rest, ',')
			if eq, es, ok := matchRange(elem, typ, sub); ok && es > spec {
				q, spec = eq, es
			}
		}
	}
	return q
}

// matchRange reports whether one Accept element matches typ/sub, how specific
// the match is (3 type/subtype, 2 type/*, 1 */*), and its quality. An element
// whose range is malformed or whose q is not a qvalue matches nothing.
func matchRange(elem []byte, typ, sub string) (q, spec int, ok bool) {
	r, params := nextItem(elem, ';')
	r = trimOWS(r)
	i := bytes.IndexByte(r, '/')
	if i <= 0 || i == len(r)-1 {
		return 0, 0, false
	}
	rt, rs := r[:i], r[i+1:]
	switch {
	case isStar(rt) && isStar(rs):
		spec = 1
	case isStar(rt):
		return 0, 0, false // */html is not a media range
	case !equalFoldASCII(rt, typ):
		return 0, 0, false
	case isStar(rs):
		spec = 2
	case equalFoldASCII(rs, sub):
		spec = 3
	default:
		return 0, 0, false
	}

	q = 1000
	for len(params) > 0 {
		var p []byte
		p, params = nextItem(params, ';')
		eq := bytes.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		if name := trimOWS(p[:eq]); len(name) == 1 && (name[0] == 'q' || name[0] == 'Q') {
			v, valid := parseQ(trimOWS(p[eq+1:]))
			if !valid {
				return 0, 0, false
			}
			q = v
		}
	}
	return q, spec, true
}

// parseQ parses an RFC 9110 qvalue into thousandths: "0" or "0." followed by up
// to three digits, or "1" or "1." followed by up to three zeros.
func parseQ(b []byte) (int, bool) {
	if len(b) == 0 || len(b) > 5 {
		return 0, false
	}
	if len(b) > 1 && b[1] != '.' {
		return 0, false
	}
	switch b[0] {
	case '0':
		q, mul := 0, 100
		for _, c := range b[min(2, len(b)):] {
			if c < '0' || c > '9' {
				return 0, false
			}
			q += int(c-'0') * mul
			mul /= 10
		}
		return q, true
	case '1':
		for _, c := range b[min(2, len(b)):] {
			if c != '0' {
				return 0, false
			}
		}
		return 1000, true
	}
	return 0, false
}

// nextItem splits s at the first sep outside a quoted string, honouring
// backslash escapes inside quotes, and returns the part before it and the rest.
func nextItem(s []byte, sep byte) (item, rest []byte) {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case !quoted && c == sep:
			return s[:i], s[i+1:]
		}
	}
	return s, nil
}

func isStar(b []byte) bool { return len(b) == 1 && b[0] == '*' }

func isOWS(c byte) bool { return c == ' ' || c == '\t' }

// trimOWS trims HTTP optional whitespace, space and tab, from both ends.
func trimOWS(b []byte) []byte {
	for len(b) > 0 && isOWS(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && isOWS(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return b
}

func trimOWSString(s string) string {
	for len(s) > 0 && isOWS(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && isOWS(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func indexByteString(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// equalFoldASCII compares b and s, folding ASCII letters only. Media types are
// ASCII tokens, so Unicode folding would be both slower and wrong.
func equalFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(b); i++ {
		x, y := b[i], s[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -run 'Accepts|ParseQ|ErrNotAcceptable' -count=1 .`
Expected: PASS.

Run: `go test -run '^$' -fuzz FuzzAccepts -fuzztime 60s .`
Expected: PASS with no failure. If the fuzzer writes a failing input under `testdata/fuzz/FuzzAccepts/`, fix the code, keep that file as a regression seed, and commit it.

Run: `go build -gcflags=-m . 2>&1 | grep ctx_accept.go | grep -i 'offers'`
Expected: no line saying `offers` escapes to the heap or `moved to heap` (a `leaking param: offers to result` line is fine: it means the returned string points at a caller's string, not that the slice escapes).

- [ ] **Step 6: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS. Under `make test-debug`, `TestEveryCtxMethodPanicsAfterRelease/Accepts` passes with the use-after-release panic.

- [ ] **Step 7: Commit**

```bash
git add ctx_accept.go ctx_accept_test.go errors.go ricedebug_test.go
git commit -m "rice: Accepts picks an offer by the request's Accept header

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Accepts adds Vary: Accept, once

**Files:**
- Modify: `ctx_accept.go` (`Accepts`, new `addVaryAccept`)
- Test: `ctx_accept_test.go`

**Interfaces:**
- Consumes: `Accepts`, `negotiate`, `nextItem`, `trimOWS`, `equalFoldASCII`, `acceptCtx` (Task 1).
- Produces: `func (c *Ctx) addVaryAccept()`.

- [ ] **Step 1: Write the failing tests**

Append to `ctx_accept_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -run 'Vary|406' -count=1 .`
Expected: FAIL — every case reports no `Accept` in the Vary lines (for example `Vary lines [], want ["Accept"]`).

- [ ] **Step 3: Write the implementation**

In `ctx_accept.go`, change `Accepts`' body to validate first, then add `Vary`, then match. Replace:

```go
	c.poison.check()
	return negotiate(c.fctx.Request.Header.PeekAll(fasthttp.HeaderAccept), offers)
```

with:

```go
	c.poison.check()
	best := negotiate(c.fctx.Request.Header.PeekAll(fasthttp.HeaderAccept), offers)
	c.addVaryAccept()
	return best
```

(`negotiate` panics on a bad offer before `Vary` is touched.)

Add to `Accepts`' doc comment, as its second paragraph:

```go
// It adds Vary: Accept to the response, once, whatever it returns and whether
// or not the request sent Accept: the response depends on the header either
// way, and a cache that is not told so serves one client's format to another.
// This is a write from something that reads like an accessor, and it is
// deliberate; see ADR-0018.
```

Append:

```go
// addVaryAccept adds Vary: Accept unless a Vary line already lists Accept, in
// any case, or *. It adds rather than sets, so a Vary: Origin written by CORS or
// the handler survives.
func (c *Ctx) addVaryAccept() {
	h := &c.fctx.Response.Header
	for _, line := range h.PeekAll(fasthttp.HeaderVary) {
		for rest := line; len(rest) > 0; {
			var tok []byte
			tok, rest = nextItem(rest, ',')
			tok = trimOWS(tok)
			if isStar(tok) || equalFoldASCII(tok, fasthttp.HeaderAccept) {
				return
			}
		}
	}
	h.Add(fasthttp.HeaderVary, fasthttp.HeaderAccept)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -run 'Accepts|ParseQ|ErrNotAcceptable|Vary|406' -count=1 .`
Expected: PASS.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ctx_accept.go ctx_accept_test.go
git commit -m "rice: Accepts adds Vary: Accept once, beside any other Vary

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Budgets and a benchmark

**Files:**
- Modify: `alloc_test.go`, `bench/rice_bench_test.go`

**Interfaces:**
- Consumes: `Accepts`, `ErrNotAcceptable` (Tasks 1–2), `budget` (budget_test.go), `newRequestCtx` (bench/helpers_test.go).
- Produces: `TestAllocBudgetAccepts`, `TestAllocBudgetAcceptsNoHeader`, `TestAllocBudgetAcceptsTwice`, `TestAllocBudgetNotAcceptable`, `BenchmarkCtxAccepts`.

- [ ] **Step 1: Write the budgets**

Append to `alloc_test.go`:

```go
// chromeAccept is Chrome's Accept header for a navigation, the header a browser
// hitting a negotiating endpoint actually sends.
const chromeAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

// acceptSink keeps Accepts' result reachable so the call cannot be elided.
var acceptSink string

// acceptBudgetCtx returns a Ctx whose request carries accept, or no Accept
// header when accept is empty.
func acceptBudgetCtx(accept string) *Ctx {
	fctx := &fasthttp.RequestCtx{}
	if accept != "" {
		fctx.Request.Header.Set(fasthttp.HeaderAccept, accept)
	}
	c := &Ctx{}
	c.reset(nil, fctx)
	return c
}

// TestAllocBudgetAccepts pins Accepts on Chrome's header with two offers,
// including adding Vary: the Vary line is deleted each call so the add path is
// what is measured, as it is on every live request.
func TestAllocBudgetAccepts(t *testing.T) {
	c := acceptBudgetCtx(chromeAccept)
	budget(t, "Ctx.Accepts", 0, func() {
		c.fctx.Response.Header.Del(fasthttp.HeaderVary)
		acceptSink = c.Accepts(MIMEApplicationJSON, "text/html")
	})
}

// TestAllocBudgetAcceptsNoHeader pins the path a client without Accept takes.
func TestAllocBudgetAcceptsNoHeader(t *testing.T) {
	c := acceptBudgetCtx("")
	budget(t, "Ctx.Accepts without Accept", 0, func() {
		c.fctx.Response.Header.Del(fasthttp.HeaderVary)
		acceptSink = c.Accepts(MIMEApplicationJSON, "text/html")
	})
}

// TestAllocBudgetAcceptsTwice pins two calls in one request: the second finds
// Vary: Accept already there and adds nothing.
func TestAllocBudgetAcceptsTwice(t *testing.T) {
	c := acceptBudgetCtx(chromeAccept)
	budget(t, "Ctx.Accepts twice", 0, func() {
		c.fctx.Response.Header.Del(fasthttp.HeaderVary)
		acceptSink = c.Accepts(MIMEApplicationJSON, "text/html")
		acceptSink = c.Accepts("text/html")
	})
}

// TestAllocBudgetNotAcceptable pins a handler that finds nothing acceptable and
// returns ErrNotAcceptable through the funnel.
func TestAllocBudgetNotAcceptable(t *testing.T) {
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

	budget(t, "406 through the funnel", 0, func() {
		fctx.Response.Header.Del(fasthttp.HeaderVary)
		app.handle(fctx)
	})
}
```

- [ ] **Step 2: Run the budgets**

Run: `go test -run 'AllocBudgetAccepts|AllocBudgetNotAcceptable' -count=3 . && go test -run 'AllocBudgetAccepts|AllocBudgetNotAcceptable' -count=3 -race .`
Expected: PASS on every run. If a budget fails, do not raise its `want`: the spec requires 0. Find the allocation with `go test -run <name> -memprofile mem.out . && go tool pprof -list 'Accepts|negotiate|addVary' mem.out`, remove it, and record what it was in the report. Delete `mem.out`.

- [ ] **Step 3: Write the benchmark**

Append to `bench/rice_bench_test.go`:

```go
// BenchmarkCtxAccepts measures a handler negotiating between JSON and HTML for
// Chrome's Accept header, including Vary: Accept. The response is reset each
// iteration, as fasthttp's server does between requests, so Vary is added
// every time.
func BenchmarkCtxAccepts(b *testing.B) {
	app := rice.New()
	app.GET("/u", func(c *rice.Ctx) error {
		if c.Accepts(rice.MIMEApplicationJSON, "text/html") == "" {
			return rice.ErrNotAcceptable
		}
		return nil
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/u")
	fctx.Request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fctx.Response.Reset()
		h(fctx)
	}
}
```

- [ ] **Step 4: Run the benchmark**

Run: `go test ./bench -run '^$' -bench BenchmarkCtxAccepts -benchmem -count=10 | tee /tmp/accepts-bench.txt`
Expected: ten lines, each `0 B/op` and `0 allocs/op`. Record the median ns/op, the Go version and the machine for Task 4; do not commit `/tmp/accepts-bench.txt`.

- [ ] **Step 5: Run the full suites**

Run: `make test && make test-debug && make lint`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add alloc_test.go bench/rice_bench_test.go
git commit -m "rice: pin Accepts at zero allocations and benchmark it

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Documentation

**Files:**
- Create: `docs/adr/0018-accepts-negotiates-by-q-and-adds-vary.md`
- Modify: `docs/adr/README.md`, `docs/03-core-concepts.md` (§2 *Read side* table, and §7 *Errors* where `ErrNotFound` is described), `docs/05-performance-model.md` (*Request path — `Ctx` methods* table), `docs/04-roadmap.md` (*Explicitly deferred*, *Done after M8*), `docs/progress.md` (new entry at the top)

**Interfaces:**
- Consumes: every behaviour from Tasks 1–3 and the benchmark median from Task 3.
- Produces: nothing code depends on.

- [ ] **Step 1: Write ADR-0018**

Create `docs/adr/0018-accepts-negotiates-by-q-and-adds-vary.md`, in the shape of the existing ADRs (read ADR-0017 first):

```markdown
# ADR-0018 — Accepts picks by the client's q, breaks ties by the server's order, and adds Vary

Status: Accepted
Date: 2026-09-26

## Context

Content negotiation was on the roadmap's *Explicitly deferred* list. The owner scoped it to format
selection: one handler answering JSON to an API client and HTML or plain text to a browser, chosen by
the `Accept` header. Two choices in RFC 9110 §12.5.1 are left to the server, and one choice about
caching is left to the framework.

## Decision

`c.Accepts(offers ...string) string` returns the offer the request prefers, or `""`.

- **Quality is the client's.** Each offer takes the q of the most specific matching range —
  `text/html` over `text/*` over `*/*` — and `q=0` excludes it.
- **Ties are the server's.** Among offers of equal quality the earlier one wins. A browser sends
  `*/*;q=0.8` after its preferred types; with `*/*` alone, as curl sends, every offer ties and the
  handler's first choice is served.
- **`Accepts` adds `Vary: Accept`, once.** Every call adds it, whatever it returns and whether or
  not the request sent `Accept`, unless a `Vary` line already lists `Accept` or `*`. It adds rather
  than sets, so CORS's `Vary: Origin` survives.
- **It writes no status.** A handler with nothing acceptable returns `ErrNotAcceptable`, a shared
  `*HTTPError` like `ErrNotFound`.
- **It allocates nothing and stores nothing on `Ctx`.** Each offer scans the `Accept` bytes once.

## Alternatives

**Leave `Vary` to the handler.** Keeps `Accepts` a pure read, like every other accessor. Rejected by
the owner: forgetting it is silent, and its cost is a shared cache serving one client's format to
another. The write is named in the doc comment's second paragraph.

**Two methods, one pure and one that adds `Vary`.** Rejected as twice the API for a choice with one
right answer in practice.

**Break ties by the client's order.** Some frameworks do. Rejected because RFC 9110 gives ordering
meaning only through q, and because with `*/*` — the commonest header after a browser's — the
client expresses no order at all; the server's order is the only preference there is.

**Parse `Accept` into a buffer on `Ctx`.** Faster when a handler calls `Accepts` several times.
Rejected: it adds per-request state that `reset` must clear and a size limit to choose, for a case
that is rare.

**A third-party parser.** Rejected: a dependency in core, and the candidates allocate.

## Consequences

**A read that writes.** `Accepts` is the one `Ctx` read-side method with a side effect on the
response. Calling it on a path that then serves something unrelated still adds `Vary: Accept`, which
is harmless to correctness and costs a cache some hit rate.

**Parameters are not matched.** `text/html;level=1` matches `text/html`; `application/vnd.app+json`
does not match `application/json`. Versioning by media-type parameters is out of scope.

**The ricedebug walker changed.** `TestEveryCtxMethodPanicsAfterRelease` calls variadic methods with
`CallSlice`; `reflect.Call` would panic inside reflect first and hide the use-after-release panic.
```

- [ ] **Step 2: List it**

In `docs/adr/README.md`, add a row for ADR-0018 after ADR-0017, in the existing row format.

- [ ] **Step 3: Core concepts**

In `docs/03-core-concepts.md`, add a row to the §2 *Read side* table, after `Header`:

```markdown
| `Accepts(offers ...string) string` | the offer `Accept` prefers, or `""` | 0 | adds `Vary: Accept`; see ADR-0018 |
```

and after the table, a short paragraph:

```markdown
`Accepts` is the one read that writes: it adds `Vary: Accept` to the response, once, so a cache keys
the response by the header it depended on. It follows RFC 9110 — the most specific matching range sets
an offer's quality, `q=0` refuses it, the highest quality wins, and a tie goes to the earlier offer. A
handler with nothing acceptable returns `ErrNotAcceptable`, a 406.
```

In §7 *Errors*, where `ErrNotFound` is described, add one sentence: `ErrNotAcceptable` is the shared 406 in the same form, returned by a handler whose `Accepts` found nothing acceptable.

- [ ] **Step 4: Performance model**

In `docs/05-performance-model.md`, add a row to the *Request path — `Ctx` methods* table in its existing column format: `c.Accepts` | 0 | MEASURED post-M8 | `TestAllocBudgetAccepts`, `TestAllocBudgetAcceptsNoHeader`, `TestAllocBudgetAcceptsTwice`. Add a row for the 406 path where the table of dispatch budgets lists the 404 (`TestAllocBudget404`): 0, `TestAllocBudgetNotAcceptable`. Beside them, name `BenchmarkCtxAccepts` and its median ns/op from Task 3, with the machine and Go version.

- [ ] **Step 5: Roadmap**

In `docs/04-roadmap.md`, remove `- Content negotiation` from *Explicitly deferred*, and append to *Done after M8*:

```markdown
- `c.Accepts(offers ...string)` and `ErrNotAcceptable`: a handler answers the same resource in the
  format the request's `Accept` header prefers, by RFC 9110's rules, and `Vary: Accept` is added for
  it. Probing found that fasthttp's `Peek` reads only the first of several `Accept` lines and that the
  ricedebug walker could not call a variadic method; the matching reads every line, and the walker
  uses `CallSlice`. Zero allocations, and nothing stored on `Ctx`
  ([ADR-0018](adr/0018-accepts-negotiates-by-q-and-adds-vary.md)).
```

- [ ] **Step 6: Progress entry**

Add at the top of `docs/progress.md`, below the `---` that follows the entry template, `## 2026-09-26 — post-M8 — Accepts negotiates the response format` with the four headings, in the voice of the entries below it:

- **Did:** the API, the matching rules, `Vary`, `ErrNotAcceptable`, the walker change, the tests by group (matching table, `parseQ`, panics, `Vary`, funnel, fuzz), the four budgets, the benchmark, ADR-0018 and the documents changed.
- **Learned:** three numbered items: `Peek` reads only the first `Accept` line; `reflect.Call` cannot call a variadic method with a zero slice; the matching needs no float and no buffer — qualities are integers in thousandths and each offer scans the header once. Add any surprise met while executing this plan.
- **Measured:** the four budgets at 0 with and without `-race`; `BenchmarkCtxAccepts`' median ns/op, 0 B/op, 0 allocs/op, machine and Go version; the fuzz run's duration and exec count from Task 1; the root package's coverage from `make cover`.
- **Next:** streaming and server-sent events, the last deferred item, which needs its own brainstorm.

- [ ] **Step 7: Verify and commit**

Run: `make test && make test-debug && make lint && make cover`
Expected: PASS; root package coverage not below 99.5%.

```bash
git add docs
git commit -m "docs: ADR-0018 and the docs for content negotiation

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```
