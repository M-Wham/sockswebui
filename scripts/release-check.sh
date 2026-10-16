#!/usr/bin/env bash
# Checks the `make release` output in dist/.
# Each of the five linux binaries must exist, be statically linked, carry no
# -tags testrig code (no "dev-testrig" string). The three .deb files for $VERSION
# must exist, with ar members in the order debian-binary, control.tar.gz,
# data.tar.gz. install.sh must be executable. SHA256SUMS must name exactly the
# 11 release files (5 binaries, 3 debs, install.sh, sockswebui.service,
# sockswebui.init) and must verify. The amd64 binary's --version must equal
# "sockswebui $VERSION".
# DIST_DIR (default dist) selects the directory to check.
# VERSION comes from the environment or first argument; the default matches the
# Makefile (git rev-parse --short HEAD). Run after `make release`.
# Exit 0 ok, 1 on any failure.
set -u
cd "$(dirname "$0")/.." || exit 1
DIST="${DIST_DIR:-dist}"

VERSION="${1:-${VERSION:-$(git rev-parse --short HEAD 2>/dev/null || echo dev)}}"

fail=0
bad() { echo "release-check: FAIL: $*" >&2; fail=1; }

check_bin() { # $1 file, $2 pattern expected in `file` output
  local f="$DIST/$1"
  if [ ! -f "$f" ]; then bad "$f missing (run make release)"; return; fi
  local info
  info="$(file "$f")"
  echo "$info"
  case "$info" in *"statically linked"*) ;; *) bad "$f is not statically linked" ;; esac
  case "$info" in *"$2"*) ;; *) bad "$f is not $2" ;; esac
  if grep -q dev-testrig "$f"; then bad "$f contains testrig code (dev-testrig)"; fi
}

check_bin sockswebui-linux-amd64 "x86-64"
check_bin sockswebui-linux-arm64 "ARM aarch64"
check_bin sockswebui-linux-armv7 "ARM, EABI5"
check_bin sockswebui-linux-mipsle "LSB executable, MIPS"
check_bin sockswebui-linux-mips "MSB executable, MIPS"

dver="$(sh packaging/build-deb.sh --deb-version "$VERSION")" || { bad "no deb version for $VERSION"; dver=; }
files="sockswebui-linux-amd64 sockswebui-linux-arm64 sockswebui-linux-armv7 sockswebui-linux-mipsle sockswebui-linux-mips"
files="$files install.sh sockswebui.service sockswebui.init"
if [ -n "$dver" ]; then
  for a in amd64 arm64 armhf; do
    deb="sockswebui_${dver}_$a.deb"
    files="$files $deb"
    if [ ! -f "$DIST/$deb" ]; then bad "$DIST/$deb missing (run make release)"; continue; fi
    order="$(ar t "$DIST/$deb" | tr '\n' ' ')"
    if [ "$order" != "debian-binary control.tar.gz data.tar.gz " ]; then
      bad "$DIST/$deb has wrong ar member order: $order"
    fi
  done
fi
if [ ! -x "$DIST/install.sh" ]; then bad "$DIST/install.sh missing or not executable"; fi
for f in sockswebui.service sockswebui.init; do
  if [ ! -f "$DIST/$f" ]; then bad "$DIST/$f missing"; fi
done

if [ ! -f "$DIST/SHA256SUMS" ]; then
  bad "$DIST/SHA256SUMS missing"
else
  for f in $files; do
    grep -q "^[0-9a-f]\{64\}  $f\$" "$DIST/SHA256SUMS" || bad "$f not listed in SHA256SUMS"
  done
  n="$(grep -c . "$DIST/SHA256SUMS")"
  [ "$n" -eq "$(echo "$files" | wc -w)" ] || bad "SHA256SUMS has $n lines, want $(echo "$files" | wc -w)"
  (cd "$DIST" && sha256sum -c SHA256SUMS) || bad "sha256sum -c SHA256SUMS failed"
fi

if [ -x "$DIST/sockswebui-linux-amd64" ]; then
  want="sockswebui $VERSION"
  got="$("$DIST/sockswebui-linux-amd64" --version 2>&1)"
  if [ "$got" != "$want" ]; then
    bad "amd64 --version = '$got', want '$want'"
  else
    echo "$got"
  fi
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "release-check: ok"
