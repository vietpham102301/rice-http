#!/usr/bin/env bash
# Records the end-to-end comparison to bench/results/M8-compare-e2e.txt.
#
# Each framework × scenario runs in its own server process with half the CPUs;
# the load generator gets the other half. Frameworks are interleaved within each
# round, and the order rotates between rounds, so drift over the run hits all
# four alike. Server and client still share memory bandwidth and caches.
#
# usage: bench/compare/scripts/e2e.sh
#   env: ROUNDS (5), WARMUP (5s), DURATION (10s), CONNS (64)
# With the defaults this takes about 7 × 4 × 5 × 15s ≈ 35 minutes.
set -euo pipefail

cd "$(dirname "$0")/.."
out=../results/M8-compare-e2e.txt
rounds="${ROUNDS:-5}"
warmup="${WARMUP:-5s}"
duration="${DURATION:-10s}"
conns="${CONNS:-64}"

bin="$(mktemp -d)"
trap 'rm -rf "${bin}"' EXIT
go build -o "${bin}/server" ./cmd/server
go build -o "${bin}/loadgen" ./cmd/loadgen
go build -o "${bin}/summarize" ./cmd/summarize

ncpu="$(getconf _NPROCESSORS_ONLN)"
half=$(( ncpu / 2 ))
[ "${half}" -ge 1 ] || half=1

frameworks=(rice gin echo fiber)
raw="${bin}/raw.txt"
: > "${raw}"
port=18000

for (( round = 1; round <= rounds; round++ )); do
  for scenario in $("${bin}/loadgen" -list); do
    for (( k = 0; k < ${#frameworks[@]}; k++ )); do
      fw="${frameworks[$(( (k + round - 1) % ${#frameworks[@]} ))]}"
      port=$(( port + 1 ))
      addr="127.0.0.1:${port}"
      GOMAXPROCS="${half}" "${bin}/server" -framework "${fw}" -scenario "${scenario}" -addr "${addr}" &
      pid=$!
      if ! GOMAXPROCS="${half}" "${bin}/loadgen" -addr "${addr}" -scenario "${scenario}" -framework "${fw}" \
          -round "${round}" -conns "${conns}" -warmup "${warmup}" -duration "${duration}" >> "${raw}"; then
        kill "${pid}" 2>/dev/null || true
        wait "${pid}" 2>/dev/null || true
        echo "e2e: ${fw} ${scenario} round ${round} failed" >&2
        exit 1
      fi
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
      echo "e2e: round ${round} ${scenario} ${fw} done" >&2
    done
  done
done

{
  ./scripts/header.sh M8-compare-e2e
  echo "# cpus: ${ncpu} (server GOMAXPROCS=${half}, loadgen GOMAXPROCS=${half})"
  echo "# load: ${conns} keep-alive connections, closed loop, warmup ${warmup}, measured ${duration}, ${rounds} rounds"
  echo
  "${bin}/summarize" < "${raw}"
  echo "## raw"
  echo
  echo '```'
  cat "${raw}"
  echo '```'
} | tee "${out}"

echo "wrote bench/results/M8-compare-e2e.txt" >&2
