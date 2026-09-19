# Benchmark Results

One file per milestone, produced by `make bench-record LABEL=<name>`.

These files are committed on purpose. A performance regression should appear as
a diff in a pull request, not as someone's recollection of what the number used
to be.

Each file is stamped with the date, Go version, OS and CPU it was produced on.
Numbers from different stamps are not comparable. When comparing two milestones,
re-run the older label on the current machine rather than trusting a stored
number from different hardware.

## The framework comparison (M8)

Two files come from `make compare-record` rather than `make bench-record`, and are produced by
the separate module in `bench/compare/`:

- `M8-compare-handler.txt` — `BenchmarkHandler/<scenario>/<framework>` for rice, Gin, Echo and
  Fiber over seven scenarios, ten runs each, with each framework's in-process entry point on an
  already-parsed request. Read it with `benchstat bench/results/M8-compare-handler.txt`.
- `M8-compare-e2e.txt` — the same scenarios end to end: each framework's own server in one
  process, the load generator in another, `GOMAXPROCS` set to half the CPUs each, five rounds
  with the frameworks interleaved. Median requests per second, p50 and p99 per framework and
  scenario, with the min–max across rounds, then every raw line.

Both headers add the module versions compared and a `# power:` line on macOS; the end-to-end
header adds the CPU split and load settings. The committed `M8-compare-e2e.txt` was written by an
earlier `e2e.sh` whose header was taken after the run, so its `# date:` and `# power:` are the
run's end; the script now takes them at the start and adds a line saying server and load
generator share memory bandwidth and caches, which the committed file lacks. `make
compare-record` takes about 40 minutes and needs nothing but Go; `make compare` runs the
equivalence gate and a short smoke run in a few seconds, and is what CI runs. What each file can
and cannot compare is in
[`docs/05-performance-model.md`](../../docs/05-performance-model.md#where-rice-stands) — read
that before quoting a number from either.

## Comparing runs

Compare two runs with:

    benchstat bench/results/M0-fasthttp-baseline.txt bench/results/M1-minimal-server.txt

Install benchstat with:

    go install golang.org/x/perf/cmd/benchstat@latest
