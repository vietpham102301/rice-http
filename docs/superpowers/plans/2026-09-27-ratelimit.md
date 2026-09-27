# RateLimit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `middleware.RateLimit` admits a `Limit` of requests per key with GCRA, answers the rest with 429 and `Retry-After`, and counts through a `RateLimitStore` whose in-memory implementation, `MemoryStore`, needs no goroutine.

**Architecture:** Two files in `middleware/`. `ratelimit.go` holds `Limit` (with the unexported `params` that validates it), the `RateLimitStore` interface, `RateLimitConfig`, `RateLimit`, the default key and the `Retry-After` rounding. `memorystore.go` holds `MemoryStore`: 64 mutex-guarded shards of `map[string]int64`, GCRA's read-decide-write under one lock, times kept relative to the store's creation on the monotonic clock, and a sweep of expired keys at most once a minute per shard, run inside `Take`.

**Tech Stack:** Go 1.25 (`hash/maphash`, `sync.WaitGroup.Go`), fasthttp v1.73.0. No new dependency.

**Spec:** [docs/superpowers/specs/2026-09-27-ratelimit-design.md](../specs/2026-09-27-ratelimit-design.md)

## Global Constraints

- **Branch:** `ratelimit-middleware`, already created, holding the spec commit. Do not commit to `main`.
- **Exported surface added:** exactly `Limit`, `RateLimitStore`, `RateLimitConfig`, `RateLimit`, `MemoryStore`, `NewMemoryStore` and `(*MemoryStore).Take` in package `middleware`. Nothing exported changes in core.
- **Core `go.mod`/`go.sum` and `otelrice/` must not change.**
- **Panics carry the prefix `rice: middleware.RateLimit:`**; `MemoryStore.Take` errors carry `rice: middleware.MemoryStore:`. Both for: `Rate` ≤ 0, `Per` ≤ 0, `Burst` < 0, `Per/Rate` below 1 ns, and `Burst*Per/Rate` overflowing a `time.Duration` (the last is this plan's addition to the spec's list; see Task 1's note).
- **429 body is `Too Many Requests`**, from one shared `*rice.HTTPError`; `Retry-After` is whole seconds rounded up, at least 1; an allowed request gets no header.
- **An error from `KeyFunc` or the store goes to the funnel as it is** (500 by default); `Take` is not called after a `KeyFunc` error.
- **Budgets, exact, with and without `-race`:** RateLimit allowed 1, RateLimit 429 1, `MemoryStore.Take` on an existing key 0.
- **Run `make test`, `make test-debug` and `make lint` before each commit; never commit with a failing suite.** Root coverage (`make cover`) stays at or above 99.6%. Commit messages: subject, blank line, then `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` on its own line.

## Review Focus

Inputs the spec implies but does not enumerate. Each has a test in the task that owns the code.

1. **Many goroutines hitting one key at the same instant** — exactly `Burst` admitted, no race. (Task 1, `TestMemoryStoreConcurrentTakesAdmitExactlyTheBurst`)
2. **A client that keeps sending while limited** — its wait does not grow. (Task 1, `TestMemoryStoreDoesNotChargeADeniedRequest`)
3. **A huge `Burst`** (`math.MaxInt` with `Per` an hour) — refused, not an overflow that admits everything. (Task 1 and Task 2)
4. **A wait of a few nanoseconds** — `Retry-After: 1`, never `0`. (Task 2, `TestRateLimitOutcomes`)
5. **An IPv4 client reaching the service over an IPv4-mapped IPv6 socket** — the same key as over IPv4. (Task 2, `TestRateLimitDefaultKey`)

---

## File Structure

| File | Responsibility | Task |
| --- | --- | --- |
| `middleware/ratelimit.go` (create) | `Limit`, `params`, `RateLimitStore`; then `RateLimitConfig`, `RateLimit`, `retryAfter`, `clientKey`, `errTooManyRequests` | 1, 2 |
| `middleware/memorystore.go` (create) | `MemoryStore`, `NewMemoryStore`, `Take` | 1 |
| `middleware/memorystore_test.go` (create, package `middleware`) | GCRA, sweep and concurrency tests against a controlled clock | 1 |
| `middleware/ratelimit_test.go` (create, package `middleware_test`) | middleware behaviour, default key, placement, panics | 2 |
| `middleware/alloc_test.go` (modify) | three budgets | 3 |
| `docs/adr/0022-rate-limiting-is-gcra-behind-a-store.md` (create), `docs/adr/README.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md`, `middleware/doc.go`, `README.md` | documentation | 4 |

---

### Task 1: Limit, RateLimitStore and MemoryStore

**Files:**
- Create: `middleware/ratelimit.go`, `middleware/memorystore.go`, `middleware/memorystore_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `type Limit struct{ Rate int; Per time.Duration; Burst int }`; unexported `func (l Limit) params() (interval, burst int64, msg string)` (msg "" when the Limit works); `type RateLimitStore interface{ Take(ctx context.Context, key string, l Limit) (wait time.Duration, err error) }`; `type MemoryStore struct` with unexported fields `seed maphash.Seed`, `base time.Time`, `now func() time.Time`, `sweepEvery time.Duration`, `shards [memoryShards]memoryShard`; `func NewMemoryStore() *MemoryStore`; `func (s *MemoryStore) Take(ctx context.Context, key string, l Limit) (time.Duration, error)`; const `memoryShards = 64`.

**Note — a panic the spec does not list.** GCRA computes `Burst*interval`; with `Burst` near `math.MaxInt` it overflows `int64` and goes negative, which would admit every request. `params` refuses it with "Burst * Per / Rate overflows a time.Duration". This is a ruling to ledger at execution: the spec's intent (a `Limit` that cannot work is refused) covers it.

- [ ] **Step 1: Confirm the branch**

Run: `git branch --show-current`
Expected: `ratelimit-middleware`

- [ ] **Step 2: Write the failing tests**

Create `middleware/memorystore_test.go`:

```go
package middleware

import (
	"context"
	"hash/maphash"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testStore returns a MemoryStore whose clock reads *at past its creation.
func testStore(at *time.Duration) *MemoryStore {
	s := NewMemoryStore()
	s.now = func() time.Time { return s.base.Add(*at) }
	return s
}

// take calls Take and fails the test on an error.
func take(t *testing.T, s *MemoryStore, key string, l Limit) time.Duration {
	t.Helper()
	wait, err := s.Take(context.Background(), key, l)
	if err != nil {
		t.Fatalf("Take(%q): %v", key, err)
	}
	return wait
}

// admitted takes n times at the current instant and returns how many were
// allowed.
func admitted(t *testing.T, s *MemoryStore, key string, l Limit, n int) int {
	t.Helper()
	ok := 0
	for range n {
		if take(t, s, key, l) == 0 {
			ok++
		}
	}
	return ok
}

// tenPerSecond is an emission interval of 100ms and a burst of 3.
var tenPerSecond = Limit{Rate: 10, Per: time.Second, Burst: 3}

func TestMemoryStoreAdmitsABurstThenOneEveryInterval(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	if got := admitted(t, s, "k", tenPerSecond, 3); got != 3 {
		t.Fatalf("a fresh key admitted %d of 3, want the whole burst", got)
	}
	if wait := take(t, s, "k", tenPerSecond); wait != 100*time.Millisecond {
		t.Fatalf("the first request past the burst waits %v, want 100ms", wait)
	}
	at = 100 * time.Millisecond
	if wait := take(t, s, "k", tenPerSecond); wait != 0 {
		t.Fatalf("after waiting the interval, wait %v, want 0", wait)
	}
	if wait := take(t, s, "k", tenPerSecond); wait != 100*time.Millisecond {
		t.Fatalf("the next one waits %v, want 100ms", wait)
	}
	at += 300 * time.Millisecond // Burst * interval of idleness
	if got := admitted(t, s, "k", tenPerSecond, 4); got != 3 {
		t.Fatalf("after idling, admitted %d of 4, want the burst of 3 back", got)
	}
}

func TestMemoryStoreBurst(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	five := Limit{Rate: 5, Per: time.Second}
	if got := admitted(t, s, "zero", five, 10); got != 5 {
		t.Errorf("Burst 0 admitted %d, want Rate (5)", got)
	}
	if wait := take(t, s, "zero", five); wait != 200*time.Millisecond {
		t.Errorf("Burst 0: wait %v, want 200ms", wait)
	}
	one := Limit{Rate: 5, Per: time.Second, Burst: 1}
	if got := admitted(t, s, "one", one, 10); got != 1 {
		t.Errorf("Burst 1 admitted %d, want 1", got)
	}
	if wait := take(t, s, "one", one); wait != 200*time.Millisecond {
		t.Errorf("Burst 1: wait %v, want 200ms", wait)
	}
}

func TestMemoryStoreDoesNotChargeADeniedRequest(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	admitted(t, s, "k", tenPerSecond, 3)
	for range 50 {
		take(t, s, "k", tenPerSecond)
	}
	if wait := take(t, s, "k", tenPerSecond); wait != 100*time.Millisecond {
		t.Errorf("after hammering while limited, wait %v, want still 100ms", wait)
	}
}

func TestMemoryStoreKeysAndLimitsAreIndependent(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	admitted(t, s, "a", tenPerSecond, 3)
	if got := admitted(t, s, "b", tenPerSecond, 3); got != 3 {
		t.Errorf("key b admitted %d of 3 after key a used its burst", got)
	}
	slow := Limit{Rate: 1, Per: time.Hour, Burst: 2}
	if got := admitted(t, s, "c", slow, 3); got != 2 {
		t.Errorf("key c under its own Limit admitted %d, want 2", got)
	}
}

func TestMemoryStoreClockEarlierThanTheStoredTime(t *testing.T) {
	at := 500 * time.Millisecond
	s := testStore(&at)

	admitted(t, s, "k", tenPerSecond, 3)
	at = 400 * time.Millisecond // a request that read the clock before another
	if wait := take(t, s, "k", tenPerSecond); wait != 200*time.Millisecond {
		t.Errorf("wait %v, want 200ms: the burst is spent until 600ms", wait)
	}
}

func TestMemoryStoreRejectsALimitThatCannotWork(t *testing.T) {
	cases := map[string]Limit{
		"zero Rate":         {Per: time.Second},
		"zero Per":          {Rate: 1},
		"negative Burst":    {Rate: 1, Per: time.Second, Burst: -1},
		"sub-ns interval":   {Rate: 2, Per: 1},
		"overflowing burst": {Rate: 1, Per: time.Hour, Burst: math.MaxInt},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewMemoryStore().Take(context.Background(), "k", l)
			if err == nil || !strings.HasPrefix(err.Error(), "rice: middleware.MemoryStore:") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// sameShard returns n distinct keys that land in one shard.
func sameShard(s *MemoryStore, n int) []string {
	var keys []string
	want := maphash.String(s.seed, "k0") % memoryShards
	for i := 0; len(keys) < n; i++ {
		k := "k" + strconv.Itoa(i)
		if maphash.String(s.seed, k)%memoryShards == want {
			keys = append(keys, k)
		}
	}
	return keys
}

func TestMemoryStoreSweepsExpiredKeys(t *testing.T) {
	var at time.Duration
	s := testStore(&at)
	s.sweepEvery = time.Second

	keys := sameShard(s, 3)
	expiring, lasting, trigger := keys[0], keys[1], keys[2]
	take(t, s, expiring, tenPerSecond)                  // tat 100ms
	take(t, s, lasting, Limit{Rate: 1, Per: time.Hour}) // tat 1h
	sh := &s.shards[maphash.String(s.seed, expiring)%memoryShards]

	at = 999 * time.Millisecond
	take(t, s, trigger, tenPerSecond)
	if _, ok := sh.tat[expiring]; !ok {
		t.Fatal("swept before the sweep interval")
	}
	at = time.Second
	take(t, s, trigger, tenPerSecond)
	if _, ok := sh.tat[expiring]; ok {
		t.Error("an expired key survived the sweep")
	}
	if _, ok := sh.tat[lasting]; !ok {
		t.Error("the sweep deleted a key that has not expired")
	}
}

func TestMemoryStoreConcurrentTakesAdmitExactlyTheBurst(t *testing.T) {
	var at time.Duration
	s := testStore(&at)
	l := Limit{Rate: 10, Per: time.Second, Burst: 8}

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 64 {
		wg.Go(func() {
			if wait, _ := s.Take(context.Background(), "k", l); wait == 0 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if ok != 8 {
		t.Errorf("64 concurrent takes admitted %d, want exactly the burst (8)", ok)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test -count=1 -run MemoryStore ./middleware/`
Expected: FAIL to compile — `undefined: MemoryStore`, `undefined: Limit`, `undefined: NewMemoryStore`, `undefined: memoryShards`.

- [ ] **Step 4: Write `middleware/ratelimit.go`**

```go
package middleware

import (
	"context"
	"math"
	"time"
)

// Limit allows Rate requests every Per, with bursts of up to Burst requests:
// Limit{Rate: 100, Per: time.Minute, Burst: 20} admits 20 at once, then one
// every 600ms. Burst 0 means Rate.
type Limit struct {
	Rate  int
	Per   time.Duration
	Burst int
}

// params returns the emission interval and the burst in nanoseconds and
// requests, or a message saying why the Limit cannot work.
func (l Limit) params() (interval, burst int64, msg string) {
	switch {
	case l.Rate <= 0:
		return 0, 0, "Rate must be positive"
	case l.Per <= 0:
		return 0, 0, "Per must be positive"
	case l.Burst < 0:
		return 0, 0, "Burst may not be negative"
	}
	interval = int64(l.Per) / int64(l.Rate)
	if interval == 0 {
		return 0, 0, "Per / Rate is below one nanosecond"
	}
	burst = int64(l.Burst)
	if burst == 0 {
		burst = int64(l.Rate)
	}
	if burst > math.MaxInt64/interval {
		return 0, 0, "Burst * Per / Rate overflows a time.Duration"
	}
	return interval, burst, ""
}

// RateLimitStore keeps the state RateLimit counts with. Take charges one
// request to key under GCRA with limit l and reports how long the caller must
// wait before it would be allowed; 0 means allowed now, and a request that is
// not allowed is not charged. The check and the charge are one atomic step:
// a store shared by several instances runs them as one operation on the
// shared state (a Lua script in Redis, say), never as a read and a later write.
//
// Keys are the caller's namespace: two RateLimit middleware sharing a store
// must not produce the same key for different purposes.
type RateLimitStore interface {
	Take(ctx context.Context, key string, l Limit) (wait time.Duration, err error)
}

```

- [ ] **Step 5: Write `middleware/memorystore.go`**

```go
package middleware

import (
	"context"
	"errors"
	"hash/maphash"
	"sync"
	"time"
)

// memoryShards is how many independently locked maps a MemoryStore spreads
// its keys over.
const memoryShards = 64

// MemoryStore is a RateLimitStore in this process's memory: each instance of
// a service counts on its own. Build it with NewMemoryStore; it needs no
// Close.
//
// It starts no goroutine. A key whose theoretical arrival time has passed is
// the same as an absent one, so each shard deletes those keys at most once a
// minute, while serving a Take. Its memory is the keys active in the last
// Burst*Per/Rate, plus up to a minute of expired ones.
type MemoryStore struct {
	seed       maphash.Seed
	base       time.Time
	now        func() time.Time
	sweepEvery time.Duration
	shards     [memoryShards]memoryShard
}

type memoryShard struct {
	mu        sync.Mutex
	tat       map[string]int64 // key → theoretical arrival time, ns since base
	lastSweep int64
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{seed: maphash.MakeSeed(), base: time.Now(), now: time.Now, sweepEvery: time.Minute}
	for i := range s.shards {
		s.shards[i].tat = make(map[string]int64)
	}
	return s
}

// Take implements RateLimitStore. A Limit that RateLimit would refuse at
// construction returns an error.
func (s *MemoryStore) Take(_ context.Context, key string, l Limit) (time.Duration, error) {
	interval, burst, msg := l.params()
	if msg != "" {
		return 0, errors.New("rice: middleware.MemoryStore: " + msg)
	}
	// Measured on the monotonic clock from the store's creation, so a step of
	// the wall clock neither releases nor locks out every client.
	now := int64(s.now().Sub(s.base))

	sh := &s.shards[maphash.String(s.seed, key)%memoryShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if now-sh.lastSweep >= int64(s.sweepEvery) {
		for k, tat := range sh.tat {
			if tat <= now {
				delete(sh.tat, k)
			}
		}
		sh.lastSweep = now
	}

	tat, ok := sh.tat[key]
	if !ok || tat < now {
		tat = now
	}
	next := tat + interval
	if allowAt := next - burst*interval; now < allowAt {
		return time.Duration(allowAt - now), nil
	}
	sh.tat[key] = next
	return 0, nil
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race -count=1 -run MemoryStore -v ./middleware/`
Expected: PASS for all eight tests (`TestMemoryStoreAdmitsABurstThenOneEveryInterval`, `TestMemoryStoreBurst`, `TestMemoryStoreDoesNotChargeADeniedRequest`, `TestMemoryStoreKeysAndLimitsAreIndependent`, `TestMemoryStoreClockEarlierThanTheStoredTime`, `TestMemoryStoreRejectsALimitThatCannotWork` with five subtests, `TestMemoryStoreSweepsExpiredKeys`, `TestMemoryStoreConcurrentTakesAdmitExactlyTheBurst`).

- [ ] **Step 7: Prove the tests bite**

Three mutations, each reverted after its run (`git checkout -- middleware/memorystore.go` restores nothing yet — the file is uncommitted — so copy it aside first: `cp middleware/memorystore.go /tmp/ms.go`, and `cp /tmp/ms.go middleware/memorystore.go` after each):
1. Insert `sh.tat[key] = next` on the line after `next := tat + interval`. Run `go test -count=1 -run MemoryStore ./middleware/`. Expected: FAIL (a denied request is charged).
2. Delete the two lines `sh.mu.Lock()` and `defer sh.mu.Unlock()`. Run `go test -race -count=1 -run Concurrent ./middleware/`. Expected: `WARNING: DATA RACE`.
3. Change `if tat <= now {` in the sweep to `if tat <= now && false {`. Run `go test -count=1 -run Sweeps ./middleware/`. Expected: FAIL "an expired key survived the sweep".

After restoring, run `diff /tmp/ms.go middleware/memorystore.go` — Expected: no output.

- [ ] **Step 8: Run the suites and commit**

Run: `make test && make test-debug && make lint`
Expected: all pass.

```bash
git add middleware/ratelimit.go middleware/memorystore.go middleware/memorystore_test.go
git commit -m "middleware: MemoryStore counts with GCRA in 64 shards and sweeps without a goroutine

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: The RateLimit middleware

**Files:**
- Modify: `middleware/ratelimit.go` (replace whole file)
- Create: `middleware/ratelimit_test.go`

**Interfaces:**
- Consumes: Task 1's `Limit`, `params`, `RateLimitStore`, `NewMemoryStore`; test helpers `newRequest(method, uri string, headers map[string]string) *fasthttp.RequestCtx` (middleware/alloc_test.go) and `hdr(fctx *fasthttp.RequestCtx, name string) string` (middleware/cors_test.go); `middleware.Timeout`, `middleware.RealIP`, `middleware.CORS`; `rice.Option`, `rice.WithErrorHandler`; `(*fasthttp.RequestCtx).SetRemoteAddr`.
- Produces: `type RateLimitConfig struct{ Limit Limit; KeyFunc func(c *rice.Ctx) (string, error); Store RateLimitStore }`; `func RateLimit(cfg RateLimitConfig) rice.Middleware`; unexported `errTooManyRequests`, `retryAfter(time.Duration) int64`, `clientKey(*rice.Ctx) (string, error)`. Test declarations `type fakeStore`, `var perMinute`, `func limitedApp`, `func keyFor`.

- [ ] **Step 1: Write the failing tests**

Create `middleware/ratelimit_test.go`:

```go
package middleware_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/middleware"
)

// fakeStore answers every Take with wait and err, and records what it saw.
type fakeStore struct {
	wait  time.Duration
	err   error
	calls int
	keys  []string
	ctx   context.Context
	limit middleware.Limit
}

func (s *fakeStore) Take(ctx context.Context, key string, l middleware.Limit) (time.Duration, error) {
	s.calls++
	s.keys = append(s.keys, key)
	s.ctx, s.limit = ctx, l
	return s.wait, s.err
}

var perMinute = middleware.Limit{Rate: 60, Per: time.Minute}

// limitedApp serves GET /x behind RateLimit with cfg; ran reports whether the
// handler ran.
func limitedApp(cfg middleware.RateLimitConfig, ran *bool, opts ...rice.Option) *rice.App {
	app := rice.New(opts...)
	app.Use(middleware.RateLimit(cfg))
	app.GET("/x", func(c *rice.Ctx) error {
		*ran = true
		return c.String(200, "ok")
	})
	return app
}

func TestRateLimitOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		wait       time.Duration
		status     int
		retryAfter string
	}{
		{"allowed", 0, 200, ""},
		{"a wait under a second rounds up to 1", 300 * time.Millisecond, 429, "1"},
		{"a fractional wait rounds up", 1200 * time.Millisecond, 429, "2"},
		{"a whole wait stays whole", 2 * time.Second, 429, "2"},
		{"a wait of a nanosecond is 1", 1, 429, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			store := &fakeStore{wait: tc.wait}
			fctx := newRequest("GET", "/x", nil)
			limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: store}, &ran).FasthttpHandler()(fctx)
			if got := fctx.Response.StatusCode(); got != tc.status {
				t.Errorf("status %d, want %d", got, tc.status)
			}
			if got := hdr(fctx, "Retry-After"); got != tc.retryAfter {
				t.Errorf("Retry-After %q, want %q", got, tc.retryAfter)
			}
			if ran != (tc.status == 200) {
				t.Errorf("handler ran = %v with status %d", ran, tc.status)
			}
			if tc.status == 429 && string(fctx.Response.Body()) != "Too Many Requests" {
				t.Errorf("body %q", fctx.Response.Body())
			}
			if store.limit != perMinute {
				t.Errorf("Take got Limit %+v, want %+v", store.limit, perMinute)
			}
		})
	}
}

func TestRateLimitErrors(t *testing.T) {
	down := errors.New("store down")
	t.Run("a store error is a 500", func(t *testing.T) {
		var ran bool
		fctx := newRequest("GET", "/x", nil)
		limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: &fakeStore{wait: time.Second, err: down}}, &ran).FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 500 || ran || hdr(fctx, "Retry-After") != "" {
			t.Errorf("got %d, ran %v, Retry-After %q", fctx.Response.StatusCode(), ran, hdr(fctx, "Retry-After"))
		}
	})
	t.Run("a KeyFunc error is a 500 and never reaches the store", func(t *testing.T) {
		var ran bool
		store := &fakeStore{}
		keyFunc := func(*rice.Ctx) (string, error) { return "", down }
		fctx := newRequest("GET", "/x", nil)
		limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: store, KeyFunc: keyFunc}, &ran).FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 500 || ran || store.calls != 0 {
			t.Errorf("got %d, ran %v, Take called %d times", fctx.Response.StatusCode(), ran, store.calls)
		}
	})
	t.Run("a custom ErrorHandler sees the store's error", func(t *testing.T) {
		var ran bool
		var seen error
		fctx := newRequest("GET", "/x", nil)
		limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: &fakeStore{err: down}}, &ran,
			rice.WithErrorHandler(func(c *rice.Ctx, err error) { seen = err; _ = c.String(503, "later") })).FasthttpHandler()(fctx)
		if !errors.Is(seen, down) || fctx.Response.StatusCode() != 503 {
			t.Errorf("ErrorHandler saw %v and answered %d", seen, fctx.Response.StatusCode())
		}
	})
}

func TestRateLimitKeyFuncChoosesTheKey(t *testing.T) {
	var ran bool
	store := &fakeStore{}
	keyFunc := func(c *rice.Ctx) (string, error) { return string(c.Header("X-User")), nil }
	app := limitedApp(middleware.RateLimitConfig{Limit: perMinute, Store: store, KeyFunc: keyFunc}, &ran)
	app.FasthttpHandler()(newRequest("GET", "/x", map[string]string{"X-User": "ann"}))
	app.FasthttpHandler()(newRequest("GET", "/x", nil))
	if len(store.keys) != 2 || store.keys[0] != "ann" || store.keys[1] != "" {
		t.Errorf("keys %q, want [ann \"\"]", store.keys)
	}
}

func TestRateLimitPassesTheRequestContext(t *testing.T) {
	store := &fakeStore{}
	app := rice.New()
	app.Use(middleware.Timeout(time.Minute), middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: store}))
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	app.FasthttpHandler()(newRequest("GET", "/x", nil))
	if store.ctx == nil {
		t.Fatal("Take was not called")
	}
	if _, ok := store.ctx.Deadline(); !ok {
		t.Error("Take did not receive the Timeout's deadline")
	}
}

// keyFor returns the default key RateLimit hands the store for a request from
// addr, with headers.
func keyFor(t *testing.T, addr string, headers map[string]string, mws ...rice.Middleware) string {
	t.Helper()
	store := &fakeStore{}
	app := rice.New()
	app.Use(append(mws, middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: store}))...)
	app.GET("/x", func(c *rice.Ctx) error { return nil })
	fctx := newRequest("GET", "/x", headers)
	fctx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP(addr), Port: 4711})
	app.FasthttpHandler()(fctx)
	if len(store.keys) != 1 {
		t.Fatalf("Take called %d times", len(store.keys))
	}
	return store.keys[0]
}

func TestRateLimitDefaultKey(t *testing.T) {
	same := func(a, b string) bool { return keyFor(t, a, nil) == keyFor(t, b, nil) }
	if same("203.0.113.1", "203.0.113.2") {
		t.Error("two IPv4 clients share a key")
	}
	if !same("2001:db8:1:2::1", "2001:db8:1:2:ffff::9") {
		t.Error("two addresses in one IPv6 /64 are two keys")
	}
	if same("2001:db8:1:2::1", "2001:db8:1:3::1") {
		t.Error("two IPv6 /64s share a key")
	}
	if !same("::ffff:203.0.113.1", "203.0.113.1") {
		t.Error("an IPv4-mapped IPv6 address is not its IPv4 client's key")
	}
	if same("203.0.113.1", "::") {
		t.Error("an IPv4 client shares the unspecified IPv6 address's key")
	}
	t.Run("behind RealIP the forwarded address is the key", func(t *testing.T) {
		fwd := map[string]string{"X-Forwarded-For": "198.51.100.7"}
		if keyFor(t, "10.0.0.1", fwd, middleware.RealIP(1)) != keyFor(t, "198.51.100.7", nil) {
			t.Error("the key is not the forwarded client's")
		}
	})
}

func TestRateLimitWithTheMemoryStore(t *testing.T) {
	var ran bool
	app := limitedApp(middleware.RateLimitConfig{Limit: middleware.Limit{Rate: 1, Per: time.Hour}}, &ran)
	serve := func(addr string) *fasthttp.RequestCtx {
		fctx := newRequest("GET", "/x", nil)
		fctx.SetRemoteAddr(&net.TCPAddr{IP: net.ParseIP(addr), Port: 1})
		app.FasthttpHandler()(fctx)
		return fctx
	}
	if got := serve("203.0.113.1").Response.StatusCode(); got != 200 {
		t.Fatalf("first request %d, want 200", got)
	}
	second := serve("203.0.113.1")
	if second.Response.StatusCode() != 429 || hdr(second, "Retry-After") != "3600" {
		t.Errorf("second request %d with Retry-After %q, want 429 and 3600", second.Response.StatusCode(), hdr(second, "Retry-After"))
	}
	if got := serve("203.0.113.2").Response.StatusCode(); got != 200 {
		t.Errorf("another client got %d, want 200", got)
	}
}

func TestRateLimitPlacement(t *testing.T) {
	deny := &fakeStore{wait: time.Second}
	t.Run("a 429 carries the CORS headers", func(t *testing.T) {
		app := rice.New()
		app.Use(middleware.CORS(middleware.CORSConfig{Origins: []string{"https://app.example.com"}}),
			middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: deny}))
		app.GET("/x", func(c *rice.Ctx) error { return nil })
		fctx := newRequest("GET", "/x", map[string]string{"Origin": "https://app.example.com"})
		app.FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 429 || hdr(fctx, "Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Errorf("got %d with Allow-Origin %q", fctx.Response.StatusCode(), hdr(fctx, "Access-Control-Allow-Origin"))
		}
	})
	t.Run("with app.Use a miss is counted", func(t *testing.T) {
		store := &fakeStore{wait: time.Second}
		app := rice.New()
		app.Use(middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: store}))
		app.GET("/x", func(c *rice.Ctx) error { return nil })
		fctx := newRequest("GET", "/nowhere", nil)
		app.FasthttpHandler()(fctx)
		if fctx.Response.StatusCode() != 429 || store.calls != 1 {
			t.Errorf("a miss got %d after %d Take calls, want 429 after 1", fctx.Response.StatusCode(), store.calls)
		}
	})
	t.Run("on a group other routes are not limited", func(t *testing.T) {
		app := rice.New()
		api := app.Group("/api", middleware.RateLimit(middleware.RateLimitConfig{Limit: perMinute, Store: deny}))
		api.GET("/x", func(c *rice.Ctx) error { return nil })
		app.GET("/public", func(c *rice.Ctx) error { return c.String(200, "open") })
		fctx := newRequest("GET", "/public", nil)
		app.FasthttpHandler()(fctx)
		if got := fctx.Response.StatusCode(); got != 200 {
			t.Errorf("status %d, want 200", got)
		}
	})
}

func TestRateLimitPanicsOnALimitThatCannotWork(t *testing.T) {
	cases := map[string]middleware.Limit{
		"zero Rate":       {Per: time.Second},
		"zero Per":        {Rate: 1},
		"negative Burst":  {Rate: 1, Per: time.Second, Burst: -1},
		"sub-ns interval": {Rate: 2, Per: 1},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				msg, _ := recover().(string)
				if !strings.HasPrefix(msg, "rice: middleware.RateLimit:") {
					t.Errorf("panic %q", msg)
				}
			}()
			middleware.RateLimit(middleware.RateLimitConfig{Limit: l})
		})
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test -count=1 -run RateLimit ./middleware/`
Expected: FAIL to compile — `undefined: middleware.RateLimitConfig`, `undefined: middleware.RateLimit`.

- [ ] **Step 3: Replace `middleware/ratelimit.go` with the whole middleware**

```go
package middleware

import (
	"context"
	"math"
	"strconv"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// errTooManyRequests is the one error every rate-limited request returns.
var errTooManyRequests = &rice.HTTPError{Code: 429, Message: "Too Many Requests"}

// Limit allows Rate requests every Per, with bursts of up to Burst requests:
// Limit{Rate: 100, Per: time.Minute, Burst: 20} admits 20 at once, then one
// every 600ms. Burst 0 means Rate.
type Limit struct {
	Rate  int
	Per   time.Duration
	Burst int
}

// params returns the emission interval and the burst in nanoseconds and
// requests, or a message saying why the Limit cannot work.
func (l Limit) params() (interval, burst int64, msg string) {
	switch {
	case l.Rate <= 0:
		return 0, 0, "Rate must be positive"
	case l.Per <= 0:
		return 0, 0, "Per must be positive"
	case l.Burst < 0:
		return 0, 0, "Burst may not be negative"
	}
	interval = int64(l.Per) / int64(l.Rate)
	if interval == 0 {
		return 0, 0, "Per / Rate is below one nanosecond"
	}
	burst = int64(l.Burst)
	if burst == 0 {
		burst = int64(l.Rate)
	}
	if burst > math.MaxInt64/interval {
		return 0, 0, "Burst * Per / Rate overflows a time.Duration"
	}
	return interval, burst, ""
}

// RateLimitStore keeps the state RateLimit counts with. Take charges one
// request to key under GCRA with limit l and reports how long the caller must
// wait before it would be allowed; 0 means allowed now, and a request that is
// not allowed is not charged. The check and the charge are one atomic step:
// a store shared by several instances runs them as one operation on the
// shared state (a Lua script in Redis, say), never as a read and a later write.
//
// Keys are the caller's namespace: two RateLimit middleware sharing a store
// must not produce the same key for different purposes.
type RateLimitStore interface {
	Take(ctx context.Context, key string, l Limit) (wait time.Duration, err error)
}

// RateLimitConfig is what RateLimit needs. Limit is required.
//
// KeyFunc says whom a request counts against; nil means the client's address
// (see RateLimit). It may return any string, "" included, which is a key like
// any other. An error it returns goes to the error funnel. A limit per user
// reads the identity an auth middleware stored:
//
//	KeyFunc: func(c *rice.Ctx) (string, error) {
//		u, _ := userKey.Get(c)
//		return u.ID, nil
//	}
//
// Store holds the counts; nil means a new MemoryStore for this middleware
// alone. Two middleware sharing one Store must keep their keys apart, with a
// prefix, say.
type RateLimitConfig struct {
	Limit   Limit
	KeyFunc func(c *rice.Ctx) (string, error)
	Store   RateLimitStore
}

// RateLimit admits cfg.Limit's rate of requests per key and answers the rest
// with 429 Too Many Requests and a Retry-After of the whole seconds to wait,
// rounded up. It counts with GCRA: a key may send Burst requests at once, then
// one every Per/Rate, and a request that is refused does not count. An
// allowed request gets no header.
//
// The default key is c.ClientIP(): a whole IPv4 address, and an IPv6 address
// to its /64, since one IPv6 client is routinely given a whole /64. Install
// RealIP before it behind a proxy, or every request counts against the proxy.
//
// An error from KeyFunc or the Store goes to the error funnel, which answers
// 500: a store that is down is not a client over its limit. To serve rather
// than refuse while a shared store is down, wrap it:
//
//	type failOpen struct{ middleware.RateLimitStore }
//
//	func (s failOpen) Take(ctx context.Context, key string, l middleware.Limit) (time.Duration, error) {
//		wait, err := s.RateLimitStore.Take(ctx, key, l)
//		if err != nil {
//			log.Printf("rate limit store: %v", err)
//			return 0, nil
//		}
//		return wait, nil
//	}
//
// Install it after RealIP and inside CORS, Logger and otelrice, so a 429
// carries the CORS headers and is logged and traced. By address, before
// BasicAuth or KeyAuth, it throttles credential guessing; by user, after them
// with a KeyFunc, it is a quota per identity. With app.Use it also counts
// route misses (ADR-0012). See ADR-0022.
//
// A Limit with a Rate or Per that is not positive, a negative Burst, a
// Per/Rate below one nanosecond or a Burst*Per/Rate beyond a time.Duration
// panics.
func RateLimit(cfg RateLimitConfig) rice.Middleware {
	if _, _, msg := cfg.Limit.params(); msg != "" {
		panic("rice: middleware.RateLimit: " + msg)
	}
	keyFunc, store, limit := cfg.KeyFunc, cfg.Store, cfg.Limit
	if keyFunc == nil {
		keyFunc = clientKey
	}
	if store == nil {
		store = NewMemoryStore()
	}

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			key, err := keyFunc(c)
			if err != nil {
				return err
			}
			wait, err := store.Take(c.Context(), key, limit)
			if err != nil {
				return err
			}
			if wait > 0 {
				c.SetHeader("Retry-After", strconv.FormatInt(retryAfter(wait), 10))
				return errTooManyRequests
			}
			return next(c)
		}
	}
}

// retryAfter is wait in whole seconds, rounded up so that a client that waits
// as told is admitted, and at least 1.
func retryAfter(wait time.Duration) int64 {
	return max(int64((wait+time.Second-1)/time.Second), 1)
}

// clientKey is the default key: the client's IPv4 address, or its IPv6
// address's /64, as the 16 bytes of the IPv6 form. IPv4 and IPv4-mapped IPv6
// addresses are one key.
func clientKey(c *rice.Ctx) (string, error) {
	ip := c.ClientIP()
	var k [16]byte
	if v4 := ip.To4(); v4 != nil {
		k[10], k[11] = 0xff, 0xff
		copy(k[12:], v4)
	} else if len(ip) == 16 {
		copy(k[:8], ip[:8])
	}
	return string(k[:]), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race -count=1 -run 'RateLimit|MemoryStore' -v ./middleware/`
Expected: PASS — `TestRateLimitOutcomes` (5 subtests), `TestRateLimitErrors` (3), `TestRateLimitKeyFuncChoosesTheKey`, `TestRateLimitPassesTheRequestContext`, `TestRateLimitDefaultKey` (with its RealIP subtest), `TestRateLimitWithTheMemoryStore`, `TestRateLimitPlacement` (3), `TestRateLimitPanicsOnALimitThatCannotWork` (4), and Task 1's tests.

- [ ] **Step 5: Prove the default-key test bites**

`cp middleware/ratelimit.go /tmp/rl.go`; change `copy(k[:8], ip[:8])` to `copy(k[:], ip)`; run `go test -count=1 -run RateLimitDefaultKey ./middleware/`.
Expected: FAIL "two addresses in one IPv6 /64 are two keys". Then `cp /tmp/rl.go middleware/ratelimit.go` and `diff /tmp/rl.go middleware/ratelimit.go` — no output.

- [ ] **Step 6: Check coverage of the new files**

Run: `go test -count=1 -coverprofile=/tmp/rl.out ./middleware/ >/dev/null && go tool cover -func=/tmp/rl.out | grep -E "ratelimit|memorystore"`
Expected: every function at 100.0%.

- [ ] **Step 7: Run the suites and commit**

Run: `make test && make test-debug && make lint`
Expected: all pass.

```bash
git add middleware/ratelimit.go middleware/ratelimit_test.go
git commit -m "middleware: RateLimit answers 429 with Retry-After, keyed by address or KeyFunc

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Budgets

**Files:**
- Modify: `middleware/alloc_test.go` (append; it already imports `context` and `time`)

**Interfaces:**
- Consumes: `measure(t *testing.T, mw rice.Middleware, headers map[string]string) float64` (middleware/alloc_test.go); Tasks 1–2's API.
- Produces: `TestAllocBudgetRateLimitAllowed`, `TestAllocBudgetRateLimitDenied`, `TestAllocBudgetMemoryStoreTake`.

- [ ] **Step 1: Append the budgets**

Append to `middleware/alloc_test.go`:

```go

// TestAllocBudgetRateLimitAllowed pins RateLimit admitting a request with the
// default key and store, the key already in the store: the key string. The
// Limit admits a million at once, so every run of the measurement is allowed.
func TestAllocBudgetRateLimitAllowed(t *testing.T) {
	const want float64 = 1

	mw := middleware.RateLimit(middleware.RateLimitConfig{Limit: middleware.Limit{Rate: 1_000_000, Per: time.Second, Burst: 1_000_000}})
	if got := measure(t, mw, nil); got != want {
		t.Errorf("RateLimit allocated %.1f objects per allowed request, want exactly %.0f", got, want)
	}
}

// TestAllocBudgetRateLimitDenied pins a 429 with the default key and store:
// the key string; the 429 is a shared error, and Retry-After, below 100
// seconds here, is one of strconv's preformatted small numbers.
func TestAllocBudgetRateLimitDenied(t *testing.T) {
	const want float64 = 1

	mw := middleware.RateLimit(middleware.RateLimitConfig{Limit: middleware.Limit{Rate: 1, Per: time.Minute}})
	if got := measure(t, mw, nil); got != want {
		t.Errorf("RateLimit allocated %.1f objects per 429, want exactly %.0f", got, want)
	}
}

// TestAllocBudgetMemoryStoreTake pins Take on a key the store already holds:
// a map lookup and update under the shard's lock, no allocation.
func TestAllocBudgetMemoryStoreTake(t *testing.T) {
	const want float64 = 0

	s := middleware.NewMemoryStore()
	l := middleware.Limit{Rate: 1_000_000, Per: time.Second, Burst: 1_000_000}
	ctx := context.Background()
	_, _ = s.Take(ctx, "k", l)
	if got := testing.AllocsPerRun(1000, func() { _, _ = s.Take(ctx, "k", l) }); got != want {
		t.Errorf("MemoryStore.Take allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
```

- [ ] **Step 2: Run them in both modes, three times each**

Run: `go test -count=3 -run 'AllocBudget(RateLimit|MemoryStore)' ./middleware/ && go test -race -count=3 -run 'AllocBudget(RateLimit|MemoryStore)' ./middleware/`
Expected: `ok` twice. (Measured in the scratch copy at 1, 1 and 0 on darwin arm64, go1.25.6, both modes.)

- [ ] **Step 3: Prove a budget bites**

A budget measures code that already exists, so it cannot fail first. Change only `TestAllocBudgetMemoryStoreTake`'s `const want float64 = 0` to `1` — edit that one line by hand, not with a pattern that could match another budget — run `go test -count=1 -run AllocBudgetMemoryStoreTake ./middleware/`.
Expected: FAIL "MemoryStore.Take allocated 0.0 objects per call, want exactly 1". Restore it to `0`; `git diff --stat` shows only `middleware/alloc_test.go`, and `git diff | grep '^-' | grep -v '^---'` prints nothing (only additions).

- [ ] **Step 4: Run the suites and commit**

Run: `make test && make test-debug && make lint`
Expected: all pass.

```bash
git add middleware/alloc_test.go
git commit -m "middleware: pin RateLimit at 1 allocation allowed or refused, and Take at 0

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Documentation

**Files:**
- Create: `docs/adr/0022-rate-limiting-is-gcra-behind-a-store.md`
- Modify: `docs/adr/README.md`, `docs/adr/0021-auth-validates-through-a-callback.md`, `docs/03-core-concepts.md`, `docs/05-performance-model.md`, `docs/04-roadmap.md`, `docs/progress.md`, `middleware/doc.go`, `README.md`

**Interfaces:**
- Consumes: Tasks 1–3's API and the measured figures 1, 1, 0.
- Produces: documentation only.

- [ ] **Step 1: Write ADR-0022**

Create `docs/adr/0022-rate-limiting-is-gcra-behind-a-store.md`:

````markdown
# ADR-0022 — Rate limiting is GCRA behind a store that runs the algorithm

Status: Accepted
Date: 2026-09-27

## Context

BasicAuth and KeyAuth (ADR-0021) left guessing unthrottled, and a service needs a quota per client
anyway. Rate limiting is the first middleware that keeps state across requests. That state is per
process when the service runs alone and must be shared when it runs behind a load balancer, and rice's
core takes no dependency for the shared case. A middleware also has no hook into `App.Shutdown`, so a
store that needed a background goroutine would need the application to stop it.

## Decision

`middleware.RateLimit(RateLimitConfig{Limit, KeyFunc, Store})`, with
`Limit{Rate, Per, Burst}` and a `RateLimitStore` interface of one method,
`Take(ctx, key, limit) (wait time.Duration, err error)`.

- **GCRA.** A key keeps one value, its theoretical arrival time. A fresh key admits `Burst` requests
  at once (`Rate` when `Burst` is 0), then one every `Per/Rate`. A refused request is not charged, so
  hammering while limited does not lengthen the wait. No window boundary lets a client through at
  twice the rate.
- **The store runs the algorithm.** `Take` reads, decides and writes in one step: under a shard's lock
  in `MemoryStore`, in one script in a shared store. A store that only read and wrote the value would
  make every shared-store call a compare-and-set loop.
- **`MemoryStore` has no goroutine.** 64 shards, each a mutex and a map. A key whose time has passed
  is the same as an absent one, so each shard deletes those keys at most once a minute, during a
  `Take`. Times are kept relative to the store's creation on the monotonic clock.
- **The key.** By default the client address after RealIP: IPv4 whole, IPv6 to its /64, since one
  IPv6 client is routinely given a /64. A `KeyFunc` counts by anything else, such as the identity an
  auth middleware stored. `""` is a key, not an exemption; groups decide which routes are limited.
- **Three outcomes.** Allowed: the chain runs, no header. Refused: 429 through the funnel with a shared
  `*rice.HTTPError` and `Retry-After` in whole seconds rounded up, at least 1. An error from `KeyFunc`
  or the store: returned as it is, a 500 by default, because a store that is down is not a client over
  its limit. An application that prefers to serve while its store is down wraps the store.
- **Placement.** After RealIP; inside CORS, Logger and otelrice; by address before BasicAuth or KeyAuth
  to throttle guessing, by identity after them for a quota. With `app.Use` it counts misses (ADR-0012).

A `Limit` whose `Rate` or `Per` is not positive, whose `Burst` is negative, whose `Per/Rate` is below a
nanosecond or whose `Burst*Per/Rate` overflows a `time.Duration` panics at construction; `MemoryStore.Take`
returns an error for it.

## Alternatives

**Fixed window.** A counter per key per minute: the simplest, and a client can send twice the limit
across a boundary.

**Sliding window.** Two counters per key, smoother; still an approximation, and twice the state of GCRA.

**A store that reads and writes the value, the algorithm in the middleware.** A simpler interface, and
under contention a shared store would need a compare-and-set retry loop per request.

**A background sweeper with `Close`.** Exact expiry, and a lifecycle the middleware does not have: the
application would have to register the store's `Close` with `OnShutdown` or leak a goroutine per store.

**`RateLimit` or `X-RateLimit-*` headers.** The IETF headers are a draft whose syntax has changed; the
`X-` headers are not a standard. `Retry-After` is RFC 9110's and every retrying client reads it.

**Fail-open by default on a store error.** Keeps serving through an outage, and silently removes the
limit exactly when a shared store is under stress. The application chooses it by wrapping its store.

## Consequences

**Costs, pinned exactly with and without `-race`:** RateLimit allowing a request with the default key
and store, 1 allocation (the key string); a 429, 1 (the key; `Retry-After` below 100 seconds is one of
strconv's preformatted numbers); `MemoryStore.Take` on an existing key, 0.

**Each instance counts alone with `MemoryStore`.** Behind a load balancer of N instances a client gets
up to N times the limit until a shared store is installed. A Redis store is a separate module.

**Memory is bounded by expiry, not by a cap.** A flood from many addresses holds one entry per address
for `Burst*Per/Rate` plus up to a minute.
````

- [ ] **Step 2: Index it and answer ADR-0021's open end**

Append this row to the table in `docs/adr/README.md`, after the 0021 row:

```markdown
| [0022](0022-rate-limiting-is-gcra-behind-a-store.md) | Rate limiting is GCRA behind a store that runs the algorithm | Accepted |
```

In `docs/adr/0021-auth-validates-through-a-callback.md`, replace
`**Brute force is the next design.** Rate limiting is separate middleware with state of its own.`
with
`**Brute force is the next design.** Rate limiting is separate middleware with state of its own; it is ADR-0022.`

- [ ] **Step 3: `docs/03-core-concepts.md`**

In the "**What rice ships.**" paragraph, replace
``middleware.Logger`, `middleware.CORS`, `middleware.Timeout`, `middleware.BasicAuth` and
`middleware.KeyAuth`, in the opt-in `middleware` package.``
with
``middleware.Logger`, `middleware.CORS`, `middleware.Timeout`, `middleware.BasicAuth`,
`middleware.KeyAuth` and `middleware.RateLimit`, in the opt-in `middleware` package.``
and, after the sentence ending `([ADR-0021](adr/0021-auth-validates-through-a-callback.md)).`, insert:
`` `RateLimit` admits a `Limit` of requests per client address or per `KeyFunc` key with GCRA and answers the rest with 429 and `Retry-After`; its state lives behind a `RateLimitStore`, in memory by default ([ADR-0022](adr/0022-rate-limiting-is-gcra-behind-a-store.md)).``

- [ ] **Step 4: `docs/05-performance-model.md`**

After the table row that begins ``| `middleware.BasicAuth` and `middleware.KeyAuth`, a request without credentials (401)``, insert:

```markdown
| `middleware.RateLimit`, default key and store, an allowed request | 1, exactly, with and without `-race` | MEASURED after M8 | `TestAllocBudgetRateLimitAllowed` |
| `middleware.RateLimit`, default key and store, a 429 | 1, exactly, with and without `-race` | MEASURED after M8 | `TestAllocBudgetRateLimitDenied` |
| `MemoryStore.Take`, a key the store holds | 0, exactly, with and without `-race` | MEASURED after M8 | `TestAllocBudgetMemoryStoreTake` |
```

Before the line ``#### `otelrice` ``, insert:

```markdown
#### `middleware.RateLimit`

**1, 1 and 0.** Measured on darwin arm64 (go1.25.6), three runs each with and without `-race`, all
equal. The one allocation, allowed or refused, is the default key: the client address's 16 bytes as a
string, which the store keeps. A 429 adds nothing: the error is one shared `*rice.HTTPError`, and
`Retry-After` below 100 seconds is one of strconv's preformatted numbers (the refused fixture waits under
a minute). `MemoryStore.Take` on a key it holds is a hash, a lock and a map update. Inserting a new key
allocates its map entry and is not pinned; neither is a `KeyFunc` of the application's, which costs
what it costs.

```

- [ ] **Step 5: `docs/04-roadmap.md`**

In the BasicAuth/KeyAuth bullet under *Done after M8*, replace
`([ADR-0021](adr/0021-auth-validates-through-a-callback.md)). Rate limiting is next.`
with
`([ADR-0021](adr/0021-auth-validates-through-a-callback.md)).`
and append a new bullet after it:

```markdown
- `middleware.RateLimit`, the second security middleware and the first with state across requests.
  GCRA — a burst, then one request per interval, a refused request not charged — behind a
  `RateLimitStore` that runs the algorithm in one step, so a shared store later needs no retry loop.
  `MemoryStore` shards its keys and sweeps expired ones during `Take`, with no goroutine to stop. The
  default key is the client address, IPv6 to its /64; a `KeyFunc` counts by identity. A 429 carries
  `Retry-After`; a store error is a 500. 1 allocation allowed or refused, 0 for `Take`
  ([ADR-0022](adr/0022-rate-limiting-is-gcra-behind-a-store.md)).
```

- [ ] **Step 6: `docs/progress.md`**

Insert this entry directly after the header's closing `---` line, above the BasicAuth and KeyAuth entry:

```markdown
## 2026-09-27 — post-M8 — RateLimit

**Did:** `middleware.RateLimit` with `Limit{Rate, Per, Burst}`, a `KeyFunc` (default: the client
address, IPv4 whole and IPv6 to its /64) and a `RateLimitStore` whose one method runs GCRA atomically.
`MemoryStore` keeps a theoretical arrival time per key in 64 locked shards, relative to its creation on
the monotonic clock, and sweeps expired keys at most once a minute per shard inside `Take`. A refusal is
a shared 429 with `Retry-After` in whole seconds rounded up; a `KeyFunc` or store error goes to the
funnel. Tests: GCRA against a controlled clock, sweeping, 64 concurrent takes under `-race`, the
outcomes, the default key for IPv4, IPv6 and mapped addresses and behind RealIP, placement with CORS, a
group and a miss, the panics, and three budgets. ADR-0022.

A correction to the entry below: its **Measured** line was edited in place on the same day, from
BasicAuth 2 to 1, when the final review found the 2 held only for short credentials. This journal is
append-only; that edit should have been this note.

**Learned:** Two things.

1. *Handing the store the whole algorithm is what makes a shared store possible.* With GCRA split
   between a middleware and a store that only reads and writes, every request to a shared store is a
   compare-and-set loop; with `Take` owning read, decision and write, it is one script.
2. *Expiry makes a sweeper unnecessary.* A GCRA key whose time has passed is indistinguishable from an
   absent one, so deleting it is always safe and can happen whenever a request is already holding the
   lock — no goroutine, and nothing for a middleware without a lifecycle to stop.

**Measured:** `TestAllocBudgetRateLimitAllowed` 1, `TestAllocBudgetRateLimitDenied` 1,
`TestAllocBudgetMemoryStoreTake` 0 — darwin arm64 (go1.25.6), three runs each with and without `-race`,
all equal.

**Next:** tag `v0.3.0` with BasicAuth, KeyAuth and RateLimit. A Redis `RateLimitStore` is a separate
module and design, when a service runs on several instances.

---

```

- [ ] **Step 7: `middleware/doc.go`**

In "# What is here", after the sentence ending `puts the CORS headers on every other response.`, insert (as part of the same paragraph, rewrapped at 80 columns):

```go
// BasicAuth and KeyAuth check credentials with an application callback and
// store the identity it returns under a rice.Key. RateLimit admits a Limit of
// requests per client address, or per key of the application's choosing, and
// answers the rest with 429 and Retry-After.
```

In "# The recommended order", replace the line
`//		middleware.CORS(cfg),              // before auth, so a 401 carries the CORS headers`
with the two lines
```go
//		middleware.CORS(cfg),              // before auth, so a 401 carries the CORS headers
//		middleware.RateLimit(limitCfg),    // after RealIP and CORS: counts the client, and a 429 carries the CORS headers
```

In "# What they cost", replace
`// RequestID 6, Timeout 4, and CORS 0 on every branch.`
with
```go
// RequestID 6, Timeout 4, CORS 0 on every branch, BasicAuth and KeyAuth 1 when
// they accept and 0 for a request without credentials, and RateLimit 1 with
// its default key and store, allowed or refused.
```

- [ ] **Step 8: README**

Insert this section before `## Reading requests, writing JSON` (after the Authentication section):

````markdown
### Rate limiting

`RateLimit` admits a burst and then a steady rate per client, and answers the rest with `429 Too Many
Requests` and a `Retry-After`:

```go
app.Use(
	middleware.RealIP(1),
	middleware.CORS(corsCfg),
	middleware.RateLimit(middleware.RateLimitConfig{
		Limit: middleware.Limit{Rate: 100, Per: time.Minute, Burst: 20},
	}),
)
```

The default key is the client address — IPv4 whole, IPv6 to its /64 — so install `RealIP` before it
behind a proxy. For a quota per user, install it after `KeyAuth` or `BasicAuth` with a `KeyFunc` that
reads the identity:

```go
api := app.Group("/api",
	middleware.KeyAuth(keyCfg),
	middleware.RateLimit(middleware.RateLimitConfig{
		Limit: middleware.Limit{Rate: 1000, Per: time.Hour},
		KeyFunc: func(c *rice.Ctx) (string, error) {
			u, _ := userKey.Get(c)
			return u.ID, nil
		},
	}),
)
```

Counts live in a `MemoryStore` by default, per process: behind a load balancer each instance counts on
its own until a shared `RateLimitStore` is installed. A store error answers 500; to keep serving while a
shared store is down, wrap it and return `0, nil` on error. Allowed or refused, a request costs 1
allocation ([ADR-0022](docs/adr/0022-rate-limiting-is-gcra-behind-a-store.md)).
````

- [ ] **Step 9: Run the suites, check coverage, and commit**

Run: `make test && make test-debug && make lint && make cover`
Expected: all pass; the `total:` line of `make cover` at or above 99.6%.

```bash
git add docs README.md middleware/doc.go
git commit -m "docs: ADR-0022 and the docs for RateLimit

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

## After the plan

The final review, its fix pass, and then — on the owner's instruction, which was given with the
request ("làm middleware ratelimit đi nhé xong đánh tag 1 lượt") — merge to `main`, push, and tag
`v0.3.0` on the merge commit, push the tag. `otelrice` is not re-tagged.
