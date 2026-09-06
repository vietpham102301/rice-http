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
