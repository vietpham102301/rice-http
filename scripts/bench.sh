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
  go test . ./bench/... -run '^$' -bench . -benchmem -count=10
} | tee "${out}"

echo "wrote ${out}"
