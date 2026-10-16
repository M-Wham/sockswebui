#!/usr/bin/env bash
# Peak RSS (VmHWM) must stay < 64 MiB with 4 idle slots.
# Builds the -tags testrig binary, runs it with 4 dev-rig slots on loopback only,
# waits for all slots UP, idles 10 s, then reads VmRSS/VmHWM from /proc.
# Exit: 0 ok, 1 over budget, 2 slots never reached UP, 3 harness error.
#
# --load: peak memory under load instead. Runs the memload test
# (cmd/sockswebui/memload*_test.go, -tags memload): the real entrypoint with 4 WireGuard
# slots against a rig in a child process, then phases boot, login (argon2), load
# (4 slots x MEMLOAD_CONNS x MEMLOAD_MB through the relays), mixed (load + logins) and
# idle-after. VmHWM is reset per phase. The process is pinned to one CPU with GOMAXPROCS=1
# (like a 1-core CT); the rig runs on the other CPUs. Heap profiles at each phase's peak
# and summary.txt go to MEMLOAD_OUT (default bin/rss-load, git-ignored).
# Budgets: boot and login must stay < 65536 kB; load and mixed are
# checked only when LOAD_LIMIT_KB is set. Other knobs: see the test file header.
set -u
cd "$(dirname "$0")/.." || exit 3

LIMIT_KB=65536

if [ "${1:-}" = "--load" ]; then
  out="${MEMLOAD_OUT:-bin/rss-load}"
  mkdir -p "$out" || exit 3
  bin="$out/memload.test"
  CGO_ENABLED=0 go test -tags memload -c -o "$bin" ./cmd/sockswebui >&2 || exit 3
  pin=()
  if command -v taskset >/dev/null && [ "$(nproc)" -gt 1 ]; then
    pin=(taskset -c 0)
    export MEMLOAD_RIG_CPUS="${MEMLOAD_RIG_CPUS:-1-$(($(nproc) - 1))}"
  fi
  log="$out/memload.log"
  MEMLOAD_OUT="$(cd "$out" && pwd)" GOMAXPROCS=1 "${pin[@]}" "$bin" \
    -test.run '^TestMemLoad$' -test.timeout 10m >"$log" 2>&1
  rc=$?
  grep '^MEMLOAD' "$log" | sed 's/ | phase-max.*//'
  if [ "$rc" -ne 0 ]; then
    grep -v '^MEMLOAD' "$log" | tail -20 >&2
    exit 3
  fi
  status=0
  while read -r phase hwm; do
    limit=""
    case "$phase" in
      boot|login) limit=$LIMIT_KB ;;
      load|mixed) limit="${LOAD_LIMIT_KB:-}" ;;
    esac
    if [ -n "$limit" ] && [ "$hwm" -ge "$limit" ]; then
      echo "OVER BUDGET phase=$phase vmhwm_kb=$hwm limit_kb=$limit" >&2
      status=1
    fi
  done < <(sed -n 's/^MEMLOAD phase=\([^ ]*\) .* vmhwm_kb=\([0-9]*\) .*/\1 \2/p' "$log")
  peak="$(sed -n 's/^MEMLOAD phase=.* vmhwm_kb=\([0-9]*\) .*/\1/p' "$log" | sort -n | tail -1)"
  echo "load peak_hwm_kb=$peak (profiles: $out)"
  exit "$status"
fi

PASSWORD="rss-check-password-1"
tmp="$(mktemp -d)"
pid=""
cleanup() {
  if [ -n "$pid" ]; then kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; fi
  rm -rf "$tmp"
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags testrig -o "$tmp/sockswebui" ./cmd/sockswebui >&2 || exit 3

port="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')" || exit 3
base="http://127.0.0.1:$port"
"$tmp/sockswebui" --dev-testrig --dev-testrig-slots 4 --data-dir "$tmp/data" \
  --ui-addr "127.0.0.1:$port" >"$tmp/log" 2>&1 &
pid=$!

for _ in $(seq 1 100); do
  curl -s -o /dev/null "$base/" && break
  kill -0 "$pid" 2>/dev/null || { cat "$tmp/log" >&2; exit 3; }
  sleep 0.1
done

jar="$tmp/jar"
body="{\"password\":\"$PASSWORD\"}"
curl -sf -X POST -H 'Content-Type: application/json' -d "$body" "$base/api/setup" >/dev/null || { echo "setup failed" >&2; exit 3; }
curl -sf -c "$jar" -X POST -H 'Content-Type: application/json' -d "$body" "$base/api/login" >/dev/null || { echo "login failed" >&2; exit 3; }

# GET /api/slots carries config only; live state is in the first SSE frame of /api/events.
snapshot() {
  curl -s -b "$jar" --max-time 2 "$base/api/events" 2>/dev/null \
    | sed -n '/^event: snapshot/{n;s/^data: //p;q}'
}
up=0
snap=""
for _ in $(seq 1 60); do
  snap="$(snapshot)"
  n="$(printf '%s' "$snap" | grep -o '"state":"UP"' | wc -l)"
  if [ "$n" -ge 4 ]; then up=1; break; fi
  sleep 1
done
if [ "$up" -ne 1 ]; then
  echo "slots never reached UP; snapshot: $snap" >&2
  curl -s -b "$jar" "$base/api/slots" >&2
  exit 2
fi

sleep 10
rss="$(awk '/^VmRSS:/{print $2}' "/proc/$pid/status")"
hwm="$(awk '/^VmHWM:/{print $2}' "/proc/$pid/status")"
echo "ps rss=$(ps -o rss= -p "$pid" | tr -d ' ') kB" >&2
echo "slots=4 UP rss_kb=$rss hwm_kb=$hwm"
[ "$hwm" -lt "$LIMIT_KB" ] || exit 1
exit 0
