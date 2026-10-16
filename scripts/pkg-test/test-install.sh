#!/usr/bin/env bash
# Suite install: dist/install.sh against a local fixture server.
set -u
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_release
VER="$(pkg_version)"
DEBVER="${VER#v}"
INSTALL="$PKG_DIST/install.sh"
tmp="$(mktemp -d)"
srv_pid=""
POST=""
INVOKE=""
BASEURL=""
cleanup() { [ -n "$srv_pid" ] && kill "$srv_pid" 2>/dev/null; rm -rf "$tmp"; }
trap cleanup EXIT

[ -f "$INSTALL" ] || { fail install-sh-exists "dist/install.sh missing"; pkg_finish install; }

# mkfix <name>: copy the release files into $tmp/<name> and write a fresh SHA256SUMS.
mkfix() {
  local d="$tmp/$1"
  mkdir -p "$d"
  cp "$PKG_DIST"/sockswebui-linux-* "$PKG_DIST"/sockswebui_*.deb "$PKG_DIST/install.sh" \
    "$PKG_DIST/sockswebui.service" "$PKG_DIST/sockswebui.init" "$d/"
  (cd "$d" && sha256sum -- * >SHA256SUMS)
  echo "$d"
}

# serve <dir> <log>: start the fixture server; sets PORT.
serve() {
  PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')"
  : >"$2"
  python3 -m http.server --bind 127.0.0.1 "$PORT" --directory "$1" >/dev/null 2>"$2" &
  srv_pid=$!
  local i
  for i in $(seq 50); do
    (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null && break
    sleep 0.1
  done
  : >"$2"
}
unserve() { kill "$srv_pid" 2>/dev/null; wait "$srv_pid" 2>/dev/null; srv_pid=""; }

# run_inst <fixdir> <image> <user|-> <pre-script> <install args>
# Prints RC=<n>, the install stderr between ERR< and ERR>, then NOTHING-INSTALLED markers.
run_inst() {
  local fix="$1" img="$2" user="$3" pre="$4" args="$5"
  local uflag=()
  [ "$user" = "-" ] || uflag=(--user "$user")
  serve "$fix" "$tmp/req.log"
  "$PKG_ENGINE" run --rm --network host "${uflag[@]}" \
    -e "SOCKSWEBUI_BASE_URL=${BASEURL:-http://127.0.0.1:$PORT}" \
    -v "$INSTALL:/install.sh:ro" "$img" sh -c "
      $pre
      ${INVOKE:-sh /install.sh} $args >/tmp/out 2>/tmp/err; echo RC=\$?
      echo 'ERR<'; cat /tmp/err; echo 'ERR>'
      dpkg -s sockswebui >/dev/null 2>&1 && echo M:DPKG
      test -e /usr/bin/sockswebui && echo M:BIN1
      test -e /usr/local/bin/sockswebui && echo M:BIN2
      getent passwd sockswebui >/dev/null && echo M:USER
      ${POST:-true}
    " 2>&1
  unserve
}
requests() { grep -c 'GET ' "$tmp/req.log"; }
errhas() { printf '%s\n' "$1" | sed -n '/^ERR</,/^ERR>/p' | grep -q -- "$2"; }
has() { printf '%s\n' "$1" | grep -qx -- "$2"; }
hasre() { printf '%s\n' "$1" | grep -q -- "$2"; }
clean() { ! hasre "$1" '^M:'; }

# dpkg-path
fix="$(mkfix ok)"
POST='dpkg -s sockswebui | grep -E "^(Status|Version):"; /usr/bin/sockswebui --version'
out="$(run_inst "$fix" "$IMG_CURL" - "" "")"
if has "$out" RC=0 && has "$out" "Status: install ok installed" && has "$out" "Version: $DEBVER" \
  && has "$out" "sockswebui $VER"; then pass dpkg-path; else fail dpkg-path "$out"; fi
POST=""

# wget-only
out="$(run_inst "$fix" "$IMG_CURL" - "rm -f /usr/bin/curl" "")"
if has "$out" RC=0 && has "$out" M:DPKG && has "$out" M:BIN1; then pass wget-only; else fail wget-only "$out"; fi

# base-warning: a custom base URL is announced on stderr
out="$(run_inst "$fix" "$IMG_CURL" - "" "")"
if errhas "$out" "install.sh: WARNING: downloading from http://127.0.0.1:[0-9]*, not GitHub"; then pass base-warning; else fail base-warning "$out"; fi

# base-scheme: only http:// and https:// are accepted
BASEURL="ftp://127.0.0.1/x"
out="$(run_inst "$fix" "$IMG_CURL" - "" "")"
if has "$out" RC=1 && errhas "$out" 'http' && clean "$out" && [ "$(requests)" -eq 0 ]; then pass base-scheme; else fail base-scheme "$out"; fi
BASEURL=""

# pipe-install: curl | sh style, script on stdin
INVOKE='cat /install.sh | sh -s --'
POST='/usr/bin/sockswebui --version'
out="$(run_inst "$fix" "$IMG_CURL" - "" "")"
if has "$out" RC=0 && has "$out" M:DPKG && has "$out" "sockswebui $VER"; then pass pipe-install; else fail pipe-install "$out"; fi
INVOKE=""
POST=""

# truncation: a script cut inside main() must not exit 0 or install anything.
# (A cut in the header comments before main() is an empty script: harmless, not tested.)
TRUNC='
  start=$(grep -b "^main() {" /install.sh | cut -d: -f1)
  sz=$(wc -c < /install.sh)
  n=$((start + 1)); bad=0; tried=0
  while [ "$n" -lt $((sz - 12)) ]; do
    head -c "$n" /install.sh | sh >/dev/null 2>&1; rc=$?
    tried=$((tried + 1))
    if [ "$rc" -eq 0 ] || dpkg -s sockswebui >/dev/null 2>&1 || [ -e /usr/bin/sockswebui ]; then
      echo "TRUNC-BAD offset=$n rc=$rc"; bad=1
    fi
    n=$((n + 97))
  done
  echo "TRUNC-TRIED=$tried"
  [ "$bad" -eq 0 ] && echo TRUNC-OK'
out="$(serve "$fix" "$tmp/req.log"; "$PKG_ENGINE" run --rm --network host -e "SOCKSWEBUI_BASE_URL=http://127.0.0.1:$PORT" -v "$INSTALL:/install.sh:ro" "$IMG_CURL" sh -c "$TRUNC" 2>&1; unserve)"
tried="$(printf '%s\n' "$out" | sed -n 's/^TRUNC-TRIED=//p')"
if has "$out" TRUNC-OK && [ "${tried:-0}" -ge 10 ]; then pass truncation; else fail truncation "$out"; fi

# checksum failures: nothing may be installed
fx="$(mkfix bad)"
printf 'x' >>"$fx/sockswebui_${DEBVER}_amd64.deb"
out="$(run_inst "$fx" "$IMG_CURL" - "" "")"
if has "$out" RC=1 && hasre "$out" 'checksum' && clean "$out"; then pass checksum-mismatch; else fail checksum-mismatch "$out"; fi

fx="$(mkfix miss)"
sed -i "/ sockswebui_${DEBVER}_amd64.deb\$/d" "$fx/SHA256SUMS"
out="$(run_inst "$fx" "$IMG_CURL" - "" "")"
if has "$out" RC=1 && hasre "$out" 'checksum' && clean "$out"; then pass checksum-missing-line; else fail checksum-missing-line "$out"; fi

fx="$(mkfix nosums)"
rm "$fx/SHA256SUMS"
out="$(run_inst "$fx" "$IMG_CURL" - "" "")"
if has "$out" RC=1 && errhas "$out" 'download failed' && clean "$out"; then pass sums-404; else fail sums-404 "$out"; fi

# wget-checksum: tampered deb on the wget path
fx="$(mkfix badw)"
printf 'x' >>"$fx/sockswebui_${DEBVER}_amd64.deb"
out="$(run_inst "$fx" "$IMG_CURL" - "rm -f /usr/bin/curl" "")"
if has "$out" RC=1 && errhas "$out" 'checksum' && clean "$out"; then pass wget-checksum-mismatch; else fail wget-checksum-mismatch "$out"; fi

# sums-shape: duplicate, binary-style, CRLF and traversal lines all fail closed
DEBN="sockswebui_${DEBVER}_amd64.deb"
shape_case() { # <name> <sed-or-shell mutation of SHA256SUMS in dir $fx>
  local name="$1"
  shift
  fx="$(mkfix "shape-$name")"
  ( cd "$fx" && "$@" )
  out="$(run_inst "$fx" "$IMG_CURL" - "" "")"
  if has "$out" RC=1 && errhas "$out" 'checksum' && clean "$out"; then pass "$name"; else fail "$name" "$out"; fi
}
dup_good_bad() { grep " $DEBN\$" SHA256SUMS >g; printf '%064d  %s\n' 0 "$DEBN" >b; cat g b >n; mv n SHA256SUMS; }
dup_bad_good() { grep " $DEBN\$" SHA256SUMS >g; printf '%064d  %s\n' 0 "$DEBN" >b; cat b g >n; mv n SHA256SUMS; }
binary_style() { sed -i "s/  $DEBN\$/ *$DEBN/" SHA256SUMS; }
crlf_sums() { sed -i 's/$/\r/' SHA256SUMS; }
traversal() { sed -i "s#  $DEBN\$#  sockswebui_../../x_amd64.deb#" SHA256SUMS; }
shape_case dup-deb-good-bad dup_good_bad
shape_case dup-deb-bad-good dup_bad_good
shape_case binary-style-line binary_style
shape_case crlf-sums crlf_sums
shape_case traversal-name traversal

# no-sha256sum
out="$(run_inst "$fix" "$IMG_CURL" - "rm -f /usr/bin/sha256sum /bin/sha256sum" "")"
if has "$out" RC=1 && errhas "$out" 'sha256sum' && clean "$out"; then pass no-sha256sum; else fail no-sha256sum "$out"; fi

# bad-version: reject before any request
bv_ok=1
for v in "" "v1..2" "1.2.3" "v1/../x" "v1 2"; do
  out="$(run_inst "$fix" "$IMG_CURL" - "" "--version '$v'")"
  has "$out" RC=1 && errhas "$out" 'version' && [ "$(requests)" -eq 0 ] || { bv_ok=0; echo "  bad-version [$v]: $out" >&2; }
done
if [ "$bv_ok" -eq 1 ]; then pass bad-version; else fail bad-version "see above"; fi

# non-root
out="$(run_inst "$fix" "$IMG_CURL" 65534:65534 "" "")"
if has "$out" RC=1 && hasre "$out" 'must run as root' && [ "$(requests)" -eq 0 ]; then pass non-root; else fail non-root "$out (requests=$(requests))"; fi

# unknown-arch-dpkg
pre='printf "#!/bin/sh\nif [ \"\$1\" = --print-architecture ]; then echo riscv64; else exec /usr/bin/dpkg \"\$@\"; fi\n" >/usr/local/bin/dpkg; chmod 755 /usr/local/bin/dpkg'
out="$(run_inst "$fix" "$IMG_CURL" - "$pre" "")"
if has "$out" RC=1 && hasre "$out" 'unsupported architecture: riscv64' && [ "$(requests)" -eq 0 ]; then pass unknown-arch-dpkg; else fail unknown-arch-dpkg "$out"; fi

# other-host
out="$(run_inst "$fix" "docker.io/library/alpine:3" - "" "")"
if has "$out" RC=1 && hasre "$out" 'sockswebui-linux-amd64' && hasre "$out" 'docs/INSTALL.md' && clean "$out"; then pass other-host; else fail other-host "$out"; fi

# url-default / url-version: a stub curl logs its URL and fails; no base URL is set
url_case() {
  local name="$1" args="$2" want="$3"
  local o
  o="$("$PKG_ENGINE" run --rm -v "$INSTALL:/install.sh:ro" "$IMG_CURL" sh -c "
    printf '#!/bin/sh\nfor a; do :; done\necho \"\$a\" >> /tmp/curl.log\nexit 22\n' >/usr/local/bin/curl
    chmod 755 /usr/local/bin/curl
    sh /install.sh $args >/dev/null 2>&1; echo RC=\$?
    head -1 /tmp/curl.log
  " 2>&1)"
  eq "$name" "$(printf 'RC=1\n%s' "$want")" "$o"
}
url_case url-default "" "https://github.com/M-Wham/sockswebui/releases/latest/download/SHA256SUMS"
url_case url-version "--version v1.2.3" "https://github.com/M-Wham/sockswebui/releases/download/v1.2.3/SHA256SUMS"

# bad-option
o="$("$PKG_ENGINE" run --rm -v "$INSTALL:/install.sh:ro" "$IMG_CURL" sh -c 'sh /install.sh --bogus 2>&1; echo RC=$?')"
if printf '%s\n' "$o" | grep -q '^usage:' && printf '%s\n' "$o" | grep -qx 'RC=2'; then pass bad-option; else fail bad-option "$o"; fi

# url-https-flags: on the default (GitHub) base, curl must refuse non-https redirects
o="$("$PKG_ENGINE" run --rm -v "$INSTALL:/install.sh:ro" "$IMG_CURL" sh -c '
  printf "#!/bin/sh\necho \"\$*\" >> /tmp/curl.log\nexit 22\n" >/usr/local/bin/curl
  chmod 755 /usr/local/bin/curl
  sh /install.sh >/dev/null 2>&1
  head -1 /tmp/curl.log
' 2>&1)"
if hasre "$o" "--proto =https --proto-redir =https" && hasre "$o" "releases/latest/download/SHA256SUMS\$"; then
  pass url-https-flags
else
  fail url-https-flags "$o"
fi

# --- T4: systemd (non-dpkg) and OpenWrt paths, --uninstall ---
UNIT_SUM="$(sha256sum <"$PKG_DIST/sockswebui.service" | cut -d' ' -f1)"
INIT_SUM="$(sha256sum <"$PKG_DIST/sockswebui.init" | cut -d' ' -f1)"
SC_STUB='mkdir -p /run/systemd/system
printf "#!/bin/sh\necho \"\$*\" >> /tmp/sc.log\nexit 0\n" >/usr/local/sbin/systemctl
chmod 755 /usr/local/sbin/systemctl'

# systemd-path + systemd-uninstall (archlinux: systemd, no dpkg)
POST='echo "MODE=$(stat -c "%a %U" /usr/local/bin/sockswebui)"
/usr/local/bin/sockswebui --version
echo "UNIT=$(sha256sum </etc/systemd/system/sockswebui.service | cut -d" " -f1)"
echo "UID=$(getent passwd sockswebui | cut -d: -f3)"
sed "s/^/SC:/" /tmp/sc.log
grep -q "set the admin password now" /tmp/out && echo WARN
: >/tmp/sc.log
mkdir -p /var/lib/sockswebui && echo "{}" >/var/lib/sockswebui/config.json
sh /install.sh --uninstall >/tmp/uout 2>&1; echo URC=$?
sed "s/^/USC:/" /tmp/sc.log
test -e /usr/local/bin/sockswebui || echo UBIN-GONE
test -e /etc/systemd/system/sockswebui.service || echo UUNIT-GONE
test -f /var/lib/sockswebui/config.json && echo UDATA-KEPT'
out="$(run_inst "$fix" "$IMG_ARCH" - "$SC_STUB" "")"
uid="$(printf '%s\n' "$out" | sed -n 's/^UID=//p')"
if has "$out" RC=0 && has "$out" "MODE=755 root" && has "$out" "sockswebui $VER" \
  && has "$out" "UNIT=$UNIT_SUM" && [ -n "$uid" ] && [ "$uid" -lt 1000 ] \
  && has "$out" "SC:daemon-reload" && has "$out" "SC:enable --now sockswebui.service" && has "$out" WARN; then
  pass systemd-path
else
  fail systemd-path "$out"
fi
if has "$out" URC=0 && has "$out" "USC:disable --now sockswebui.service" && has "$out" UBIN-GONE \
  && has "$out" UUNIT-GONE && has "$out" UDATA-KEPT; then
  pass systemd-uninstall
else
  fail systemd-uninstall "$out"
fi

# openwrt-path + openwrt-uninstall (busybox ash, uclient-fetch wget, no ubus in a container)
INVOKE='ash /install.sh'
POST='/usr/bin/sockswebui --version
echo "MODE=$(ls -l /etc/init.d/sockswebui | cut -c1-10)"
echo "INIT=$(sha256sum </etc/init.d/sockswebui | cut -d" " -f1)"
test -e /etc/rc.d/S95sockswebui && echo RCD
grep -q "set the admin password now" /tmp/out && echo WARN
mkdir -p /etc/sockswebui && echo "{}" >/etc/sockswebui/config.json
ash /install.sh --uninstall >/tmp/uout 2>&1; echo URC=$?
test -e /usr/bin/sockswebui || echo UBIN-GONE
test -e /etc/init.d/sockswebui || echo UINIT-GONE
test -e /etc/rc.d/S95sockswebui || echo URCD-GONE
test -f /etc/sockswebui/config.json && echo UDATA-KEPT'
# A booted OpenWrt has /var/lock (procd.sh locks there); the bare rootfs image does not.
out="$(run_inst "$fix" "$IMG_OPENWRT" - "mkdir -p /var/lock" "")"
if has "$out" RC=0 && has "$out" "sockswebui $VER" && has "$out" "MODE=-rwxr-xr-x" \
  && has "$out" "INIT=$INIT_SUM" && has "$out" RCD && has "$out" WARN; then
  pass openwrt-path
else
  fail openwrt-path "$out"
fi
if has "$out" URC=0 && has "$out" UBIN-GONE && has "$out" UINIT-GONE && has "$out" URCD-GONE \
  && has "$out" UDATA-KEPT; then
  pass openwrt-uninstall
else
  fail openwrt-uninstall "$out"
fi

# checksum-mismatch-openwrt: tampered binary, nothing installed
fx="$(mkfix badow)"
printf 'x' >>"$fx/sockswebui-linux-amd64"
POST='test -e /etc/init.d/sockswebui && echo M:INIT'
out="$(run_inst "$fx" "$IMG_OPENWRT" - "" "")"
if has "$out" RC=1 && errhas "$out" 'checksum' && clean "$out"; then pass checksum-mismatch-openwrt; else fail checksum-mismatch-openwrt "$out"; fi
INVOKE=""

# dpkg-uninstall
POST='mkdir -p /var/lib/sockswebui && echo "{}" >/var/lib/sockswebui/config.json
sh /install.sh --uninstall >/tmp/uout 2>&1; echo URC=$?
dpkg -s sockswebui 2>/dev/null | grep -q "^Status: install ok installed" && echo USTILL
test -f /var/lib/sockswebui/config.json && echo UDATA-KEPT'
out="$(run_inst "$fix" "$IMG_CURL" - "" "")"
if has "$out" RC=0 && has "$out" URC=0 && ! has "$out" USTILL && has "$out" UDATA-KEPT; then
  pass dpkg-uninstall
else
  fail dpkg-uninstall "$out"
fi
POST=""

# systemd-reinstall: second run over an install whose unit was edited by the owner
POST='echo "# owner edit" >>/etc/systemd/system/sockswebui.service
: >/tmp/sc.log
sh /install.sh >/tmp/out2 2>/tmp/err2; echo R2=$?
sed "s/^/SC2:/" /tmp/sc.log
grep -q "# owner edit" /etc/systemd/system/sockswebui.service && echo EDIT-KEPT
echo "DIST=$(sha256sum </etc/systemd/system/sockswebui.service.dist | cut -d" " -f1)"
grep -q "set the admin password now" /tmp/out2 && echo WARN2
grep -q "sockswebui.service.dist" /tmp/out2 /tmp/err2 && echo NOTICE'
out="$(run_inst "$fix" "$IMG_ARCH" - "$SC_STUB" "")"
if has "$out" R2=0 && has "$out" "SC2:enable sockswebui.service" && has "$out" "SC2:restart sockswebui.service" \
  && ! has "$out" WARN2 && has "$out" EDIT-KEPT && has "$out" "DIST=$UNIT_SUM" && has "$out" NOTICE; then
  pass systemd-reinstall
else
  fail systemd-reinstall "$out"
fi

# systemd-hand-binary: a hand-installed binary without a unit is a fresh install
POST='/usr/local/bin/sockswebui --version; sed "s/^/SC:/" /tmp/sc.log; grep -q "set the admin password now" /tmp/out && echo WARN'
out="$(run_inst "$fix" "$IMG_ARCH" - "$SC_STUB
mkdir -p /usr/local/bin; printf '#!/bin/sh\necho old\n' >/usr/local/bin/sockswebui; chmod 755 /usr/local/bin/sockswebui" "")"
if has "$out" RC=0 && has "$out" "sockswebui $VER" && has "$out" "SC:enable --now sockswebui.service" && has "$out" WARN; then
  pass systemd-hand-binary
else
  fail systemd-hand-binary "$out"
fi

# systemd-checksum-unit: a tampered unit stops the install before anything is placed
fx="$(mkfix badunit)"
printf '# x\n' >>"$fx/sockswebui.service"
POST='test -e /etc/systemd/system/sockswebui.service && echo M:UNIT'
out="$(run_inst "$fx" "$IMG_ARCH" - "$SC_STUB" "")"
if has "$out" RC=1 && errhas "$out" 'checksum' && clean "$out"; then pass systemd-checksum-unit; else fail systemd-checksum-unit "$out"; fi

# systemd-no-useradd: refused before anything is placed
POST=""
out="$(run_inst "$fix" "$IMG_ARCH" - "$SC_STUB
rm -f /usr/sbin/useradd /usr/bin/useradd" "")"
if has "$out" RC=1 && errhas "$out" 'useradd' && clean "$out"; then pass systemd-no-useradd; else fail systemd-no-useradd "$out"; fi

# openwrt-reinstall: an edited init script (e.g. a moved --data-dir) is kept
INVOKE='ash /install.sh'
POST='sed -i "s#--data-dir /etc/sockswebui#--data-dir /mnt/usb/sockswebui#" /etc/init.d/sockswebui
ash /install.sh >/tmp/out2 2>/tmp/err2; echo R2=$?
grep -q -- "--data-dir /mnt/usb/sockswebui" /etc/init.d/sockswebui && echo EDIT-KEPT
echo "DIST=$(sha256sum </etc/init.d/sockswebui.dist | cut -d" " -f1)"
grep -q "sockswebui.dist" /tmp/out2 /tmp/err2 && echo NOTICE
ash /install.sh --uninstall >/tmp/uout 2>&1; echo URC=$?
grep -q "/mnt/usb/sockswebui" /tmp/uout && echo KEPT-PATH
test -e /etc/init.d/sockswebui.dist || echo UDIST-GONE'
out="$(run_inst "$fix" "$IMG_OPENWRT" - "mkdir -p /var/lock" "")"
if has "$out" R2=0 && has "$out" EDIT-KEPT && has "$out" "DIST=$INIT_SUM" && has "$out" NOTICE \
  && has "$out" URC=0 && has "$out" KEPT-PATH && has "$out" UDIST-GONE; then
  pass openwrt-reinstall
else
  fail openwrt-reinstall "$out"
fi

# openwrt-arm-novfp: arm_cortex-a9 without VFP cannot run the GOARM=7 binary
POST=""
out="$(run_inst "$fix" "$IMG_OPENWRT" - "sed -i \"s/^DISTRIB_ARCH=.*/DISTRIB_ARCH='arm_cortex-a9'/\" /etc/openwrt_release" "")"
if has "$out" RC=1 && errhas "$out" 'unsupported architecture: arm_cortex-a9' && [ "$(requests)" -eq 0 ]; then
  pass openwrt-arm-novfp
else
  fail openwrt-arm-novfp "$out"
fi

# openwrt-partial-uninstall: binary only (no init script) is still removed
POST='ash /install.sh --uninstall >/tmp/uout 2>&1; echo URC=$?; test -e /usr/bin/sockswebui || echo UBIN-GONE'
out="$(run_inst "$fix" "$IMG_OPENWRT" - "mkdir -p /usr/bin; printf x >/usr/bin/sockswebui" "--help")"
if has "$out" URC=0 && has "$out" UBIN-GONE; then pass openwrt-partial-uninstall; else fail openwrt-partial-uninstall "$out"; fi
INVOKE=""
POST=""

# systemd-stale-unit: an unedited unit from an older release is replaced, not kept
POST='printf "[Unit]\nDescription=old release\n" >/etc/systemd/system/sockswebui.service
sha256sum </etc/systemd/system/sockswebui.service | cut -d" " -f1 >/etc/systemd/system/sockswebui.service.sum
sh /install.sh >/tmp/out2 2>&1; echo R2=$?
echo "UNIT2=$(sha256sum </etc/systemd/system/sockswebui.service | cut -d" " -f1)"
test -e /etc/systemd/system/sockswebui.service.dist && echo HAS-DIST
sh /install.sh --uninstall >/dev/null 2>&1
test -e /etc/systemd/system/sockswebui.service.sum && echo SUM-LEFT'
out="$(run_inst "$fix" "$IMG_ARCH" - "$SC_STUB" "")"
if has "$out" R2=0 && has "$out" "UNIT2=$UNIT_SUM" && ! has "$out" HAS-DIST && ! has "$out" SUM-LEFT; then
  pass systemd-stale-unit
else
  fail systemd-stale-unit "$out"
fi

# openwrt-dist-mode: the .dist copy is not executable (LuCI lists executables in /etc/init.d)
INVOKE='ash /install.sh'
POST='echo "# edit" >>/etc/init.d/sockswebui; ash /install.sh >/dev/null 2>&1
echo "DMODE=$(ls -l /etc/init.d/sockswebui.dist | cut -c1-10)"'
out="$(run_inst "$fix" "$IMG_OPENWRT" - "mkdir -p /var/lock" "")"
if has "$out" "DMODE=-rw-r--r--"; then pass openwrt-dist-mode; else fail openwrt-dist-mode "$out"; fi

# openwrt-armv7-map: hard-float 32-bit ARM targets fetch the armv7 binary
am_ok=1
for da in arm_cortex-a7_neon-vfpv4 arm_cortex-a9_vfpv3-d16 arm_cortex-a53_neon-vfpv4; do
  POST=""
  run_inst "$fix" "$IMG_OPENWRT" - "sed -i \"s/^DISTRIB_ARCH=.*/DISTRIB_ARCH='$da'/\" /etc/openwrt_release" "" >/dev/null
  grep -q 'GET /sockswebui-linux-armv7 ' "$tmp/req.log" || { am_ok=0; echo "  armv7-map [$da]: $(cat "$tmp/req.log")" >&2; }
done
if [ "$am_ok" -eq 1 ]; then pass openwrt-armv7-map; else fail openwrt-armv7-map "see above"; fi
INVOKE=""
POST=""

# uninstall-nothing: clean host, no download
POST='cat /tmp/out'
out="$(run_inst "$fix" "$IMG_CURL" - "" "--uninstall")"
POST=""
if has "$out" RC=0 && hasre "$out" 'sockswebui is not installed' && [ "$(requests)" -eq 0 ]; then
  pass uninstall-nothing
else
  fail uninstall-nothing "$out"
fi

# uninstall-non-root
out="$(run_inst "$fix" "$IMG_CURL" 65534:65534 "" "--uninstall")"
if has "$out" RC=1 && hasre "$out" 'must run as root' && [ "$(requests)" -eq 0 ]; then
  pass uninstall-non-root
else
  fail uninstall-non-root "$out"
fi

# shellcheck
sc="$("$PKG_ENGINE" run --rm -v "$PKG_DIST:/mnt:ro" "$IMG_SHELLCHECK" -s sh /mnt/install.sh 2>&1)"
if [ -z "$sc" ]; then pass shellcheck; else fail shellcheck "$sc"; fi

pkg_finish install
