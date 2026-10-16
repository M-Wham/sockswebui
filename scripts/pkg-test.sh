#!/usr/bin/env bash
# Usage: bash scripts/pkg-test.sh <deb|install|release|docker|all>
# Container tests. They pull images, so they stay out of gates.sh.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
suite="${1:-}"
case "$suite" in
  deb | install | release | docker) suites="$suite" ;;
  all) suites="deb install release docker" ;;
  *) echo "usage: pkg-test.sh <deb|install|release|docker|all>" >&2; exit 2 ;;
esac
bad=0
for s in $suites; do
  script="$here/pkg-test/test-$s.sh"
  if [ ! -f "$script" ]; then
    echo "FAIL: $s: missing $script"
    echo "pkg-test $s: FAIL (1)"
    bad=1
    continue
  fi
  bash "$script" || bad=1
done
exit "$bad"
