#!/usr/bin/env bash
# Shared helpers for scripts/pkg-test/*.sh. Source me; do not run me.
# Image refs are fully qualified and set once here.
IMG_DEBIAN="docker.io/library/debian:13"
IMG_CURL="docker.io/library/buildpack-deps:trixie-curl"
IMG_SHELLCHECK="docker.io/koalaman/shellcheck:stable"
IMG_ARCH="docker.io/library/archlinux:base"
IMG_OPENWRT="docker.io/openwrt/rootfs:x86-64-24.10.4"

PKG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PKG_DIST="$PKG_ROOT/dist"
PKG_FAILS=0

if [ -z "${PKG_ENGINE:-}" ]; then
  if docker info >/dev/null 2>&1; then PKG_ENGINE=docker; else PKG_ENGINE=podman; fi
fi

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1: $2"; PKG_FAILS=$((PKG_FAILS + 1)); }

# check <name> <reason> <cmd...>: PASS if the command succeeds.
check() {
  local name="$1" reason="$2"
  shift 2
  if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name" "$reason"; fi
}

# eq <name> <want> <got>
eq() {
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want [$2] got [$3]"; fi
}

need_release() {
  [ -f "$PKG_DIST/sockswebui-linux-amd64" ] || { echo "run make release first" >&2; exit 1; }
}

# pkg_version prints the version the release binary reports, e.g. v0.9.0.
pkg_version() { "$PKG_DIST/sockswebui-linux-amd64" --version | awk '{print $2}'; }

# in_image <image> <sh -c script>: run a script with dist mounted read-only at /dist.
in_image() {
  local img="$1"
  shift
  "$PKG_ENGINE" run --rm --network host -v "$PKG_DIST:/dist:ro" "$img" sh -c "$@"
}

# pkg_finish <suite>: print the summary line and exit.
pkg_finish() {
  if [ "$PKG_FAILS" -eq 0 ]; then echo "pkg-test $1: ok"; exit 0; fi
  echo "pkg-test $1: FAIL ($PKG_FAILS)"
  exit 1
}
