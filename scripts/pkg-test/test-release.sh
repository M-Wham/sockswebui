#!/usr/bin/env bash
# Suite release: scripts/release-check.sh must catch missing or altered release assets.
set -u
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_release
VER="$(pkg_version)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ASSETS="install.sh sockswebui-linux-amd64 sockswebui-linux-arm64 sockswebui-linux-armv7 sockswebui-linux-mips sockswebui-linux-mipsle sockswebui.init sockswebui.service sockswebui_${VER#v}_amd64.deb sockswebui_${VER#v}_arm64.deb sockswebui_${VER#v}_armhf.deb"

# fresh <name>: copy dist/ to a new dir and print its path.
fresh() { mkdir -p "$tmp/$1" && cp -a "$PKG_DIST/." "$tmp/$1/" && echo "$tmp/$1"; }

# run_check <dir>: run release-check on a dist copy; output in $tmp/out, status in $?.
run_check() { (cd "$PKG_ROOT" && DIST_DIR="$1" bash scripts/release-check.sh "$VER") >"$tmp/out" 2>&1; }

# expect_fail <name> <dir> <file>: exit 1 and a FAIL line naming <file>.
expect_fail() {
  run_check "$2"
  local rc=$?
  if [ "$rc" -eq 1 ] && grep -q "^release-check: FAIL: .*$3" "$tmp/out"; then pass "$1"
  else fail "$1" "rc=$rc, no FAIL line naming $3: $(tail -n 2 "$tmp/out" | tr '\n' ' ')"; fi
}

# intact
d="$(fresh intact)"
run_check "$d"; rc=$?
if [ "$rc" -eq 0 ] && [ "$(tail -n 1 "$tmp/out")" = "release-check: ok" ]; then pass intact
else fail intact "rc=$rc: $(tail -n 2 "$tmp/out" | tr '\n' ' ')"; fi

# SHA256SUMS names exactly the 11 assets
want="$(printf '%s\n' $ASSETS | LC_ALL=C sort | tr '\n' ' ')"
eq sums-11-files "$want" "$(cut -c67- "$PKG_DIST/SHA256SUMS" | LC_ALL=C sort | tr '\n' ' ')"

d="$(fresh no-armhf)"; rm "$d/sockswebui_${VER#v}_armhf.deb"
expect_fail missing-deb "$d" "sockswebui_${VER#v}_armhf.deb"

d="$(fresh no-install)"; rm "$d/install.sh"
expect_fail missing-install.sh "$d" "install.sh"

d="$(fresh no-init-sum)"; grep -v ' sockswebui.init$' "$PKG_DIST/SHA256SUMS" >"$d/SHA256SUMS"
expect_fail init-not-in-sums "$d" "sockswebui.init"

d="$(fresh reorder)"
deb="sockswebui_${VER#v}_amd64.deb"
mkdir "$tmp/ar" && (cd "$tmp/ar" && ar x "$d/$deb" && rm "$d/$deb" && ar rc "$d/$deb" control.tar.gz debian-binary data.tar.gz)
(cd "$d" && sha256sum $ASSETS >SHA256SUMS)
expect_fail deb-ar-order "$d" "$deb"

pkg_finish release
