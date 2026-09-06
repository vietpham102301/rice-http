# M0 + M1 Implementation Plan — Scaffold and Minimal Server

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the rice repository with a working measurement rig (M0), then make it serve one hardcoded handler over a real socket with a deliberately unpooled context (M1), producing the allocation baseline every later milestone is measured against.

**Architecture:** M0 creates the module, the Makefile, CI, and a benchmark harness whose first benchmark measures *raw fasthttp with no framework at all*. That is the true zero point. M1 adds the thinnest possible framework on top: `Handler`, `Ctx`, `App`, and a server, allocating one `Ctx` per request on purpose. The delta between the two benchmark numbers is the framework's cost, and M6's pooling work is judged against it.

**Tech Stack:** Go 1.25, `github.com/valyala/fasthttp` (the only runtime dependency), GNU make, GitHub Actions.

**Spec:** `docs/README.md` (index). Milestone definitions and exit criteria in `docs/04-roadmap.md`. Allocation budgets in `docs/05-performance-model.md`. Decisions in `docs/adr/`.

## Global Constraints

Every task's requirements implicitly include this section.

- Module path is exactly `github.com/vietpham102301/rice-http`. Root package name is `rice`. Because the path ends in `rice-http` and the package is `rice`, every example import is written with an explicit alias: `rice "github.com/vietpham102301/rice-http"`.
- Go directive in `go.mod` is `1.25`.
- `github.com/valyala/fasthttp` is the only permitted runtime dependency. Test-only use of the standard library, including `net/http` in tests, is fine.
- Package `rice` must not import `reflect` or `encoding/json` (ADR-0006). Not applicable yet in M1, but the constraint is active from the first commit.
- Every value handed out by `Ctx` is borrowed and dies when the handler returns (ADR-0005). Byte-returning accessors are free and borrowed. String-returning accessors copy. This naming rule is established in M1 and never broken later.
- Allocation budgets that M1 must meet, from `docs/05-performance-model.md`: `c.String` = 0, `c.Bytes` = 0. These are asserted by tests using `testing.AllocsPerRun`, not merely observed in benchmarks.
- End-to-end per-request allocation in M1 is expected to be greater than zero. That is intentional. Do not optimise it. It is the baseline.
- Code that belongs to a later milestone carries a comment naming that milestone, so temporary scaffolding is never mistaken for a design decision.
- Commit after every task. Commit messages use Conventional Commits (`feat:`, `test:`, `chore:`, `docs:`, `bench:`).

---

## File Structure

Files created by this plan, and what each is responsible for.

| File | Responsibility | Task |
| --- | --- | --- |
| `go.mod`, `go.sum` | Module identity, pinned fasthttp | 1 |
| `doc.go` | Package `rice` documentation, borrow contract summary | 1 |
| `Makefile` | `test`, `bench`, `bench-record`, `cover`, `lint`, `tidy` | 1 |
| `.gitignore` | Adds `coverage.out` to the existing entries | 1 |
| `bench/doc.go` | Package `bench` docs, benchmark methodology note | 2 |
| `bench/helpers_test.go` | `newRequestCtx` shared by all benchmark files | 2 |
| `bench/fasthttp_baseline_test.go` | Raw fasthttp handler benchmark, the zero point | 2 |
| `scripts/bench.sh` | Runs benchmarks, stamps environment, writes a results file | 2 |
| `bench/results/` | Committed benchmark output, one file per milestone | 2, 9 |
| `.github/workflows/ci.yml` | Lint, test with race detector, benchmark smoke run | 3 |
| `docs/milestones/M0-scaffold.md` | M0 retrospective | 4 |
| `handler.go` | `type Handler func(*Ctx) error` | 5 |
| `ctx.go` | `Ctx` struct, `reset`, `RequestCtx`, `Method`, `Path` | 5 |
| `ctx_test.go` | Unit tests for the read side | 5 |
| `ctx_response.go` | `Status`, `SetHeader`, `SetContentType`, `String`, `Bytes` | 6 |
| `ctx_response_test.go` | Response behaviour tests | 6 |
| `alloc_test.go` | `AllocsPerRun` budget assertions | 6 |
| `app.go` | `App`, `Option`, `New`, `SetHandler`, `handle`, `FasthttpHandler` | 7 |
| `app_test.go` | Dispatch and error-path tests | 7 |
| `server.go` | `Run`, `Serve`, `Addr`, `Shutdown`, `ErrShutdownTimeout` | 8 |
| `server_test.go` | Real-socket integration tests | 8 |
| `bench/rice_bench_test.go` | rice handler-level benchmarks | 9 |
| `docs/milestones/M1-minimal-server.md` | M1 retrospective with measured numbers | 10 |

---

# Milestone 0 — Scaffold

### Task 1: Module, package doc, Makefile

**Files:**
- Create: `go.mod`
- Create: `doc.go`
- Create: `Makefile`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: nothing.
- Produces: the module path `github.com/vietpham102301/rice-http` and package name `rice`, which every later task imports. The make targets `test`, `bench`, `bench-record`, `cover`, `lint`, `tidy`.

- [x] **Step 1: Initialise the module and pin fasthttp**

```bash
cd /Users/vietpham1023/dev/rice-http
go mod init github.com/vietpham102301/rice-http
go get github.com/valyala/fasthttp@latest
```

Record the resolved version. Read it back and paste it into the M0 milestone doc in Task 4:

```bash
grep fasthttp go.mod
```

Do not hand-write a version number. Whatever `@latest` resolves to is the pin, and `go.sum` locks it.

- [x] **Step 2: Confirm the Go directive**

Open `go.mod` and ensure the directive line reads exactly:

```
go 1.25
```

If `go mod init` wrote a more specific version such as `go 1.25.6`, change it to `go 1.25`. A patch-level directive forces every contributor onto that exact patch for no benefit.

- [x] **Step 3: Write the package doc**

Create `doc.go`:

```go
// Package rice is a small HTTP framework built on fasthttp.
//
// # Borrow contract
//
// Every value reachable from a *Ctx is borrowed, not owned. It is valid only
// until the handler returns. That includes the *Ctx itself and every []byte it
// hands out: route parameters, headers, the path, and the request body. The
// memory behind those slices belongs to fasthttp and is reused for the next
// request on the same connection.
//
// Accessors that return []byte are free and borrowed. Accessors that return
// string copy, cost one allocation, and are safe to keep. The naming makes the
// expensive choice the longer one to type:
//
//	id := c.Param("id")        // borrowed: valid until the handler returns
//	id := c.ParamString("id")  // owned: safe to keep, costs one allocation
//
// See docs/adr/0005-context-pooling-and-borrow-contract.md.
package rice
```

- [x] **Step 4: Write the Makefile**

Create `Makefile`. Note that recipe lines must be indented with a real tab character, and that `$` is escaped as `$$` so make passes a literal dollar sign to the shell:

```make
GO ?= go

.PHONY: test bench bench-record cover lint tidy

## test: run all tests with the race detector, no cache
test:
	$(GO) test ./... -race -count=1

## bench: quick benchmark run, one iteration, for CI smoke testing
bench:
	$(GO) test ./bench/... -run '^$$' -bench . -benchmem -count=1

## bench-record: full benchmark run, stamped and written to bench/results/
## usage: make bench-record LABEL=M1-minimal-server
bench-record:
	./scripts/bench.sh $(LABEL)

## cover: test coverage summary
cover:
	$(GO) test ./... -coverprofile=coverage.out -covermode=atomic -count=1
	$(GO) tool cover -func=coverage.out | tail -1

## lint: formatting and vet
lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files"; exit 1)
	$(GO) vet ./...

## tidy: sync go.mod and go.sum
tidy:
	$(GO) mod tidy
```

- [x] **Step 5: Add coverage output to .gitignore**

Append to the existing `.gitignore`:

```
coverage.out
```

- [x] **Step 6: Verify**

```bash
make lint
make test
```

Expected: `make lint` produces no output and exits 0. `make test` prints `?   github.com/vietpham102301/rice-http  [no test files]` and exits 0. `make bench` will fail at this point because `bench/` does not exist yet; that is Task 2.

- [x] **Step 7: Commit**

```bash
git add go.mod go.sum doc.go Makefile .gitignore
git commit -m "chore: initialise go module, package doc, and make targets"
```

---

### Task 2: Benchmark harness and the raw fasthttp baseline

**Files:**
- Create: `bench/doc.go`
- Create: `bench/helpers_test.go`
- Create: `bench/fasthttp_baseline_test.go`
- Create: `scripts/bench.sh`
- Create: `bench/results/README.md`

**Interfaces:**
- Consumes: the module from Task 1.
- Produces: `newRequestCtx(method, uri string) *fasthttp.RequestCtx`, used by every benchmark in Tasks 2 and 9. The `bench/results/<LABEL>.txt` file format that Task 9 and every later milestone appends to.

This task answers M0's question: what does the measurement rig look like, before there is anything to measure? The answer is that the rig's first job is to measure the transport with no framework on top, so that M1 has something honest to be compared against.

- [x] **Step 1: Write the bench package doc**

Create `bench/doc.go`:

```go
// Package bench holds rice's benchmark suite.
//
// # Why benchmarks live outside package rice
//
// They import rice the way a user does, through its exported API only. A
// benchmark with access to unexported internals measures a program no user can
// write.
//
// # Two levels of measurement, and why
//
// Handler-level benchmarks call the fasthttp request handler directly on a
// reused *fasthttp.RequestCtx. They exclude connection handling, parsing and
// socket I/O, which makes them the right place to assert allocation budgets:
// the number that comes out is attributable to framework code and nothing else.
//
// End-to-end benchmarks that drive a real client measure the client as much as
// the server, so their allocation counts are not a contract. Correctness over a
// real socket is proven in server_test.go instead.
//
// BenchmarkFasthttpBaseline is the zero point: fasthttp with no framework at
// all. Every rice benchmark is read as a delta from it.
package bench
```

- [x] **Step 2: Write the shared helper**

Create `bench/helpers_test.go`:

```go
package bench

import "github.com/valyala/fasthttp"

// newRequestCtx builds a reusable request context for handler-level benchmarks.
// It is created once outside the benchmark loop so that request construction
// does not pollute the allocation count of the code under test.
func newRequestCtx(method, uri string) *fasthttp.RequestCtx {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)
	return fctx
}
```

- [x] **Step 3: Write the baseline benchmark**

Create `bench/fasthttp_baseline_test.go`:

```go
package bench

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// BenchmarkFasthttpBaseline measures a bare fasthttp handler writing a
// plaintext response, with no framework in the path. This is the zero point.
// Any allocation rice reports above this number is rice's own.
func BenchmarkFasthttpBaseline(b *testing.B) {
	h := func(fctx *fasthttp.RequestCtx) {
		fctx.SetStatusCode(fasthttp.StatusOK)
		fctx.SetContentType("text/plain; charset=utf-8")
		fctx.SetBodyString("hello")
	}

	fctx := newRequestCtx("GET", "/hello")
	h(fctx) // warm the response buffers, as a live server would be warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
```

- [x] **Step 4: Run it and confirm the rig works**

```bash
go test ./bench/... -run '^$' -bench . -benchmem -count=1
```

Expected: one benchmark line for `BenchmarkFasthttpBaseline`, reporting `0 B/op` and `0 allocs/op`. If it reports allocations, the warm-up call in Step 3 is missing or the response buffer is being reset inside the loop. Investigate before continuing, because every later number depends on this one being trustworthy.

- [x] **Step 5: Write the recording script**

Create `scripts/bench.sh`:

```bash
#!/usr/bin/env bash
# Runs the benchmark suite and writes a stamped result file.
# usage: scripts/bench.sh <label>      e.g. scripts/bench.sh M1-minimal-server
set -euo pipefail

label="${1:?usage: scripts/bench.sh <label>}"
out="bench/results/${label}.txt"
mkdir -p bench/results

{
  echo "# rice benchmark results: ${label}"
  echo "# date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# go:   $(go version)"
  echo "# os:   $(uname -srm)"
  if [ "$(uname -s)" = "Darwin" ]; then
    echo "# cpu:  $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  else
    echo "# cpu:  $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ *//' || echo unknown)"
  fi
  echo
  go test ./bench/... -run '^$' -bench . -benchmem -count=10
} | tee "${out}"

echo "wrote ${out}"
```

Make it executable:

```bash
chmod +x scripts/bench.sh
```

- [x] **Step 6: Write the results README**

Create `bench/results/README.md`:

```markdown
# Benchmark Results

One file per milestone, produced by `make bench-record LABEL=<name>`.

These files are committed on purpose. A performance regression should appear as
a diff in a pull request, not as someone's recollection of what the number used
to be.

Each file is stamped with the date, Go version, OS and CPU it was produced on.
Numbers from different stamps are not comparable. When comparing two milestones,
re-run the older label on the current machine rather than trusting a stored
number from different hardware.

Compare two runs with:

    benchstat bench/results/M0-fasthttp-baseline.txt bench/results/M1-minimal-server.txt

Install benchstat with:

    go install golang.org/x/perf/cmd/benchstat@latest
```

- [x] **Step 7: Record the M0 baseline**

```bash
make bench-record LABEL=M0-fasthttp-baseline
```

Expected: `bench/results/M0-fasthttp-baseline.txt` exists, has the stamp header, and contains ten runs of `BenchmarkFasthttpBaseline`.

- [x] **Step 8: Verify the make targets**

```bash
make lint
make test
make bench
```

Expected: all three exit 0. M0's exit criterion is now met.

- [x] **Step 9: Commit**

```bash
git add bench scripts
git commit -m "bench: add benchmark harness and raw fasthttp baseline"
```

---

### Task 3: Continuous integration

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the make targets from Task 1.
- Produces: nothing other tasks depend on.

- [x] **Step 1: Write the workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
          cache: true

      - name: Verify go.mod is tidy
        run: |
          go mod tidy
          git diff --exit-code go.mod go.sum

      - name: Lint
        run: make lint

      - name: Test
        run: make test

      - name: Benchmark smoke run
        run: make bench
```

The benchmark step runs with `-count=1` and is a smoke test only. Its job is to prove the benchmarks still compile and run, not to produce numbers. CI hardware is too noisy to compare timings across runs, which is why recorded results come from `make bench-record` on a known machine.

- [x] **Step 2: Verify locally**

The workflow cannot be run locally, so run what it runs:

```bash
go mod tidy && git diff --exit-code go.mod go.sum
make lint
make test
make bench
```

Expected: all exit 0.

- [x] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: run lint, race tests, and a benchmark smoke run on push"
```

---

### Task 4: Close out M0

**Files:**
- Create: `docs/milestones/M0-scaffold.md`
- Modify: `docs/04-roadmap.md` (M0 status marker)
- Modify: `docs/progress.md` (prepend an entry)

**Interfaces:**
- Consumes: the resolved fasthttp version from Task 1 Step 1, and the baseline numbers from Task 2 Step 7.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Write the milestone retrospective**

Create `docs/milestones/M0-scaffold.md` from `docs/milestones/TEMPLATE.md`. Fill in every section. The Measurements table takes the `BenchmarkFasthttpBaseline` numbers from `bench/results/M0-fasthttp-baseline.txt`, with the hardware line copied from that file's stamp header. Record the resolved fasthttp version in the Design notes section.

The Retrospective section must be written honestly rather than filled with plausible text. If nothing surprised you, write that nothing did and say what you expected to be harder.

- [ ] **Step 2: Mark M0 done in the roadmap**

In `docs/04-roadmap.md`, change the M0 heading marker from `☐` to `☑`:

```
### ☑ M0 — Scaffold
```

- [ ] **Step 3: Prepend a journal entry**

In `docs/progress.md`, insert a new entry directly below the `---` separator and above the existing `## 2026-09-02 — M0 — Design phase` entry, using the four-field shape defined at the top of that file: Did, Learned, Measured, Next. The Measured field carries the real baseline number, which is the first real number in the project.

Do not edit the existing entry. The journal is append-only.

- [ ] **Step 4: Verify the links**

```bash
grep -n "M0" docs/04-roadmap.md docs/progress.md docs/milestones/M0-scaffold.md
```

Expected: the roadmap shows `☑ M0`, and both the journal and the milestone doc reference the recorded results file.

- [ ] **Step 5: Commit**

```bash
git add docs
git commit -m "docs: close out M0 with retrospective, baseline numbers, and journal entry"
```

---

# Milestone 1 — Minimal Server

M1 has no router. The `App` serves exactly one handler, and it allocates a `Ctx` per request on purpose. Resist every urge to optimise here. The number this milestone produces is the thing M6 has to beat.

### Task 5: Handler and the Ctx read side

**Files:**
- Create: `handler.go`
- Create: `ctx.go`
- Test: `ctx_test.go`

**Interfaces:**
- Consumes: the module from Task 1.
- Produces:
  - `type Handler func(c *Ctx) error`
  - `type Ctx struct{...}` with unexported fields `fctx *fasthttp.RequestCtx` and `app *App`
  - `func (c *Ctx) reset(app *App, fctx *fasthttp.RequestCtx)` (unexported, used by Task 7)
  - `func (c *Ctx) RequestCtx() *fasthttp.RequestCtx`
  - `func (c *Ctx) Method() []byte`
  - `func (c *Ctx) Path() []byte`

The `app` field is unused in M1 and exists because Task 7's `handle` sets it and M5's error funnel reads it. It is set now so that the `reset` signature does not change under later tasks.

- [ ] **Step 1: Write the failing test**

Create `ctx_test.go`:

```go
package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func TestCtxMethodAndPath(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("POST")
	fctx.Request.SetRequestURI("/users/42?q=x")

	c := &Ctx{}
	c.reset(nil, fctx)

	if got := string(c.Method()); got != "POST" {
		t.Errorf("Method() = %q, want %q", got, "POST")
	}
	if got := string(c.Path()); got != "/users/42" {
		t.Errorf("Path() = %q, want %q", got, "/users/42")
	}
}

func TestCtxRequestCtxReturnsUnderlyingContext(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}

	c := &Ctx{}
	c.reset(nil, fctx)

	if c.RequestCtx() != fctx {
		t.Error("RequestCtx() did not return the context passed to reset")
	}
}

func TestCtxResetRebindsToANewRequest(t *testing.T) {
	first := &fasthttp.RequestCtx{}
	first.Request.SetRequestURI("/first")

	second := &fasthttp.RequestCtx{}
	second.Request.SetRequestURI("/second")

	c := &Ctx{}
	c.reset(nil, first)
	c.reset(nil, second)

	if got := string(c.Path()); got != "/second" {
		t.Errorf("after reset, Path() = %q, want %q", got, "/second")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestCtx -v`

Expected: FAIL to compile, with `undefined: Ctx`.

- [ ] **Step 3: Write the handler type**

Create `handler.go`:

```go
package rice

// Handler is the single unit of work in rice.
//
// It receives a borrowed *Ctx and returns an error or nil. Returning a non-nil
// error is the only way to signal failure: there is no abort flag and no
// sentinel status the framework inspects. If you see "return err" in a handler,
// the request is over.
//
// See docs/adr/0002-handler-returns-error.md.
type Handler func(c *Ctx) error
```

- [ ] **Step 4: Write the context**

Create `ctx.go`:

```go
package rice

import "github.com/valyala/fasthttp"

// Ctx is a borrowed handle to one in-flight request.
//
// It is valid only for the duration of the handler that received it, along with
// every []byte it hands out. See the borrow contract in doc.go.
type Ctx struct {
	fctx *fasthttp.RequestCtx
	app  *App
}

// reset rebinds the context to a new request.
//
// M1 constructs a fresh Ctx per request, so reset is called exactly once per
// instance. M6 introduces a sync.Pool, at which point reset becomes the point
// where a recycled Ctx drops every reference to the previous request.
func (c *Ctx) reset(app *App, fctx *fasthttp.RequestCtx) {
	c.app = app
	c.fctx = fctx
}

// RequestCtx exposes the underlying fasthttp context.
//
// It is the escape hatch for anything rice does not wrap. Everything the
// borrow contract says about Ctx applies to what you reach through it.
func (c *Ctx) RequestCtx() *fasthttp.RequestCtx { return c.fctx }

// Method returns the HTTP verb.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Method() []byte { return c.fctx.Method() }

// Path returns the request path, without the query string.
//
// Borrowed: the returned slice is valid only until the handler returns.
func (c *Ctx) Path() []byte { return c.fctx.Path() }
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./... -run TestCtx -v`

Expected: PASS for all three tests.

- [ ] **Step 6: Commit**

```bash
git add handler.go ctx.go ctx_test.go
git commit -m "feat: add Handler type and the Ctx read side"
```

---

### Task 6: The Ctx response side and its allocation budgets

**Files:**
- Create: `ctx_response.go`
- Test: `ctx_response_test.go`
- Test: `alloc_test.go`

**Interfaces:**
- Consumes: `Ctx` and `reset` from Task 5.
- Produces:
  - `const MIMETextPlainUTF8 = "text/plain; charset=utf-8"`
  - `func (c *Ctx) Status(code int) *Ctx`
  - `func (c *Ctx) SetHeader(key, value string)`
  - `func (c *Ctx) SetContentType(value string)`
  - `func (c *Ctx) String(code int, s string) error`
  - `func (c *Ctx) Bytes(code int, b []byte) error`

`String` and `Bytes` return `error` although they cannot currently fail. The signature matches the `Handler` return type so that `return c.String(200, "ok")` is the idiomatic last line of a handler, and it leaves room for a streaming write to report a failure later without an API break.

- [ ] **Step 1: Write the failing behaviour tests**

Create `ctx_response_test.go`:

```go
package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func newTestCtx(method, uri string) (*Ctx, *fasthttp.RequestCtx) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod(method)
	fctx.Request.SetRequestURI(uri)

	c := &Ctx{}
	c.reset(nil, fctx)
	return c, fctx
}

func TestStringWritesStatusBodyAndContentType(t *testing.T) {
	c, fctx := newTestCtx("GET", "/hello")

	if err := c.String(201, "hello"); err != nil {
		t.Fatalf("String returned %v, want nil", err)
	}

	if got := fctx.Response.StatusCode(); got != 201 {
		t.Errorf("status = %d, want 201", got)
	}
	if got := string(fctx.Response.Body()); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
	if got := string(fctx.Response.Header.ContentType()); got != MIMETextPlainUTF8 {
		t.Errorf("content type = %q, want %q", got, MIMETextPlainUTF8)
	}
}

func TestBytesWritesStatusAndBodyWithoutSettingContentType(t *testing.T) {
	c, fctx := newTestCtx("GET", "/raw")
	c.SetContentType("application/octet-stream")

	if err := c.Bytes(200, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("Bytes returned %v, want nil", err)
	}

	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := string(fctx.Response.Body()); got != "\x01\x02\x03" {
		t.Errorf("body = %q, want three raw bytes", got)
	}
	if got := string(fctx.Response.Header.ContentType()); got != "application/octet-stream" {
		t.Errorf("Bytes overwrote the content type: got %q", got)
	}
}

func TestStatusIsChainable(t *testing.T) {
	c, fctx := newTestCtx("GET", "/chain")

	if c.Status(418) != c {
		t.Error("Status did not return the same *Ctx")
	}
	if got := fctx.Response.StatusCode(); got != 418 {
		t.Errorf("status = %d, want 418", got)
	}
}

func TestSetHeaderWritesAResponseHeader(t *testing.T) {
	c, fctx := newTestCtx("GET", "/hdr")

	c.SetHeader("X-Trace", "abc123")

	if got := string(fctx.Response.Header.Peek("X-Trace")); got != "abc123" {
		t.Errorf("X-Trace = %q, want %q", got, "abc123")
	}
}

func TestBodyIsOverwrittenNotAppendedOnSecondWrite(t *testing.T) {
	c, fctx := newTestCtx("GET", "/twice")

	_ = c.String(200, "first")
	_ = c.String(200, "second")

	if got := string(fctx.Response.Body()); got != "second" {
		t.Errorf("body = %q, want %q; writes must replace, not append", got, "second")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestString|TestBytes|TestStatus|TestSetHeader|TestBody' -v`

Expected: FAIL to compile, with `undefined: MIMETextPlainUTF8` and undefined methods.

- [ ] **Step 3: Write the response side**

Create `ctx_response.go`:

```go
package rice

// MIMETextPlainUTF8 is the content type String sets on the response.
const MIMETextPlainUTF8 = "text/plain; charset=utf-8"

// Status sets the response status code and returns the context for chaining.
func (c *Ctx) Status(code int) *Ctx {
	c.fctx.SetStatusCode(code)
	return c
}

// SetHeader sets a response header, replacing any existing value for the key.
func (c *Ctx) SetHeader(key, value string) {
	c.fctx.Response.Header.Set(key, value)
}

// SetContentType sets the Content-Type response header.
func (c *Ctx) SetContentType(value string) {
	c.fctx.SetContentType(value)
}

// String writes a plaintext response body and sets the content type.
//
// It allocates nothing on a warm response buffer, which is the steady state on
// a live server. See docs/05-performance-model.md.
func (c *Ctx) String(code int, s string) error {
	c.fctx.SetStatusCode(code)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString(s)
	return nil
}

// Bytes writes a raw response body and leaves the content type alone, so the
// caller can set one with SetContentType or let fasthttp default it.
//
// The bytes are copied into the response buffer, so the caller may reuse the
// slice after the call returns.
func (c *Ctx) Bytes(code int, b []byte) error {
	c.fctx.SetStatusCode(code)
	c.fctx.SetBody(b)
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -run 'TestString|TestBytes|TestStatus|TestSetHeader|TestBody' -v`

Expected: PASS for all five tests.

- [ ] **Step 5: Write the allocation budget tests**

These are the contract from `docs/05-performance-model.md`, enforced as tests so a regression fails CI rather than merely looking worse in a benchmark.

Create `alloc_test.go`:

```go
package rice

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// budget asserts that fn allocates no more than want objects per call.
//
// The response buffers are warmed before measuring, because a live server is
// warm. Measuring a cold buffer would measure one-time setup, not steady state.
func budget(t *testing.T, name string, want float64, fn func()) {
	t.Helper()
	fn() // warm
	if got := testing.AllocsPerRun(1000, fn); got > want {
		t.Errorf("%s allocated %.1f objects per call, budget is %.0f", name, got, want)
	}
}

func TestAllocBudgetString(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.String", 0, func() {
		_ = c.String(200, "hello, world")
	})
}

func TestAllocBudgetBytes(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	c := &Ctx{}
	c.reset(nil, fctx)

	body := []byte("hello, world")
	budget(t, "Ctx.Bytes", 0, func() {
		_ = c.Bytes(200, body)
	})
}

func TestAllocBudgetMethodAndPath(t *testing.T) {
	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/users/42")

	c := &Ctx{}
	c.reset(nil, fctx)

	budget(t, "Ctx.Method", 0, func() { _ = c.Method() })
	budget(t, "Ctx.Path", 0, func() { _ = c.Path() })
}
```

- [ ] **Step 6: Run the budget tests**

Run: `go test ./... -run TestAllocBudget -v`

Expected: PASS.

If `TestAllocBudgetString` fails, do not raise the budget to make it pass. Raising a budget is a design change (principle 1) and needs a written justification. Find out what allocated instead:

```bash
go test -run TestAllocBudgetString -memprofile mem.out .
go tool pprof -top -alloc_objects mem.out
```

- [ ] **Step 7: Run the full suite with the race detector**

Run: `make test`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add ctx_response.go ctx_response_test.go alloc_test.go
git commit -m "feat: add Ctx response side with enforced allocation budgets"
```

---

### Task 7: App and request dispatch

**Files:**
- Create: `app.go`
- Test: `app_test.go`

**Interfaces:**
- Consumes: `Handler` and `Ctx` from Tasks 5 and 6.
- Produces:
  - `type Option func(*App)`
  - `type App struct{...}`
  - `func New(opts ...Option) *App`
  - `func (a *App) SetHandler(h Handler)`
  - `func (a *App) FasthttpHandler() fasthttp.RequestHandler`
  - `func (a *App) handle(fctx *fasthttp.RequestCtx)` (unexported, used by Task 8)

Two shape decisions worth stating, because a reviewer will ask.

`New` takes `opts ...Option` although M1 ships zero options. The signature is committed in `docs/03-core-concepts.md` and the first real option arrives in M7 with server timeouts. Four lines now avoids a signature change later, and calls written as `rice.New()` stay valid either way.

`FasthttpHandler` is exported so that benchmarks in package `bench` can call the dispatch path directly, without a socket. It is also the supported way to mount rice inside an existing fasthttp server, so it is real API rather than a test hook.

- [ ] **Step 1: Write the failing tests**

Create `app_test.go`:

```go
package rice

import (
	"errors"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestHandleInvokesTheRegisteredHandler(t *testing.T) {
	app := New()

	called := false
	app.SetHandler(func(c *Ctx) error {
		called = true
		return c.String(200, "ok")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.Header.SetMethod("GET")
	fctx.Request.SetRequestURI("/anything")

	app.handle(fctx)

	if !called {
		t.Fatal("handler was not invoked")
	}
	if got := fctx.Response.StatusCode(); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
	if got := string(fctx.Response.Body()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestHandleWithNoRegisteredHandlerReturns404(t *testing.T) {
	app := New()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/anything")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestHandleConvertsAReturnedErrorInto500(t *testing.T) {
	app := New()
	app.SetHandler(func(c *Ctx) error {
		return errors.New("the database is on fire")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/boom")

	app.handle(fctx)

	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
	if got := string(fctx.Response.Body()); got == "the database is on fire" {
		t.Error("the error cause leaked into the response body")
	}
}

func TestHandleDiscardsAPartialBodyWhenTheHandlerErrors(t *testing.T) {
	app := New()
	app.SetHandler(func(c *Ctx) error {
		_ = c.String(200, "partial output")
		return errors.New("failed after writing")
	})

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/partial")

	app.handle(fctx)

	if got := string(fctx.Response.Body()); got == "partial output" {
		t.Error("a partially written body survived an error return")
	}
	if got := fctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", got)
	}
}

func TestFasthttpHandlerDispatchesLikeHandle(t *testing.T) {
	app := New()
	app.SetHandler(func(c *Ctx) error { return c.String(200, "via fasthttp handler") })

	h := app.FasthttpHandler()

	fctx := &fasthttp.RequestCtx{}
	fctx.Request.SetRequestURI("/x")
	h(fctx)

	if got := string(fctx.Response.Body()); got != "via fasthttp handler" {
		t.Errorf("body = %q, want %q", got, "via fasthttp handler")
	}
}

func TestNewAppliesOptions(t *testing.T) {
	marker := false
	opt := func(a *App) { marker = true }

	New(opt)

	if !marker {
		t.Error("New did not apply the option it was given")
	}
}
```

The fourth test resolves the ambiguity ADR-0002 flagged: a handler that writes a response and then returns an error. The rule is that the error wins and the partial body is discarded. Encoding it as a test now means M5 cannot quietly change it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestHandle|TestFasthttpHandler|TestNewApplies' -v`

Expected: FAIL to compile, with `undefined: New`.

- [ ] **Step 3: Write the App**

Create `app.go`:

```go
package rice

import (
	"net"
	"sync"

	"github.com/valyala/fasthttp"
)

// Option configures an App at construction time.
//
// M1 ships no options. The first ones arrive in M7 with server timeouts. The
// variadic parameter is present now so that adding them later does not change
// the signature of New.
type Option func(*App)

// App is the root of a rice application. It owns the handler, the fasthttp
// server, and the listener.
type App struct {
	h   Handler
	srv *fasthttp.Server

	mu sync.Mutex
	ln net.Listener
}

// New creates an App.
func New(opts ...Option) *App {
	a := &App{}
	a.srv = &fasthttp.Server{
		Handler: a.handle,
		Name:    "rice",
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// SetHandler installs the single handler this App serves.
//
// M1 scaffolding. rice has no router yet, so every request reaches the same
// handler. M2 replaces this with per-method route registration and removes it.
func (a *App) SetHandler(h Handler) { a.h = h }

// FasthttpHandler returns the request handler this App installs on its server.
//
// Use it to mount rice inside an existing fasthttp server, or to drive the
// dispatch path directly in benchmarks without opening a socket.
func (a *App) FasthttpHandler() fasthttp.RequestHandler { return a.handle }

// handle is the dispatch path: one request in, one response out.
func (a *App) handle(fctx *fasthttp.RequestCtx) {
	// M1 allocates a Ctx per request on purpose. This line is the baseline that
	// M6's sync.Pool is measured against. Do not optimise it here.
	c := &Ctx{}
	c.reset(a, fctx)

	if a.h == nil {
		fctx.SetStatusCode(fasthttp.StatusNotFound)
		return
	}

	if err := a.h(c); err != nil {
		a.handleError(c, err)
	}
}

// handleError is M1's error funnel.
//
// It discards any partially written body, responds 500, and never writes the
// cause to the response: leaking internal error strings to clients is how
// databases end up described in HTTP responses.
//
// M5 replaces this with a configurable ErrorHandler and the HTTPError type. The
// err parameter is unused until then and is present so that the signature does
// not change.
func (a *App) handleError(c *Ctx, err error) {
	c.fctx.ResetBody()
	c.fctx.SetStatusCode(fasthttp.StatusInternalServerError)
	c.fctx.SetContentType(MIMETextPlainUTF8)
	c.fctx.SetBodyString("Internal Server Error")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -run 'TestHandle|TestFasthttpHandler|TestNewApplies' -v`

Expected: PASS for all six tests.

- [ ] **Step 5: Verify vet is happy with the unused parameter**

Run: `make lint`

Expected: exit 0. `go vet` does not flag unused function parameters. If a future linter does, keep the parameter and silence the linter rather than dropping it, because M5 needs it.

- [ ] **Step 6: Commit**

```bash
git add app.go app_test.go
git commit -m "feat: add App with request dispatch and a minimal error funnel"
```

---

### Task 8: The server, over a real socket

**Files:**
- Create: `server.go`
- Test: `server_test.go`

**Interfaces:**
- Consumes: `App` from Task 7.
- Produces:
  - `var ErrShutdownTimeout error`
  - `func (a *App) Run(addr string) error`
  - `func (a *App) Serve(ln net.Listener) error`
  - `func (a *App) Addr() string`
  - `func (a *App) Shutdown(ctx context.Context) error`

`Serve` is exported rather than kept internal because tests need to supply their own listener, and because serving on a listener someone else created is a legitimate thing to want.

`Shutdown` takes a `context.Context` to match the signature committed in `docs/03-core-concepts.md`. fasthttp's own `Shutdown` takes no deadline, so it runs in a goroutine and the deadline is enforced by selecting against `ctx.Done()`. That is honest but blunt: on timeout the server keeps draining in the background. M7 refines it.

- [ ] **Step 1: Write the failing integration tests**

Create `server_test.go`. Note the package clause: these are black-box tests in `rice_test`, exercising only exported API, which is how a user would use it.

```go
package rice_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// waitForAddr polls until the app has bound a listener, or fails the test.
func waitForAddr(t *testing.T, app *rice.App) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := app.Addr(); addr != "" {
			return addr
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server did not bind a listener within 2s")
	return ""
}

// get issues a real HTTP request with the standard library client, which proves
// rice speaks HTTP to something that is not fasthttp.
func get(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestServeAnswersARealRequestOnAnEphemeralPort(t *testing.T) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error {
		return c.String(200, "hello "+string(c.Path()))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/world")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "hello /world" {
		t.Errorf("body = %q, want %q", body, "hello /world")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

func TestRunBindsTheGivenAddress(t *testing.T) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error { return c.String(200, "up") })

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run("127.0.0.1:0") }()

	addr := waitForAddr(t, app)

	status, body := get(t, addr, "/")
	if status != 200 || body != "up" {
		t.Errorf("got status %d body %q, want 200 %q", status, body, "up")
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
	if err := <-errCh; err != nil {
		t.Errorf("Run returned %v after shutdown, want nil", err)
	}
}

func TestRunReturnsAnErrorOnAnUnbindableAddress(t *testing.T) {
	app := rice.New()

	// Port 1 requires privileges this test does not have.
	if err := app.Run("127.0.0.1:1"); err == nil {
		t.Error("Run returned nil for an unbindable address, want an error")
	}
}

func TestShutdownStopsAcceptingNewConnections(t *testing.T) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error { return c.String(200, "up") })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)
	if status, _ := get(t, addr, "/"); status != 200 {
		t.Fatalf("server was not up before shutdown: status %d", status)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}

	client := &http.Client{Timeout: time.Second}
	if _, err := client.Get("http://" + addr + "/"); err == nil {
		t.Error("a request succeeded after shutdown, want a connection error")
	}
}

func TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses(t *testing.T) {
	release := make(chan struct{})
	inFlight := make(chan struct{})

	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error {
		close(inFlight)
		<-release // hold the request open past the shutdown deadline
		return c.String(200, "finally")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()

	addr := waitForAddr(t, app)

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get("http://" + addr + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-inFlight // the handler is now blocked

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = app.Shutdown(ctx)
	close(release) // let the handler finish so the goroutine does not leak

	if err != rice.ErrShutdownTimeout {
		t.Errorf("Shutdown returned %v, want ErrShutdownTimeout", err)
	}
}

func TestAddrIsEmptyBeforeServing(t *testing.T) {
	app := rice.New()
	if got := app.Addr(); got != "" {
		t.Errorf("Addr() = %q before serving, want empty string", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestServe|TestRun|TestShutdown|TestAddr' -v`

Expected: FAIL to compile, with `undefined: app.Serve` and `undefined: rice.ErrShutdownTimeout`.

- [ ] **Step 3: Write the server**

Create `server.go`:

```go
package rice

import (
	"context"
	"errors"
	"net"
)

// ErrShutdownTimeout is returned by Shutdown when in-flight requests did not
// finish before the context deadline. The server keeps draining in the
// background: fasthttp's Shutdown has no deadline of its own, so rice can stop
// waiting but cannot stop the drain.
var ErrShutdownTimeout = errors.New("rice: shutdown timed out")

// Run binds addr and serves until Shutdown is called.
//
// It blocks. Use "127.0.0.1:0" to bind an ephemeral port and read the result
// back with Addr.
func (a *App) Run(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return a.Serve(ln)
}

// Serve serves on an existing listener and blocks until Shutdown is called.
func (a *App) Serve(ln net.Listener) error {
	a.mu.Lock()
	a.ln = ln
	a.mu.Unlock()

	return a.srv.Serve(ln)
}

// Addr returns the bound address, or the empty string if the App is not serving.
func (a *App) Addr() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.ln == nil {
		return ""
	}
	return a.ln.Addr().String()
}

// Shutdown stops accepting new connections and waits for in-flight requests to
// finish, or for ctx to be done, whichever comes first.
//
// M1 is blunt: it runs fasthttp's deadline-free Shutdown in a goroutine and
// races it against the context. M7 adds lifecycle hooks and finer draining.
func (a *App) Shutdown(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- a.srv.Shutdown() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ErrShutdownTimeout
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -run 'TestServe|TestRun|TestShutdown|TestAddr' -v`

Expected: PASS for all six tests.

If `TestRunReturnsAnErrorOnAnUnbindableAddress` passes on a machine where the test runs as root, port 1 will bind. In that case change the address to `"256.0.0.1:80"`, which cannot be parsed as an IP and fails on any machine.

- [ ] **Step 5: Run the full suite with the race detector**

Run: `make test`

Expected: PASS with no race reports. The race detector matters here specifically: `Addr` reads `a.ln` from the test goroutine while `Serve` writes it from another, which is why both take the mutex.

- [ ] **Step 6: Commit**

```bash
git add server.go server_test.go
git commit -m "feat: add Run, Serve, Addr, and deadline-aware Shutdown"
```

---

### Task 9: M1 benchmarks and the unpooled baseline

**Files:**
- Create: `bench/rice_bench_test.go`
- Create: `bench/results/M1-minimal-server.txt` (generated, then committed)
- Modify: `docs/05-performance-model.md` (three rows move from TARGET to measured)

**Interfaces:**
- Consumes: `newRequestCtx` from Task 2, `rice.New`, `SetHandler`, `FasthttpHandler`, `Ctx.String` from Tasks 6 to 8.
- Produces: the recorded numbers that Task 10 writes into the milestone retrospective, and that M6 is compared against.

- [ ] **Step 1: Write the rice benchmarks**

Create `bench/rice_bench_test.go`:

```go
package bench

import (
	"testing"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
)

// BenchmarkRiceDispatch measures the full M1 dispatch path: allocate a Ctx,
// bind it, call the handler, write a plaintext body.
//
// It is expected to report exactly one allocation per request, for the Ctx.
// That allocation is deliberate (see app.go) and is the number M6's sync.Pool
// has to remove. Compare against BenchmarkFasthttpBaseline, which is the same
// work with no framework.
func BenchmarkRiceDispatch(b *testing.B) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error {
		return c.String(fasthttp.StatusOK, "hello")
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/hello")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkRiceDispatchNotFound measures the path where no handler is
// registered, which skips the handler call entirely.
func BenchmarkRiceDispatchNotFound(b *testing.B) {
	app := rice.New()

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/missing")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}

// BenchmarkCtxSetHeader measures a response header write. The performance model
// records this as amortised zero; this benchmark is where that claim is checked
// rather than assumed.
func BenchmarkCtxSetHeader(b *testing.B) {
	app := rice.New()
	app.SetHandler(func(c *rice.Ctx) error {
		c.SetHeader("X-Trace", "abc123")
		return c.String(fasthttp.StatusOK, "hello")
	})

	h := app.FasthttpHandler()
	fctx := newRequestCtx("GET", "/hdr")
	h(fctx) // warm

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h(fctx)
	}
}
```

- [ ] **Step 2: Run the benchmarks and read the numbers**

```bash
go test ./bench/... -run '^$' -bench . -benchmem -count=1
```

Expected: `BenchmarkFasthttpBaseline` at `0 allocs/op`, and `BenchmarkRiceDispatch` at `1 allocs/op`. If rice reports more than one, something other than the `Ctx` is allocating and it must be found before the number is recorded, because a baseline nobody understands is worthless.

To find it:

```bash
go test ./bench/... -run '^$' -bench BenchmarkRiceDispatch -memprofile mem.out -count=1
go tool pprof -top -alloc_objects mem.out
```

- [ ] **Step 3: Record the results**

```bash
make bench-record LABEL=M1-minimal-server
```

Expected: `bench/results/M1-minimal-server.txt` exists with the stamp header and ten runs of each benchmark.

- [ ] **Step 4: Compare against the M0 baseline**

```bash
go install golang.org/x/perf/cmd/benchstat@latest
benchstat bench/results/M0-fasthttp-baseline.txt bench/results/M1-minimal-server.txt
```

The two files contain different benchmark names, so benchstat will not pair them automatically. Read the two `BenchmarkFasthttpBaseline` figures against `BenchmarkRiceDispatch` by hand and write the delta into the milestone doc in Task 10. That delta, in nanoseconds and in allocations, is what M1 exists to produce.

- [ ] **Step 5: Update the performance model with measured values**

In `docs/05-performance-model.md`, in the allocation budget table, change the Status column from `TARGET (M1)` to `MEASURED M1` on exactly the two rows carrying that label, and no others:

```markdown
| `c.Method`, `c.Path` | 0 | MEASURED M1 |
| `c.String`, `c.Bytes` | 0 | MEASURED M1 |
```

Those are precisely the budgets Task 6's `alloc_test.go` asserts. Every other row keeps its TARGET label, because the code it describes does not exist yet.

Then add one new row immediately below `| Chain call, 5 middleware | 0 | TARGET (M4) |`, recording M1's end-to-end figure:

```markdown
| **End to end: single handler, unpooled Ctx (M1 baseline)** | 1 | MEASURED M1 |
```

This row is the one M6 deletes when pooling brings it to zero, and its number is what M6's success is measured against.

Then add a sentence under the table naming the file the numbers came from, so a reader can find them:

```markdown
Measured values come from `bench/results/M1-minimal-server.txt`. Re-run
`make bench-record` on your own machine before comparing.
```

- [ ] **Step 6: Verify**

```bash
make lint
make test
make bench
```

Expected: all exit 0.

- [ ] **Step 7: Commit**

```bash
git add bench docs/05-performance-model.md
git commit -m "bench: record M1 dispatch baseline and update the performance model"
```

---

### Task 10: Close out M1

**Files:**
- Create: `docs/milestones/M1-minimal-server.md`
- Modify: `docs/04-roadmap.md` (M1 status marker)
- Modify: `docs/progress.md` (prepend an entry)
- Modify: `docs/adr/0002-handler-returns-error.md` (record the resolved open question)

**Interfaces:**
- Consumes: the recorded numbers from Task 9.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Write the milestone retrospective**

Create `docs/milestones/M1-minimal-server.md` from `docs/milestones/TEMPLATE.md`. Fill in every section, including the Measurements table with the `BenchmarkRiceDispatch` numbers and the hardware stamp from `bench/results/M1-minimal-server.txt`.

The Design notes section must record the two decisions this milestone forced that the design docs did not anticipate:

1. `FasthttpHandler` had to be exported so that benchmarks outside package `rice` could reach the dispatch path without a socket. It turned out to be real API rather than a test hook, because mounting rice inside an existing fasthttp server needs the same thing.
2. `Shutdown` takes a `context.Context` but fasthttp's own `Shutdown` has no deadline, so the timeout is enforced by racing a goroutine. On timeout the drain continues in the background, which means `ErrShutdownTimeout` reports "I stopped waiting", not "I stopped the server".

If either turns out to have had a defensible alternative, promote it to an ADR rather than leaving it in the milestone notes.

- [ ] **Step 2: Resolve the open question in ADR-0002**

ADR-0002 lists as a consequence that a handler which writes a response and then returns an error is ambiguous, and that the case needs a documented rule and a test. Task 7 settled it: the error wins and the partial body is discarded.

Append to the Consequences section of `docs/adr/0002-handler-returns-error.md`:

```markdown
**Resolved in M1:** the ambiguous case above is settled. A handler that writes a
response and then returns an error has its body discarded and receives the error
handler's response. The rule is enforced by
`TestHandleDiscardsAPartialBodyWhenTheHandlerErrors` in `app_test.go`.
```

This is an addition, not a rewrite. ADRs are append-only, and adding a resolution to an open consequence does not change the decision.

- [ ] **Step 3: Mark M1 done in the roadmap**

In `docs/04-roadmap.md`, change the M1 heading marker from `☐` to `☑`:

```
### ☑ M1 — Minimal server
```

- [ ] **Step 4: Prepend a journal entry**

In `docs/progress.md`, insert a new M1 entry at the top of the entry list, above the M0 entry added in Task 4, using the four-field shape: Did, Learned, Measured, Next.

The Measured field carries the concrete delta between `BenchmarkFasthttpBaseline` and `BenchmarkRiceDispatch`, in nanoseconds per operation and allocations per operation. That single comparison is M1's whole output.

The Next field points at M2: a deliberately naive `map[string]Handler` router, whose purpose is to produce a number for M3's radix tree to beat.

- [ ] **Step 5: Final verification of both milestones**

```bash
make lint
make test
make cover
make bench
git status --short
```

Expected: lint, test and bench exit 0. `git status --short` is empty, meaning everything is committed. Confirm by eye that `docs/04-roadmap.md` shows `☑` for M0 and M1 and `☐` for M2 through M8.

- [ ] **Step 6: Commit**

```bash
git add docs
git commit -m "docs: close out M1 with retrospective, measured baseline, and ADR-0002 resolution"
```

---

## Plan Self-Review

Run through this after execution, or before starting if reviewing the plan itself.

**Spec coverage against `docs/04-roadmap.md`:**

| M0 requirement | Task |
| --- | --- |
| `go.mod` at the module path, fasthttp pinned | 1 |
| Package skeleton matching the architecture doc | 1 (`doc.go`), 5 to 8 (the rest, as each file's milestone arrives) |
| Makefile with test, bench, lint, cover | 1 |
| CI running tests and benchmarks on push | 3 |
| `bench/` harness writing results to a committed file | 2 |
| Exit: `make test` and `make bench` run green | 2 Step 8 |

| M1 requirement | Task |
| --- | --- |
| `App`, `Handler`, thin `Ctx` over `*fasthttp.RequestCtx` | 5, 7 |
| `Run(addr)` and a blunt `Shutdown` | 8 |
| `c.String` and `c.Bytes` | 6 |
| Exit: integration test on an ephemeral port with a real request | 8 |
| Exit: benchmark recording allocations with an unpooled `Ctx` | 9 |

**Deliberate deviation from the architecture doc.** `docs/02-architecture.md` lists a package skeleton including `ctx_request.go`, `group.go`, `error.go`, `pool.go`, `internal/` and `middleware/`. This plan does not create them. Empty files are worse than absent ones: they suggest a design is implemented when it is not. Each arrives with the milestone that fills it. `ctx.go` holds the read side in M1 and splits into `ctx_request.go` when M3 adds parameters and query accessors.

**Naming consistency check.** `reset(app, fctx)` in Task 5 is called with the same argument order in Task 7. `newTestCtx` (Task 6, package `rice`) and `newRequestCtx` (Task 2, package `bench`) are different helpers in different packages, deliberately named differently so a reader is never unsure which one is in scope. `FasthttpHandler` is spelled identically in Tasks 7 and 9.

**Known risks.**

1. `TestAllocBudgetString` depends on fasthttp reusing its response buffer across writes. If a future fasthttp version changes that, the test fails. That is the test doing its job: the budget is a contract with the transport, and a change to it is news.
2. `TestShutdownReturnsErrShutdownTimeoutWhenTheDeadlinePasses` uses a 100ms deadline and could flake on a heavily loaded CI machine. If it flakes, raise the deadline rather than deleting the test.
3. The exact allocation count in `BenchmarkRiceDispatch` is predicted as 1 but not guaranteed. Task 9 Step 2 requires understanding the number before recording it, rather than recording whatever appears.
