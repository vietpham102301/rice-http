#!/usr/bin/env bash
# Records the end-to-end comparison to bench/results/M8-compare-e2e.txt.
#
# Each framework × scenario runs in its own server process with GOMAXPROCS set
# to half the CPUs; the load generator gets the other half (GOMAXPROCS only, not
# CPU affinity). Frameworks are interleaved within each round, and the order
# rotates between rounds, so drift over the run hits all four alike. Server and
# client still share memory bandwidth and caches.
#
# usage: bench/compare/scripts/e2e.sh
#   env: ROUNDS (5), WARMUP (5s), DURATION (10s), CONNS (64),
#        OUT (../results/M8-compare-e2e.txt, relative to bench/compare)
# With the defaults this takes about 7 × 4 × 5 × 15s ≈ 35 minutes.
#
# On success, failure or interrupt (INT, TERM) the script stops every process
# it started. It fails if the server it started for a measurement is gone when
# the measurement ends: a server that could not bind its port would otherwise
# let the load generator measure whatever else answers there.
set -euo pipefail

cd "$(dirname "$0")/.."
out="${OUT:-../results/M8-compare-e2e.txt}"
rounds="${ROUNDS:-5}"
warmup="${WARMUP:-5s}"
duration="${DURATION:-10s}"
conns="${CONNS:-64}"

# pid and lpid hold the running server and load generator; each is empty when
# that process is not running.
pid=
lpid=
bin="$(mktemp -d)"

# stop kills $1 and reaps it, sending SIGKILL if it outlives SIGTERM by 5s.
stop() {
  [ -n "$1" ] || return 0
  kill "$1" 2>/dev/null || true
  local i
  for (( i = 0; i < 50; i++ )); do
    kill -0 "$1" 2>/dev/null || break
    sleep 0.1
  done
  kill -KILL "$1" 2>/dev/null || true
  wait "$1" 2>/dev/null || true
}
cleanup() {
  stop "${lpid}"
  stop "${pid}"
  rm -rf "${bin}"
}
trap cleanup EXIT
trap 'echo "e2e: interrupted" >&2; exit 130' INT
trap 'echo "e2e: terminated" >&2; exit 143' TERM

go build -o "${bin}/server" ./cmd/server
go build -o "${bin}/loadgen" ./cmd/loadgen
go build -o "${bin}/summarize" ./cmd/summarize

ncpu="$(getconf _NPROCESSORS_ONLN)"
half=$(( ncpu / 2 ))
[ "${half}" -ge 1 ] || half=1

# The header is taken before the run, so its date and power are the start's.
{
  ./scripts/header.sh M8-compare-e2e
  echo "# cpus: ${ncpu} (server GOMAXPROCS=${half}, loadgen GOMAXPROCS=${half})"
  echo "# load: ${conns} keep-alive connections, closed loop, warmup ${warmup}, measured ${duration}, ${rounds} rounds"
  echo "# shared machine: server and load generator share memory bandwidth and caches; splitting CPUs removes scheduler contention, not that"
} > "${bin}/header.txt"

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
      # The load generator runs in the background so an INT or TERM trap runs
      # at once instead of after the measurement.
      GOMAXPROCS="${half}" "${bin}/loadgen" -addr "${addr}" -scenario "${scenario}" -framework "${fw}" \
        -round "${round}" -conns "${conns}" -warmup "${warmup}" -duration "${duration}" >> "${raw}" &
      lpid=$!
      if ! wait "${lpid}"; then
        lpid=
        echo "e2e: ${fw} ${scenario} round ${round} failed" >&2
        exit 1
      fi
      lpid=
      if ! kill -0 "${pid}" 2>/dev/null; then
        echo "e2e: ${fw} ${scenario} round ${round}: the server started on ${addr} exited before the measurement ended; the numbers are not its own" >&2
        exit 1
      fi
      stop "${pid}"
      pid=
      echo "e2e: round ${round} ${scenario} ${fw} done" >&2
    done
  done
done

{
  cat "${bin}/header.txt"
  echo
  "${bin}/summarize" < "${raw}"
  echo "## raw"
  echo
  echo '```'
  cat "${raw}"
  echo '```'
} | tee "${out}"

echo "wrote ${out}" >&2
