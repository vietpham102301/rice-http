#!/usr/bin/env bash
# Prints the header of a comparison results file: the same fields as
# scripts/bench.sh at the repo root, plus every compared module's version.
# usage: scripts/header.sh <label>   (run from anywhere)
set -euo pipefail

label="${1:?usage: scripts/header.sh <label>}"
cd "$(dirname "$0")/.."

echo "# rice benchmark results: ${label}"
echo "# date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "# go:   $(go version)"
echo "# os:   $(uname -srm)"
if [ "$(uname -s)" = "Darwin" ]; then
  echo "# cpu:  $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
else
  echo "# cpu:  $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ *//' || echo unknown)"
fi
if [ "$(uname -s)" = "Darwin" ]; then
  echo "# power: $(pmset -g batt 2>/dev/null | head -1 | sed "s/^Now drawing from //" || echo unknown)"
fi
echo "# rice: $(git -C ../.. rev-parse --short HEAD) (local, via replace)"
for m in github.com/gin-gonic/gin github.com/labstack/echo/v5 github.com/gofiber/fiber/v3 github.com/valyala/fasthttp; do
  echo "# $(go list -m "$m")"
done
