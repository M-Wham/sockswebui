#!/bin/sh
# Usage: build-deb.sh <VERSION> <distdir>
#        build-deb.sh --deb-version <VERSION>
# Builds one .deb per architecture from the binaries in <distdir>.
# Uses only tar, gzip, ar, md5sum, sed and wc. The output is reproducible.
set -eu

deb_version() {
  if printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    printf '%s\n' "${1#v}"
  elif printf '%s\n' "$1" | grep -Eq '^[A-Za-z0-9.+~]+$'; then
    printf '0.0.0~git%s\n' "$1"
  else
    echo "build-deb: cannot map VERSION '$1' to a Debian version" >&2
    return 1
  fi
}

if [ "${1:-}" = "--deb-version" ]; then
  [ $# -eq 2 ] || { echo "usage: build-deb.sh --deb-version <VERSION>" >&2; exit 2; }
  deb_version "$2"
  exit $?
fi

[ $# -eq 2 ] || { echo "usage: build-deb.sh <VERSION> <distdir>" >&2; exit 2; }
version="$1"
dist="$(cd "$2" && pwd)"
here="$(cd "$(dirname "$0")" && pwd)"
debver="$(deb_version "$version")" || exit 1
epoch="${SOURCE_DATE_EPOCH:-$(git -C "$here" log -1 --format=%ct)}"
umask 022
# Drop debs from earlier builds so SHA256SUMS and the release hold only this version.
rm -f "$dist"/sockswebui_*.deb

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

pack() { # pack <dir> <out.tar.gz>
  tar --format=gnu --sort=name --owner=root --group=root \
    --mtime="@$epoch" -C "$1" -cf - . | gzip -n >"$2"
}

build_one() { # build_one <debarch> <binary name>
  arch="$1"
  bin="$dist/$2"
  [ -f "$bin" ] || { echo "build-deb: missing $bin" >&2; return 1; }
  [ -f "$dist/sockswebui.service" ] || { echo "build-deb: missing $dist/sockswebui.service" >&2; return 1; }
  d="$work/$arch"
  mkdir -p "$d/data/usr/bin" "$d/data/usr/lib/systemd/system" "$d/control"
  cp "$bin" "$d/data/usr/bin/sockswebui"
  chmod 0755 "$d/data/usr/bin/sockswebui"
  sed 's#^ExecStart=/usr/local/bin/sockswebui #ExecStart=/usr/bin/sockswebui #' \
    "$dist/sockswebui.service" >"$d/data/usr/lib/systemd/system/sockswebui.service"
  chmod 0644 "$d/data/usr/lib/systemd/system/sockswebui.service"
  find "$d/data" -type d -exec chmod 0755 {} +

  bytes=$(( $(wc -c <"$d/data/usr/bin/sockswebui") + $(wc -c <"$d/data/usr/lib/systemd/system/sockswebui.service") ))
  size=$(( (bytes + 1023) / 1024 ))
  sed -e "s#@DEBVER@#$debver#" -e "s#@ARCH@#$arch#" -e "s#@SIZE@#$size#" \
    "$here/deb/control.in" >"$d/control/control"
  (cd "$d/data" && find usr -type f | LC_ALL=C sort | xargs md5sum) >"$d/control/md5sums"
  chmod 0644 "$d/control/control" "$d/control/md5sums"
  for s in postinst prerm postrm; do
    if [ -f "$here/deb/$s" ]; then
      cp "$here/deb/$s" "$d/control/$s"
      chmod 0755 "$d/control/$s"
    fi
  done

  pack "$d/control" "$d/control.tar.gz"
  pack "$d/data" "$d/data.tar.gz"
  printf '2.0\n' >"$d/debian-binary"
  out="$dist/sockswebui_${debver}_${arch}.deb"
  rm -f "$out"
  (cd "$d" && ar rcD "$out" debian-binary control.tar.gz data.tar.gz)
  echo "build-deb: wrote $out"
}

build_one amd64 sockswebui-linux-amd64
build_one arm64 sockswebui-linux-arm64
build_one armhf sockswebui-linux-armv7
