#!/usr/bin/env bash
# Records the handler-level comparison to bench/results/M8-compare-handler.txt.
# usage: bench/compare/scripts/handler.sh
set -euo pipefail

cd "$(dirname "$0")/.."
out=../results/M8-compare-handler.txt

{
  ./scripts/header.sh M8-compare-handler
  echo
  go test . -run '^$' -bench '^BenchmarkHandler$' -benchmem -count=10 -timeout 30m
} | tee "${out}"

echo "wrote bench/results/M8-compare-handler.txt"
