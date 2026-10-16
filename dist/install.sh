#!/bin/sh
# sockswebui install script. Downloads a release, checks every file against
# SHA256SUMS, then installs it. Run as root.
# Usage: install.sh [--version vX.Y.Z] | --uninstall
#   dpkg hosts:    installs the matching .deb.
#   systemd hosts: /usr/local/bin/sockswebui, user sockswebui, /etc/systemd/system/sockswebui.service.
#   OpenWrt:       /usr/bin/sockswebui and the procd script /etc/init.d/sockswebui.
#   --uninstall:   removes what was installed; keeps the data dir and the user.
# Env:   SOCKSWEBUI_BASE_URL  test hook: fetch release files from this http(s)
#                             URL instead of GitHub.
#
# Everything lives in main(), called on the last line. A copy cut short in a
# "curl | sh" pipe is then a syntax error and runs nothing. A cut between the
# closing brace and the "main" line only defines main, runs nothing and exits 0.
main() {
  set -eu

  RELEASES="https://github.com/M-Wham/sockswebui/releases"

  usage() {
    echo "usage: install.sh [--version vX.Y.Z] | --uninstall" >&2
  }

  die() {
    echo "install.sh: $*" >&2
    exit 1
  }

  version=""
  have_version=0
  uninstall=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --version)
        [ $# -ge 2 ] || { usage; exit 2; }
        version="$2"
        have_version=1
        shift 2
        ;;
      --uninstall)
        uninstall=1
        shift
        ;;
      -h | --help)
        usage
        exit 0
        ;;
      *)
        usage
        exit 2
        ;;
    esac
  done

  if [ "$have_version" = 1 ]; then
    case "$version" in
      v[0-9]*) ;;
      *) die "bad version '$version' (want vX.Y.Z)" ;;
    esac
    case "$version" in
      *[!A-Za-z0-9.+~-]* | *..*) die "bad version '$version' (want vX.Y.Z)" ;;
    esac
  fi

  [ "$(id -u)" = 0 ] || die "must run as root"

  systemd_running() { [ -d /run/systemd/system ]; }

  # url_line: tell the owner to finish setup at once (open setup).
  url_line() {
    ip="<device-ip>"
    for w in $(hostname -I 2>/dev/null || true); do
      case "$w" in
        *:*) ;;
        *) ip="$w"; break ;;
      esac
    done
    echo "sockswebui: open http://$ip:8787/ and set the admin password now. Until it is set, anyone who can reach that port can set it."
  }

  # datadir_of <file> <default>: the --data-dir the owner's unit or init script uses.
  datadir_of() {
    d="$(sed -n 's/.*--data-dir[ =]\([^ "]*\).*/\1/p' "$1" 2>/dev/null | head -n 1)"
    echo "${d:-$2}"
  }

  if [ "$uninstall" = 1 ]; then
    if command -v dpkg >/dev/null 2>&1 &&
      dpkg -s sockswebui 2>/dev/null | grep -q '^Status: install ok installed'; then
      dpkg -r sockswebui </dev/null
      kept=/var/lib/sockswebui
    elif [ -f /etc/openwrt_release ] && { [ -e /etc/init.d/sockswebui ] || [ -e /usr/bin/sockswebui ]; }; then
      kept="$(datadir_of /etc/init.d/sockswebui /etc/sockswebui)"
      if [ -e /etc/init.d/sockswebui ]; then
        /etc/init.d/sockswebui stop </dev/null >/dev/null 2>&1 || true
        /etc/init.d/sockswebui disable </dev/null >/dev/null 2>&1 || true
      fi
      rm -f /etc/rc.d/S95sockswebui /etc/rc.d/K10sockswebui /etc/init.d/sockswebui \
        /etc/init.d/sockswebui.dist /etc/sockswebui.init.sum /usr/bin/sockswebui
    elif [ -e /etc/systemd/system/sockswebui.service ] || [ -e /usr/local/bin/sockswebui ]; then
      kept="$(datadir_of /etc/systemd/system/sockswebui.service /var/lib/sockswebui)"
      if systemd_running; then
        systemctl disable --now sockswebui.service </dev/null || true
      fi
      rm -f /etc/systemd/system/sockswebui.service /etc/systemd/system/sockswebui.service.dist \
        /etc/systemd/system/sockswebui.service.sum /usr/local/bin/sockswebui
      if systemd_running; then
        systemctl daemon-reload </dev/null || true
      fi
    else
      echo "sockswebui is not installed"
      exit 0
    fi
    echo "sockswebui removed. Kept $kept (config and secrets) and the sockswebui user, if any; delete them by hand to remove everything."
    exit 0
  fi

  # Platform: dpkg, else OpenWrt, else a running systemd. Decided before any download.
  if command -v dpkg >/dev/null 2>&1; then
    kind=deb
    arch="$(dpkg --print-architecture)"
    case "$arch" in
      amd64 | arm64 | armhf) ;;
      *) die "unsupported architecture: $arch" ;;
    esac
  elif [ -f /etc/openwrt_release ]; then
    kind=openwrt
    oarch="$(sed -n "s/^DISTRIB_ARCH=['\"]*\([^'\"]*\).*/\1/p" /etc/openwrt_release)"
    case "$oarch" in
      x86_64) arch=amd64 ;;
      aarch64_*) arch=arm64 ;;
      # The GOARM=7 binary needs hardware floating point: plain arm_cortex-a9
      # (no _vfp or _neon suffix) cannot run it.
      arm_cortex-a*_*vfp* | arm_cortex-a*_*neon*) arch=armv7 ;;
      mipsel_*) arch=mipsle ;;
      mips_*) arch=mips ;;
      *) die "unsupported architecture: $oarch" ;;
    esac
  else
    machine="$(uname -m)"
    case "$machine" in
      x86_64) arch=amd64 ;;
      aarch64) arch=arm64 ;;
      armv7l) arch=armv7 ;;
      *) die "unsupported architecture: $machine" ;;
    esac
    systemd_running ||
      die "no dpkg, systemd or OpenWrt found; install sockswebui-linux-$arch by hand (see docs/INSTALL.md)"
    kind=systemd
  fi

  command -v sha256sum >/dev/null 2>&1 || die "sha256sum not found; cannot verify the download"
  if [ "$kind" = systemd ]; then
    for c in useradd getent systemctl; do
      command -v "$c" >/dev/null 2>&1 || die "$c not found; cannot install the systemd service"
    done
  fi

  # SOCKSWEBUI_BASE_URL is a test hook. It is announced, and only http(s) is allowed.
  if [ -n "${SOCKSWEBUI_BASE_URL:-}" ]; then
    base="${SOCKSWEBUI_BASE_URL%/}"
    case "$base" in
      http://* | https://*) ;;
      *) die "SOCKSWEBUI_BASE_URL must start with http:// or https://" ;;
    esac
    echo "install.sh: WARNING: downloading from $base, not GitHub" >&2
  elif [ -n "$version" ]; then
    base="$RELEASES/download/$version"
  else
    base="$RELEASES/latest/download"
  fi

  # Fetchers read no stdin, so they cannot eat the rest of a piped script.
  if command -v curl >/dev/null 2>&1; then
    case "$base" in
      https://*) fetch() { curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL -o "$2" "$1" </dev/null; } ;;
      *) fetch() { curl -fsSL -o "$2" "$1" </dev/null; } ;;
    esac
  elif command -v wget >/dev/null 2>&1; then
    # Only curl enforces HTTPS on redirects. The wget path (GNU or BusyBox)
    # relies on GitHub's redirects being HTTPS; the SHA256SUMS gate still applies.
    fetch() { wget -q -O "$2" "$1" </dev/null; }
  else
    die "need curl or wget"
  fi

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' HUP INT TERM

  # download <name>: fetch one release file into the temp dir.
  download() {
    fetch "$base/$1" "$tmp/$1" || die "download failed: $base/$1"
  }

  # verify <name>: compare the file with its one exact-name line in SHA256SUMS.
  # A missing, duplicate, CRLF or "*name" binary-style line fails closed.
  verify() {
    n="$(awk -v n="$1" '$2 == n { c++ } END { print c + 0 }' "$tmp/SHA256SUMS")"
    [ "$n" -eq 1 ] || die "checksum: want exactly one line for $1 in SHA256SUMS, found $n"
    want="$(awk -v n="$1" '$2 == n { print $1 }' "$tmp/SHA256SUMS")"
    got="$(sha256sum "$tmp/$1" | cut -d' ' -f1)"
    [ "$want" = "$got" ] || die "checksum mismatch for $1"
  }

  # put <src> <dst> <mode>: copy beside dst, then rename, so a running binary is
  # replaced, not overwritten (busybox has no install(1)).
  put() {
    rm -f "$2.new"
    cp "$1" "$2.new"
    chmod "$3" "$2.new"
    mv -f "$2.new" "$2"
  }

  # put_conf <src> <dst> <mode> <sumfile>: like put, but keeps a copy the owner
  # edited (for example a moved --data-dir). <sumfile> records the hash of the
  # release copy last installed; a file that no longer matches it was edited,
  # so the new release copy goes to <dst>.dist (0644: not a second service).
  put_conf() {
    new="$(sha256sum <"$1" | cut -d' ' -f1)"
    if [ -e "$2" ]; then
      cur="$(sha256sum <"$2" | cut -d' ' -f1)"
      last="$(cat "$4" 2>/dev/null || true)"
      if [ "$cur" != "$new" ] && [ "$cur" != "$last" ]; then
        put "$1" "$2.dist" 0644
        echo "sockswebui: kept your edited $2; the release copy is $2.dist"
        return 0
      fi
    fi
    put "$1" "$2" "$3"
    echo "$new" >"$4"
  }

  download SHA256SUMS

  if [ "$kind" = systemd ]; then
    bin="sockswebui-linux-$arch"
    for f in "$bin" sockswebui.service; do
      download "$f"
      verify "$f"
    done
    upgrade=0
    [ -e /usr/local/bin/sockswebui ] && [ -e /etc/systemd/system/sockswebui.service ] && upgrade=1
    if ! getent passwd sockswebui >/dev/null 2>&1; then
      shell="$(command -v nologin || echo /bin/false)"
      if getent group sockswebui >/dev/null 2>&1; then
        useradd --system -g sockswebui --no-create-home --home-dir /var/lib/sockswebui --shell "$shell" sockswebui
      else
        useradd --system --user-group --no-create-home --home-dir /var/lib/sockswebui --shell "$shell" sockswebui
      fi
    fi
    put "$tmp/$bin" /usr/local/bin/sockswebui 0755
    put_conf "$tmp/sockswebui.service" /etc/systemd/system/sockswebui.service 0644 \
      /etc/systemd/system/sockswebui.service.sum
    systemctl daemon-reload </dev/null
    if [ "$upgrade" = 1 ]; then
      systemctl enable sockswebui.service </dev/null
      systemctl restart sockswebui.service </dev/null || echo "sockswebui: restart failed; see journalctl -u sockswebui" >&2
    else
      systemctl enable --now sockswebui.service </dev/null || echo "sockswebui: start failed; see journalctl -u sockswebui" >&2
      url_line
    fi
    return 0
  fi

  if [ "$kind" = openwrt ]; then
    bin="sockswebui-linux-$arch"
    for f in "$bin" sockswebui.init; do
      download "$f"
      verify "$f"
    done
    upgrade=0
    [ -e /usr/bin/sockswebui ] && [ -e /etc/init.d/sockswebui ] && upgrade=1
    put "$tmp/$bin" /usr/bin/sockswebui 0755
    put_conf "$tmp/sockswebui.init" /etc/init.d/sockswebui 0755 /etc/sockswebui.init.sum
    /etc/init.d/sockswebui enable </dev/null
    if ubus list >/dev/null 2>&1 </dev/null; then
      if [ "$upgrade" = 1 ]; then
        /etc/init.d/sockswebui restart </dev/null
      else
        /etc/init.d/sockswebui start </dev/null
      fi
    else
      echo "sockswebui: procd is not running; start it with /etc/init.d/sockswebui start"
    fi
    [ "$upgrade" = 1 ] || url_line
    return 0
  fi

  n="$(awk -v a="$arch" '$2 ~ ("^sockswebui_.*_" a "\\.deb$") { c++ } END { print c + 0 }' "$tmp/SHA256SUMS")"
  [ "$n" -eq 1 ] || die "checksum: want exactly one sockswebui .deb for $arch in SHA256SUMS, found $n"
  deb="$(awk -v a="$arch" '$2 ~ ("^sockswebui_.*_" a "\\.deb$") { print $2 }' "$tmp/SHA256SUMS")"
  printf '%s\n' "$deb" | grep -Eq "^sockswebui_[A-Za-z0-9.+~-]+_${arch}\\.deb\$" ||
    die "checksum: bad package name in SHA256SUMS: $deb"
  download "$deb"
  verify "$deb"

  dpkg -i "$tmp/$deb" </dev/null
}
main "$@"
