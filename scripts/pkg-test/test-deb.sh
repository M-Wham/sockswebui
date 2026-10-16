#!/usr/bin/env bash
# Suite deb: .deb build, contents and install.
set -u
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_release
VER="$(pkg_version)"
DEBVER="${VER#v}"
BUILD="$PKG_ROOT/packaging/build-deb.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# deb-version
v1="$(sh "$BUILD" --deb-version v1.0.0 2>&1)"
v2="$(sh "$BUILD" --deb-version 3a2e87f 2>&1)"
v3="$(sh "$BUILD" --deb-version 'bad ver!' 2>&1)"; rc3=$?
if [ "$v1" = "1.0.0" ] && [ "$v2" = "0.0.0~git3a2e87f" ] && [ "$rc3" -eq 1 ] \
  && [ "$v3" = "build-deb: cannot map VERSION 'bad ver!' to a Debian version" ]; then
  pass deb-version
else
  fail deb-version "got [$v1] [$v2] [$v3] rc=$rc3"
fi

# build into a scratch dir so the test is independent of make release having run build-deb
mkdir "$tmp/a" "$tmp/b"
for d in a b; do
  for f in sockswebui-linux-amd64 sockswebui-linux-arm64 sockswebui-linux-armv7 sockswebui.service; do
    ln -s "$PKG_DIST/$f" "$tmp/$d/$f"
  done
done
export SOURCE_DATE_EPOCH=1700000000
: >"$tmp/a/sockswebui_0.0.1_amd64.deb"   # stale deb from an earlier build
sh "$BUILD" "$VER" "$tmp/a" >/dev/null 2>&1
if [ -e "$tmp/a/sockswebui_0.0.1_amd64.deb" ]; then fail stale-deb "build-deb left an older .deb in dist"; else pass stale-deb; fi
sh "$BUILD" "$VER" "$tmp/b" >/dev/null 2>&1

arches="amd64:amd64 arm64:arm64 armhf:armv7"
want_ar="$(printf 'debian-binary\ncontrol.tar.gz\ndata.tar.gz')"
ar_ok=1
for pair in $arches; do
  a="${pair%%:*}"
  got="$(ar t "$tmp/a/sockswebui_${DEBVER}_$a.deb" 2>&1)"
  [ "$got" = "$want_ar" ] || { ar_ok=0; fail ar-order "$a: [$got]"; }
done
[ "$ar_ok" -eq 1 ] && pass ar-order

rep_ok=1
for pair in $arches; do
  a="${pair%%:*}"
  cmp -s "$tmp/a/sockswebui_${DEBVER}_$a.deb" "$tmp/b/sockswebui_${DEBVER}_$a.deb" || rep_ok=0
done
if [ "$rep_ok" -eq 1 ]; then pass reproducible; else fail reproducible "debs differ"; fi

# unit
want_unit="$tmp/unit.want"
sed 's#^ExecStart=/usr/local/bin/sockswebui #ExecStart=/usr/bin/sockswebui #' "$PKG_DIST/sockswebui.service" >"$want_unit"

# in-container checks, one run per arch
for pair in $arches; do
  a="${pair%%:*}"
  bin="${pair##*:}"
  deb="sockswebui_${DEBVER}_$a.deb"
  out="$("$PKG_ENGINE" run --rm -v "$tmp/a:/deb:ro" -v "$PKG_DIST:/dist:ro" "$IMG_DEBIAN" sh -c "
    set -e
    dpkg-deb --field /deb/$deb Package Version Architecture
    echo ---
    dpkg-deb --contents /deb/$deb | awk '{print \$1, \$2, \$6}'
    echo ---
    mkdir /x && dpkg-deb -x /deb/$deb /x
    cmp /x/usr/bin/sockswebui /dist/sockswebui-linux-$bin && echo BINSAME
    cat /x/usr/lib/systemd/system/sockswebui.service
  " 2>&1)"
  fields="$(printf '%s\n' "$out" | sed -n '1,/^---$/p' | sed '$d')"
  eq "fields-$a" "$(printf 'Package: sockswebui\nVersion: %s\nArchitecture: %s' "$DEBVER" "$a")" "$fields"
  contents="$(printf '%s\n' "$out" | sed '1,/^---$/d' | sed '/^---$/,$d')"
  want_c="$(printf '%s\n' \
    'drwxr-xr-x root/root ./' \
    'drwxr-xr-x root/root ./usr/' \
    'drwxr-xr-x root/root ./usr/bin/' \
    '-rwxr-xr-x root/root ./usr/bin/sockswebui' \
    'drwxr-xr-x root/root ./usr/lib/' \
    'drwxr-xr-x root/root ./usr/lib/systemd/' \
    'drwxr-xr-x root/root ./usr/lib/systemd/system/' \
    '-rw-r--r-- root/root ./usr/lib/systemd/system/sockswebui.service')"
  eq "contents-$a" "$want_c" "$contents"
  case "$out" in *BINSAME*) pass "binary-$a" ;; *) fail "binary-$a" "usr/bin/sockswebui differs from dist" ;; esac
  unit_got="$(printf '%s\n' "$out" | sed '1,/BINSAME/d')"
  if [ "$a" = amd64 ]; then
    if [ "$unit_got" = "$(cat "$want_unit")" ]; then pass unit; else fail unit "packaged unit differs"; fi
  fi
done

# dpkg-install
inst="$("$PKG_ENGINE" run --rm -v "$tmp/a:/deb:ro" "$IMG_DEBIAN" sh -c "dpkg -i /deb/sockswebui_${DEBVER}_amd64.deb >/dev/null 2>&1 && /usr/bin/sockswebui --version" 2>&1)"
eq dpkg-install "sockswebui $VER" "$inst"

# maintainer scripts, all in debian:13 with the amd64 deb
DEB="/deb/sockswebui_${DEBVER}_amd64.deb"
run_deb() { "$PKG_ENGINE" run --rm -v "$tmp/a:/deb:ro" "$IMG_DEBIAN" sh -c "$1" 2>&1; }
STUB='mkdir -p /usr/local/sbin /run/systemd/system
printf "#!/bin/sh\necho \"\$*\" >> /tmp/sc.log\nexit 0\n" > /usr/local/sbin/systemctl
chmod 755 /usr/local/sbin/systemctl'

# no-systemd
out="$(run_deb "
  dpkg -i $DEB; echo RC1=\$?
  getent passwd sockswebui; getent group sockswebui
  dpkg -i $DEB >/dev/null 2>&1; echo RC2=\$?
")"
pw="$(printf '%s\n' "$out" | grep '^sockswebui:x:[0-9]*:[0-9]*:' | head -1)"
uid="$(printf '%s' "$pw" | cut -d: -f3)"
if printf '%s\n' "$out" | grep -q '^RC1=0$' && printf '%s\n' "$out" | grep -q '^RC2=0$' \
  && [ -n "$uid" ] && [ "$uid" -lt 1000 ] \
  && [ "$(printf '%s' "$pw" | cut -d: -f6-)" = "/var/lib/sockswebui:/usr/sbin/nologin" ] \
  && printf '%s\n' "$out" | grep -q '^sockswebui:x:[0-9]*:$' \
  && printf '%s\n' "$out" | grep -q 'set the admin password now' \
  && printf '%s\n' "$out" | grep -q ':8787/'; then
  pass no-systemd
else
  fail no-systemd "$out"
fi

# systemd-stub
out="$(run_deb "
  $STUB
  dpkg -i $DEB >/dev/null 2>&1; echo RC1=\$?
  echo LOG1; cat /tmp/sc.log
  : > /tmp/sc.log
  dpkg -i $DEB >/dev/null 2>&1; echo RC2=\$?
  echo LOG2; cat /tmp/sc.log
  : > /tmp/sc.log
  dpkg -r sockswebui >/dev/null 2>&1; echo RC3=\$?
  echo LOG3; cat /tmp/sc.log
")"
l1="$(printf '%s\n' "$out" | sed -n '/^LOG1$/,/^RC2=/p')"
l2="$(printf '%s\n' "$out" | sed -n '/^LOG2$/,/^RC3=/p')"
l3="$(printf '%s\n' "$out" | sed -n '/^LOG3$/,$p')"
if printf '%s\n' "$l1" | grep -qx 'daemon-reload' && printf '%s\n' "$l1" | grep -qx 'enable sockswebui.service' && printf '%s\n' "$l1" | grep -qx 'start sockswebui.service' \
  && printf '%s\n' "$l2" | grep -qx 'daemon-reload' && printf '%s\n' "$l2" | grep -qx 'try-restart sockswebui.service' \
  && ! printf '%s\n' "$l2" | grep -q 'enable' \
  && printf '%s\n' "$l3" | grep -qx 'disable --now sockswebui.service' \
  && printf '%s\n' "$out" | grep -q '^RC1=0$' && printf '%s\n' "$out" | grep -q '^RC2=0$' \
  && printf '%s\n' "$out" | grep -q '^RC3=0$'; then
  pass systemd-stub
else
  fail systemd-stub "$out"
fi

# reinstall-after-remove: -i, -r, -i must enable and start again and warn again
out="$(run_deb "
  $STUB
  dpkg -i $DEB >/dev/null 2>&1
  dpkg -r sockswebui >/dev/null 2>&1
  : > /tmp/sc.log
  dpkg -i $DEB 2>&1; echo RC=\$?
  echo LOG; cat /tmp/sc.log
")"
lr="$(printf '%s\n' "$out" | sed -n '/^LOG$/,$p')"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$lr" | grep -qx 'enable sockswebui.service' \
  && printf '%s\n' "$lr" | grep -qx 'start sockswebui.service' \
  && printf '%s\n' "$out" | grep -q 'set the admin password now'; then
  pass reinstall-after-remove
else
  fail reinstall-after-remove "$out"
fi

# group-exists: a leftover group must not break the install
out="$(run_deb "
  groupadd --system sockswebui
  dpkg -i $DEB >/dev/null 2>&1; echo RC=\$?
  getent passwd sockswebui >/dev/null && echo USER_OK
")"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$out" | grep -qx USER_OK; then
  pass group-exists
else
  fail group-exists "$out"
fi

# start-fails: a failed start must not wedge dpkg
out="$(run_deb "
  mkdir -p /usr/local/sbin /run/systemd/system
  printf '#!/bin/sh\n[ \"\$1\" = start ] && exit 1\nexit 0\n' > /usr/local/sbin/systemctl
  chmod 755 /usr/local/sbin/systemctl
  dpkg -i $DEB 2>&1 >/dev/null; echo RC=\$?
  dpkg -s sockswebui | grep '^Status'
")"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$out" | grep -q 'start failed' \
  && printf '%s\n' "$out" | grep -q 'install ok installed'; then
  pass start-fails
else
  fail start-fails "$out"
fi

# abort-remove: re-enable and start
out="$(run_deb "
  $STUB
  dpkg -i $DEB >/dev/null 2>&1
  : > /tmp/sc.log
  /var/lib/dpkg/info/sockswebui.postinst abort-remove; echo RC=\$?
  echo LOG; cat /tmp/sc.log
")"
la="$(printf '%s\n' "$out" | sed -n '/^LOG$/,$p')"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$la" | grep -qx 'enable sockswebui.service' \
  && printf '%s\n' "$la" | grep -qx 'start sockswebui.service'; then
  pass abort-remove
else
  fail abort-remove "$out"
fi

# marker-symlink: the service user must not be able to redirect the marker write
out="$(run_deb "
  dpkg -i $DEB >/dev/null 2>&1
  mkdir -p /var/lib/sockswebui; chown sockswebui:sockswebui /var/lib/sockswebui
  echo victim > /root/victim
  setpriv --reuid=sockswebui --regid=sockswebui --clear-groups ln -s /root/victim /var/lib/sockswebui/.removed
  dpkg -r sockswebui >/dev/null 2>&1; echo RC=\$?
  cat /root/victim
  stat -c 'M=%U %F' /var/lib/sockswebui.removed
  dpkg -P sockswebui >/dev/null 2>&1
  test ! -e /var/lib/sockswebui.removed && echo MARKER_GONE
")"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$out" | grep -qx 'victim' \
  && printf '%s\n' "$out" | grep -qx 'M=root regular empty file' \
  && printf '%s\n' "$out" | grep -qx MARKER_GONE; then
  pass marker-symlink
else
  fail marker-symlink "$out"
fi

# abort-clears-marker: prerm remove, postinst abort-remove, then an upgrade is not a first install
out="$(run_deb "
  $STUB
  dpkg -i $DEB >/dev/null 2>&1
  /var/lib/dpkg/info/sockswebui.prerm remove
  /var/lib/dpkg/info/sockswebui.postinst abort-remove
  : > /tmp/sc.log
  /var/lib/dpkg/info/sockswebui.postinst configure 0.9.0 2>&1; echo RC=\$?
  echo LOG; cat /tmp/sc.log
")"
la="$(printf '%s\n' "$out" | sed -n '/^LOG$/,$p')"
if printf '%s\n' "$out" | grep -qx 'RC=0' && ! printf '%s\n' "$la" | grep -q enable \
  && ! printf '%s\n' "$out" | grep -q 'set the admin password now'; then
  pass abort-clears-marker
else
  fail abort-clears-marker "$out"
fi

# deconfigure-marker: prerm deconfigure then configure is a fresh enable
out="$(run_deb "
  $STUB
  dpkg -i $DEB >/dev/null 2>&1
  /var/lib/dpkg/info/sockswebui.prerm deconfigure
  : > /tmp/sc.log
  /var/lib/dpkg/info/sockswebui.postinst configure 0.9.0 2>&1; echo RC=\$?
  echo LOG; cat /tmp/sc.log
")"
ld="$(printf '%s\n' "$out" | sed -n '/^LOG$/,$p')"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$ld" | grep -qx 'enable sockswebui.service' \
  && printf '%s\n' "$ld" | grep -qx 'start sockswebui.service' \
  && printf '%s\n' "$out" | grep -q 'set the admin password now'; then
  pass deconfigure-marker
else
  fail deconfigure-marker "$out"
fi

# upgrade 0.9.0 -> 0.9.1 and purge, with the systemd stub
mkdir "$tmp/c"
for f in sockswebui-linux-amd64 sockswebui-linux-arm64 sockswebui-linux-armv7 sockswebui.service; do
  ln -s "$PKG_DIST/$f" "$tmp/c/$f"
done
sh "$BUILD" v0.9.1 "$tmp/c" >/dev/null 2>&1
cp "$tmp/c/sockswebui_0.9.1_amd64.deb" "$tmp/a/"
out="$(run_deb "
  $STUB
  dpkg -i $DEB >/dev/null 2>&1
  : > /tmp/sc.log
  dpkg -i /deb/sockswebui_0.9.1_amd64.deb >/dev/null 2>&1; echo RC=\$?
  echo LOG; cat /tmp/sc.log
  /usr/bin/sockswebui --version
  : > /tmp/sc.log
  dpkg -P sockswebui >/dev/null 2>&1; echo RCP=\$?
  test ! -e /var/lib/sockswebui && echo DATA_GONE
  getent passwd sockswebui >/dev/null; echo PW=\$?
")"
lu="$(printf '%s\n' "$out" | sed -n '/^LOG$/,/^sockswebui /p')"
if printf '%s\n' "$out" | grep -qx 'RC=0' && printf '%s\n' "$lu" | grep -qx 'daemon-reload' \
  && printf '%s\n' "$lu" | grep -qx 'try-restart sockswebui.service' \
  && ! printf '%s\n' "$lu" | grep -q 'enable' \
  && printf '%s\n' "$out" | grep -qx 'RCP=0' && printf '%s\n' "$out" | grep -qx DATA_GONE \
  && printf '%s\n' "$out" | grep -qx 'PW=2'; then
  pass upgrade-and-purge-systemd
else
  fail upgrade-and-purge-systemd "$out"
fi

# remove-keeps-data
out="$(run_deb "
  dpkg -i $DEB >/dev/null 2>&1
  mkdir -p /var/lib/sockswebui; echo '{}' > /var/lib/sockswebui/config.json
  chown sockswebui:sockswebui /var/lib/sockswebui/config.json
  dpkg -r sockswebui >/dev/null 2>&1; echo RC=\$?
  test ! -e /usr/bin/sockswebui && echo BIN_GONE
  test ! -e /usr/lib/systemd/system/sockswebui.service && echo UNIT_GONE
  test -f /var/lib/sockswebui/config.json && echo DATA_KEPT
  getent passwd sockswebui >/dev/null && echo USER_KEPT
")"
ok=1
for m in RC=0 BIN_GONE UNIT_GONE DATA_KEPT USER_KEPT; do printf '%s\n' "$out" | grep -qx "$m" || ok=0; done
if [ "$ok" -eq 1 ]; then pass remove-keeps-data; else fail remove-keeps-data "$out"; fi

# purge: after remove, and directly from the installed state
out="$(run_deb "
  dpkg -i $DEB >/dev/null 2>&1
  mkdir -p /var/lib/sockswebui; echo '{}' > /var/lib/sockswebui/config.json
  dpkg -r sockswebui >/dev/null 2>&1
  dpkg -P sockswebui >/dev/null 2>&1; echo RCA=\$?
  test ! -e /var/lib/sockswebui && echo A_DATA_GONE
  getent passwd sockswebui >/dev/null; echo A_PW=\$?
  getent group sockswebui >/dev/null; echo A_GR=\$?
  dpkg -i $DEB >/dev/null 2>&1
  mkdir -p /var/lib/sockswebui; echo '{}' > /var/lib/sockswebui/config.json
  dpkg -P sockswebui >/dev/null 2>&1; echo RCB=\$?
  test ! -e /var/lib/sockswebui && echo B_DATA_GONE
  getent passwd sockswebui >/dev/null; echo B_PW=\$?
  getent group sockswebui >/dev/null; echo B_GR=\$?
")"
ok=1
for m in RCA=0 A_DATA_GONE A_PW=2 A_GR=2 RCB=0 B_DATA_GONE B_PW=2 B_GR=2; do printf '%s\n' "$out" | grep -qx "$m" || ok=0; done
if [ "$ok" -eq 1 ]; then pass purge; else fail purge "$out"; fi

# purge-literal: exactly two literal removals
rl="$(grep -nE 'rm -r?f' "$PKG_ROOT/packaging/deb/postrm" 2>/dev/null | sed 's/^[0-9]*:[[:space:]]*//')"
if [ "$rl" = "$(printf 'rm -rf /var/lib/sockswebui\nrm -f /var/lib/sockswebui.removed')" ]; then
  pass purge-literal
else
  fail purge-literal "got [$rl]"
fi

# shellcheck
sc="$("$PKG_ENGINE" run --rm -v "$PKG_ROOT/packaging:/mnt:ro" "$IMG_SHELLCHECK" -s sh /mnt/build-deb.sh /mnt/deb/postinst /mnt/deb/prerm /mnt/deb/postrm 2>&1)"
if [ -z "$sc" ]; then pass shellcheck; else fail shellcheck "$sc"; fi

pkg_finish deb
