#!/usr/bin/env bash
# Suite docker: multi-arch scratch image build, contents and run.
set -u
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need_release
VER="$(pkg_version)"

# Build needs multi-platform support: docker buildx, else podman.
E="$PKG_ENGINE"
if [ "$E" = docker ] && ! docker buildx version >/dev/null 2>&1; then E=podman; fi

TAG="localhost/sockswebui-pkgtest:$VER"
CTR="sockswebui-pkgtest-ui"
VOL="sockswebui-pkgtest"
tmp="$(mktemp -d)"
clean_engine() {
  "$E" rm -f "$CTR" >/dev/null 2>&1
  "$E" volume rm -f "$VOL" >/dev/null 2>&1
  "$E" manifest rm "$TAG" >/dev/null 2>&1
  "$E" rmi -f "$TAG" "$TAG-amd64" "$TAG-arm64" "$TAG-armv7" >/dev/null 2>&1
}
cleanup() { clean_engine; rm -rf "$tmp"; }
trap cleanup EXIT
clean_engine

# build-3-platforms
PLATS="linux/amd64,linux/arm64,linux/arm/v7"
if [ "$E" = podman ]; then
  build() { podman build --platform "$PLATS" --manifest "$TAG" "$PKG_ROOT"; }
else
  build() { docker buildx build --platform "$PLATS" -t "$TAG" "$PKG_ROOT"; }
fi
if build >"$tmp/build.log" 2>&1; then pass build-3-platforms
else fail build-3-platforms "build failed: $(tail -n 3 "$tmp/build.log" | tr '\n' ' ')"; pkg_finish docker; fi

# ref_for <plat> <suffix>: build one platform alone (layers are cached) and print its tag.
ref_for() {
  if [ "$E" = podman ]; then
    podman build --platform "$1" -t "$TAG-$2" "$PKG_ROOT" >/dev/null 2>&1
  else
    docker buildx build --platform "$1" --load -t "$TAG-$2" "$PKG_ROOT" >/dev/null 2>&1
  fi && echo "$TAG-$2"
}

# binary-per-platform
want_ca=0
bad=""
for pair in linux/amd64:amd64 linux/arm64:arm64 linux/arm/v7:armv7; do
  plat="${pair%:*}"; suffix="${pair##*:}"
  c="sockswebui-pkgtest-cp-$suffix"
  "$E" rm -f "$c" >/dev/null 2>&1
  ref="$(ref_for "$plat" "$suffix")"
  if [ -z "$ref" ] || ! "$E" create --name "$c" "$ref" >/dev/null 2>&1; then
    bad="$bad $plat(create)"; continue
  fi
  mkdir -p "$tmp/$suffix"
  "$E" cp "$c:/sockswebui" "$tmp/$suffix/sockswebui" >/dev/null 2>&1
  "$E" cp "$c:/etc/ssl/certs/ca-certificates.crt" "$tmp/$suffix/ca.crt" >/dev/null 2>&1
  "$E" rm -f "$c" >/dev/null 2>&1
  cmp -s "$tmp/$suffix/sockswebui" "$PKG_DIST/sockswebui-linux-$suffix" || bad="$bad $plat(binary)"
  [ -s "$tmp/$suffix/ca.crt" ] || bad="$bad $plat(ca)"
done
if [ -z "$bad" ]; then pass binary-per-platform; else fail binary-per-platform "mismatch:$bad"; fi

# config (amd64 image)
cfg="$("$E" image inspect --format '{{json .Config}}' "$(ref_for linux/amd64 amd64)")"
c_user="$(printf '%s' "$cfg" | python3 -c 'import json,sys;print(json.load(sys.stdin)["User"])')"
c_port="$(printf '%s' "$cfg" | python3 -c 'import json,sys;print("8787/tcp" in json.load(sys.stdin)["ExposedPorts"])')"
c_vol="$(printf '%s' "$cfg" | python3 -c 'import json,sys;print("/data" in json.load(sys.stdin)["Volumes"])')"
c_ep="$(printf '%s' "$cfg" | python3 -c 'import json,sys;print(json.dumps(json.load(sys.stdin)["Entrypoint"],separators=(",",":")))')"
c_src="$(printf '%s' "$cfg" | python3 -c 'import json,sys;print(json.load(sys.stdin)["Labels"].get("org.opencontainers.image.source"))')"
if [ "$c_user" = "65532:65532" ] && [ "$c_port" = True ] && [ "$c_vol" = True ] \
  && [ "$c_ep" = '["/sockswebui","--data-dir","/data"]' ] \
  && [ "$c_src" = "https://github.com/M-Wham/sockswebui" ]; then
  pass config
else
  fail config "user=$c_user port=$c_port vol=$c_vol ep=$c_ep src=$c_src"
fi

# version (runs only the host arch)
eq version "sockswebui $VER" "$("$E" run --rm "$TAG" --version 2>&1)"

# no-shell
check no-shell "image has a shell" test "$("$E" run --rm --entrypoint /bin/sh "$TAG" -c true >/dev/null 2>&1; echo $?)" != 0

# ui-200
"$E" volume rm -f "$VOL" >/dev/null 2>&1
code=000
if "$E" run -d --name "$CTR" -p 127.0.0.1:18787:8787 -v "$VOL:/data" "$TAG" >/dev/null 2>&1; then
  for _ in $(seq 1 20); do
    code="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18787/ 2>/dev/null)"
    [ "$code" = 200 ] && break
    sleep 1
  done
fi
if [ "$code" = 200 ]; then pass ui-200; else fail ui-200 "http code $code: $("$E" logs "$CTR" 2>&1 | tail -n 3 | tr '\n' ' ')"; fi
"$E" rm -f "$CTR" >/dev/null 2>&1

# volume-owner
own="$("$E" run --rm -v "$VOL:/data" docker.io/library/alpine stat -c "%u:%g" /data /data/.lock 2>/dev/null | tr '\n' ' ')"
eq volume-owner "65532:65532 65532:65532 " "$own"

pkg_finish docker
