# Installing sockswebui

sockswebui is a single static binary with the web UI embedded. There are five ways to install it:

- the [install script](#install-script) (quickest);
- the [Debian package](#debian-package-deb);
- a [Docker image](#docker);
- by hand on Raspberry Pi OS / Debian (systemd);
- by hand on OpenWrt (procd).

The by-hand steps cover Raspberry Pi OS / Debian (systemd) and OpenWrt (procd). The shipped unit and init script are
`dist/sockswebui.service` and `dist/sockswebui.init`. Verified on amd64 (Debian 13 LXC on an AMD thin client, 2026-10-13); arm64, arm/v7, mipsle and mips need more testing. After installing, follow [docs/SMOKE.md](SMOKE.md) to check it.

## Requirements

The device needs at least 512 MB of RAM. With 4 slots under load, peak memory can exceed 256 MB. A lower minimum is planned.

## Choosing a binary

`make release` builds static binaries (`CGO_ENABLED=0`) into `dist/`. Pick the one that matches
`uname -m` on the device:

| `uname -m` | Device class | Binary |
|---|---|---|
| `x86_64` | PC, thin client, VM (64-bit Intel/AMD) | `sockswebui-linux-amd64` |
| `aarch64` | Raspberry Pi 3/4/5 or other 64-bit ARM, on a 64-bit OS | `sockswebui-linux-arm64` |
| `armv7l` | 32-bit ARM OS (e.g. 32-bit Raspberry Pi OS), many ARM routers | `sockswebui-linux-armv7` |
| `mips` | Big-endian MIPS routers (soft-float) | `sockswebui-linux-mips` |
| `mips` (little-endian) | Little-endian MIPS routers, such as many MediaTek devices (soft-float) | `sockswebui-linux-mipsle` |

`make release` also writes `dist/SHA256SUMS`. Check the download with `sha256sum -c SHA256SUMS`.

Check it runs with `./sockswebui-linux-<arch> --version`. Copy it to the device with `scp`.

Flags: `--version`, `--data-dir` (directory holding `config.json`), `--ui-addr` (UI listen address; overrides the Settings value only when passed).

## Install script

`install.sh` is the quickest way to install on a supported host. It is POSIX `sh` and also runs
under BusyBox `ash`. Run it as root. It needs `curl` or `wget` with HTTPS support, and `sha256sum`.

```sh
curl -fsSL https://github.com/M-Wham/sockswebui/releases/latest/download/install.sh | sudo sh
```

To read the script before you run it, download it with `SHA256SUMS`, check it, then run it:

```sh
curl -fsSLO https://github.com/M-Wham/sockswebui/releases/latest/download/install.sh
curl -fsSLO https://github.com/M-Wham/sockswebui/releases/latest/download/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
sudo sh install.sh
```

The script downloads the release files and checks each one against `SHA256SUMS`. If a check fails,
it stops before it installs anything. It never passes `--ui-addr`. What it installs depends on the host:

| Host | What the script installs |
|---|---|
| Has `dpkg` (Debian, Raspberry Pi OS, Ubuntu) | The `.deb` for the host architecture. See "Debian package (.deb)". |
| Other systemd host | `/usr/local/bin/sockswebui`, the system user `sockswebui` and `/etc/systemd/system/sockswebui.service`. It then enables and starts the unit. Data: `/var/lib/sockswebui`. |
| OpenWrt | `/usr/bin/sockswebui` and the procd script `/etc/init.d/sockswebui`. It enables the service and starts it. Data: `/etc/sockswebui`. |
| Any other host | The script stops with an error that names the binary to install by hand. |

OpenWrt is the only way to get the MIPS builds (`mips` and `mipsle`) from the script. On 32-bit ARM
OpenWrt, the target must have hardware floating point (a `_vfp…` or `_neon…` suffix in `DISTRIB_ARCH`).
Plain `arm_cortex-a9` targets, such as bcm53xx, are refused.

Running the script again upgrades in place and restarts the service. If you edited the installed unit or
init script (for example to move `--data-dir`), the script keeps your copy and writes the new release copy
next to it as `sockswebui.service.dist` or `sockswebui.dist`. Merge the changes by hand. A unit or init
script that the script did not install itself (for example one copied by hand from these docs) counts as
edited the first time.

Options:

- `--version vX.Y.Z` installs that release instead of the latest one.
- `--uninstall` removes the package, or the binary and the service files. It keeps the data
  directory, and it prints the path it kept. It needs no download.

After a first install, the script prints the UI address. Open it and set the admin password at once.

## Debian package (.deb)

Each release has one `.deb` per architecture. Find yours with `dpkg --print-architecture`:

| `dpkg --print-architecture` | Device class | Package |
|---|---|---|
| `amd64` | PC, thin client, VM (64-bit Intel/AMD) | `sockswebui_<ver>_amd64.deb` |
| `arm64` | Raspberry Pi 3/4/5 on a 64-bit OS | `sockswebui_<ver>_arm64.deb` |
| `armhf` | 32-bit Raspberry Pi OS on a Pi 2/3/4/5 | `sockswebui_<ver>_armhf.deb` |

The `armhf` package is built for ARMv7. It does not run on the Pi Zero or Pi 1. MIPS has no `.deb`.

Download the package and `SHA256SUMS` from the release page, then check and install:

```sh
sha256sum -c --ignore-missing SHA256SUMS
sudo apt install ./sockswebui_<ver>_<arch>.deb
```

The package installs the binary at `/usr/bin/sockswebui` and the unit at
`/usr/lib/systemd/system/sockswebui.service`. It creates the system user `sockswebui`, then enables
and starts the service. It prints the UI address. Set the admin password at once.

- **Upgrade:** install the newer `.deb` the same way. The service restarts. `config.json` is kept.
- **Remove:** `sudo apt remove sockswebui` stops and disables the service. It keeps `/var/lib/sockswebui`.
- **Purge:** `sudo apt purge sockswebui` also deletes `/var/lib/sockswebui` and the `sockswebui` user.
  This deletes all secrets in `config.json`, and you cannot undo it.

## Docker

The image is `ghcr.io/m-wham/sockswebui`. It has builds for `linux/amd64`, `linux/arm64` and
`linux/arm/v7`. It runs as UID 65532 and keeps its data in the volume `/data`.

```sh
docker run -d --name sockswebui --restart unless-stopped --network host -v sockswebui-data:/data ghcr.io/m-wham/sockswebui:latest
```

Use `--network host`. You choose the slot ports in the UI at run time, so Docker cannot publish
them in advance. The UI listens on port 8787 of the host.

Bridge mode also works. Then you must publish each slot port with `-p`, and you must set the bind
address of each slot to `0.0.0.0`. Also publish 8787 for the UI.

The UI accepts only IP addresses, `localhost`, the host name and `--ui-host` names. Inside Docker the
host name is the container's. To use any other name, add `--ui-host <name>` after the image name:

```sh
docker run -d --name sockswebui --restart unless-stopped --network host -v sockswebui-data:/data ghcr.io/m-wham/sockswebui:latest --ui-host proxy.lan
```

Rules for the image:

- Use it on hosts in a trusted LAN only. Never publish port 8787 to the internet.
- Finish first-run setup at once. Until you set the admin password, anyone who reaches the UI can set it.
- The volume holds secrets (`config.json`). Protect it and its backups.
- To clear the admin password, stop the main container, then run a one-off container on the same volume:

  ```sh
  docker stop sockswebui
  docker run --rm -v sockswebui-data:/data ghcr.io/m-wham/sockswebui:latest --reset-admin
  docker start sockswebui
  ```

  Then open the UI and set a new password at once.

## Install by hand

The next two sections install the binary yourself. Use them when the script and the package do not fit.

## Raspberry Pi OS / Debian (systemd)

1. Install the binary (paths below match the unit file):

   ```sh
   sudo install -m755 sockswebui-linux-arm64 /usr/local/bin/sockswebui
   ```

2. Create a dedicated unprivileged user:

   ```sh
   sudo useradd -r -M -s /usr/sbin/nologin sockswebui
   ```

3. Install the unit and start it:

   ```sh
   sudo install -m644 dist/sockswebui.service /etc/systemd/system/sockswebui.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now sockswebui
   ```

The unit runs as `sockswebui` and uses `StateDirectory=sockswebui`, so systemd creates
`/var/lib/sockswebui` (mode 0700) for the data directory. Check with
`systemctl status sockswebui` and `journalctl -u sockswebui`.

### Changing the UI address

The unit does not pass `--ui-addr`, so the UI listens on the address in Settings (`ui_bind_addr`, default
`0.0.0.0:8787`: all interfaces, port 8787). Change it in the Settings screen; it applies at the next restart.
To force an address from the command line instead (the flag wins over Settings), use a drop-in rather than
editing the unit:

```sh
sudo systemctl edit sockswebui
```

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/sockswebui --data-dir /var/lib/sockswebui --ui-addr 127.0.0.1:8787
```

Then `sudo systemctl restart sockswebui`. The UI has no TLS and is meant for a trusted LAN only.

### Upgrade and uninstall

Upgrade: `sudo systemctl stop sockswebui`, run the `install -m755` command again with the new
binary, then `sudo systemctl start sockswebui`. `config.json` is kept.

Uninstall: `sudo systemctl disable --now sockswebui`, remove `/etc/systemd/system/sockswebui.service`
and `/usr/local/bin/sockswebui`, run `sudo systemctl daemon-reload`. Delete `/var/lib/sockswebui`
and `userdel sockswebui` only if you also want to discard your configuration.

## OpenWrt (procd)

The init script runs the binary as root, because OpenWrt's base system has no `useradd`. The
binary path is `/usr/bin/sockswebui` (not `/usr/local/bin`, which is used on Debian).

1. Copy the files from your computer (use `scp -O` if the router has no SFTP server):

   ```sh
   scp sockswebui-linux-armv7 root@192.168.1.1:/usr/bin/sockswebui
   scp dist/sockswebui.init root@192.168.1.1:/etc/init.d/sockswebui
   ```

2. On the router:

   ```sh
   chmod 755 /usr/bin/sockswebui /etc/init.d/sockswebui
   /etc/init.d/sockswebui enable
   /etc/init.d/sockswebui start
   ```

### Data directory (persistent storage)

The data directory is `/etc/sockswebui`. `/var` is a tmpfs on OpenWrt and is lost on reboot, so
state must be on persistent flash storage; `/etc` is on the overlay. The init script creates the
directory with mode 0700. Config writes are rare, so flash wear is small. To use `/overlay` or an
external mount instead, edit `--data-dir` in `/etc/init.d/sockswebui`.

### UI address

The script does not pass `--ui-addr`, so the UI listens on the address in Settings (`ui_bind_addr`,
default `0.0.0.0:8787`). OpenWrt's default firewall drops inbound WAN traffic, so the UI is normally
reachable from the LAN only. To bind the LAN only, set the router's LAN IP (for example
`192.168.1.1:8787`) in Settings and run `/etc/init.d/sockswebui restart`.

### Upgrade and uninstall

Upgrade: `/etc/init.d/sockswebui stop`, copy the new binary to `/usr/bin/sockswebui`, then
`/etc/init.d/sockswebui start`. Uninstall: `/etc/init.d/sockswebui disable`, stop it, delete
`/usr/bin/sockswebui` and `/etc/init.d/sockswebui`; delete `/etc/sockswebui` to discard configuration.

## First run

> **Finish setup immediately.** Until the admin password is set, anyone who reaches the UI can set
> it and claim the device. On versions without the Host guard, a web page opened on your LAN could
> also do this through DNS rebinding; this version rejects requests whose `Host` is not an IP
> address, `localhost`, the device's hostname (or `<hostname>.local`) or a `--ui-host` name. Finish
> setup straight after install, after clearing the admin password with `--reset-admin`, and after
> restoring a backup.

Open `http://<device-ip>:8787/` from a LAN machine. On a fresh data directory the UI listens on
`0.0.0.0:8787` unless `--ui-addr` is passed. The first-run setup screen asks you to choose an admin
password (at least 8 characters). The password is stored only as an argon2id hash.

Optional, to keep the setup screen off the network: do the first boot by hand on loopback, before
the service starts, and set the password through an SSH tunnel.

1. On the device, start the binary with a loopback address. On Debian (the data directory does not
   exist until the service first starts, so create it):

   ```sh
   sudo install -d -m 0700 -o sockswebui -g sockswebui /var/lib/sockswebui
   sudo -u sockswebui /usr/local/bin/sockswebui --data-dir /var/lib/sockswebui --ui-addr 127.0.0.1:8787
   ```

   On OpenWrt: `/usr/bin/sockswebui --data-dir /etc/sockswebui --ui-addr 127.0.0.1:8787`.
2. From your computer: `ssh -L 8787:127.0.0.1:8787 <user>@<device-ip>`, then open
   `http://127.0.0.1:8787/` and set the admin password.
3. Stop the hand-started binary (Ctrl+C) and restart without the flag: start the service
   (`sudo systemctl start sockswebui` or `/etc/init.d/sockswebui start`). `--ui-addr` is not
   saved; without it the UI uses the stored `ui_bind_addr` setting.

For this to help, run step 1 before the service is first started (on Debian, use
`sudo systemctl enable sockswebui` without `--now` in install step 3).

### Resetting the admin password

The `--reset-admin` flag is always present. Stop the service first, then run it. It clears the
admin password and the session secret, keeps endpoints and slots, and the next start opens
first-run setup. It also lists the `config.json.bad-*` and `config.json.pre-import-*` files that may
still hold the old hash. Delete them if the password may be compromised. **Finish setup immediately** after restarting, or do the restart
on loopback as in the optional steps above.

## Config file and secrets

`config.json` lives in the data directory (`/var/lib/sockswebui` on Debian, `/etc/sockswebui` on
OpenWrt). It is written with mode 0600 and holds secrets: WireGuard private and preshared keys, proxy
passwords, the admin password hash and the session secret. Do not commit it or share it, and treat
backups of it as secret. If it cannot be parsed at start-up it is moved to `config.json.bad-<ts>`;
see [docs/SMOKE.md](SMOKE.md).

Before a config import replaces the endpoints and slots, the current config is saved next to it as
`config.json.pre-import-<yyyymmddThhmmssZ>` (UTC; `.1`, `.2`, ... is appended if two imports land in
the same second). These backups hold every endpoint and slot secret (WireGuard keys, proxy and SOCKS passwords) in
plaintext, so protect them like `config.json`. They do **not** hold the admin password hash or the
session secret (both are written empty). Backups made by older versions still hold the hash; treat
them like `config.json.bad-*` files. Only the 3 newest are kept; older ones are
deleted after each import.

To restore a backup:

1. Stop the service (`sudo systemctl stop sockswebui` on Debian, `/etc/init.d/sockswebui stop` on
   OpenWrt).
2. Copy the backup over `config.json` and keep it mode 0600, for example
   `sudo install -m 0600 -o sockswebui -g sockswebui /var/lib/sockswebui/config.json.pre-import-<ts> /var/lib/sockswebui/config.json`
   (on OpenWrt: `cp /etc/sockswebui/config.json.pre-import-<ts> /etc/sockswebui/config.json && chmod 0600 /etc/sockswebui/config.json`).
3. Start the service again. A restored backup holds no admin password, so the UI boots into open
   first-visit setup and anyone who reaches it can claim the device: **finish setup immediately**.
   To keep setup off the network, restore with the service stopped and start the binary by hand
   with `--ui-addr 127.0.0.1:8787`, set the password through an SSH tunnel
   (`ssh -L 8787:127.0.0.1:8787 <user>@<device-ip>`), then stop it and start the service, as in
   "First run". Backups from older versions still hold the admin hash, so a restore of one keeps
   the old admin password and setup stays closed.

## Security notes

- Slots with an empty route, and allow-direct slots falling back to the device route, cannot reach loopback, link-local or cloud metadata addresses (for example `127.0.0.1`, `169.254.169.254`); such connections are refused.
- A health URL that points at a loopback, link-local or metadata address therefore fails the probe on those slots, and the slot does not reach UP.
- By default those same slots can reach private LAN addresses (`192.168.0.0/16`, `10.0.0.0/8`, `172.16.0.0/12`, `fc00::/7`, `100.64.0.0/10` and similar). The slot editor's **Block LAN targets** toggle (config field `block_lan_targets`, default off) refuses them too; a refused connection gets SOCKS reply 0x01. The toggle appears only for slots with an empty route or kill switch `allow-direct`.
- Turn on **Block LAN targets** when such a slot can be reached from beyond your LAN: for example a router port forward to the SOCKS port, or an open-proxy bind (`0.0.0.0` without SOCKS auth). Otherwise anyone who reaches the port can use the slot to reach hosts on your LAN.
- Loopback, link-local and cloud metadata addresses (`169.254.169.254`, `fd00:ec2::254`, `100.100.100.200`) stay blocked whether the toggle is on or off.
- WireGuard and upstream proxy routes are not affected: their traffic exits at the remote end. The toggle only applies to an empty route and to the allow-direct fallback while the slot is DEGRADED.
- If you roll back to a version without this setting, the old binary ignores `block_lan_targets` and still loads the config, but slots that had it on lose the block and can reach the LAN again.
- The deny list covers IPv4/IPv6 loopback, link-local, metadata and the NAT64 well-known prefix `64:ff9b::/96`. Network-specific NAT64 prefixes and ISATAP addresses are not recognised.
- LAN ranges are reachable through empty-route and allow-direct slots by default. Binding the SOCKS listener to a non-loopback address without SOCKS auth (an open proxy) is allowed; the slot editor shows a warning.

### Secrets at rest, lost device

- `config.json` and every file in the data directory hold secrets in plaintext (mode 0600). Use full-disk encryption where the device supports it.
- `config.json.bad-<ts>` files are raw copies of a config that failed to load. They hold every secret in plaintext, including the admin password hash. Only the 3 newest are kept (older ones are deleted when a new one is written); start-up salvage reads the newest one. Delete them yourself once you no longer need them.
- If the device is lost, stolen or may be compromised: revoke the WireGuard keys at your provider (for Mullvad, remove the device or key in your account), change any upstream proxy passwords, then delete old `config.json.bad-*` files and the `config.json.pre-import-*` backups.
- Upstream HTTP and SOCKS5 proxy credentials are sent in cleartext on the wire unless a WireGuard hop comes before the proxy in the route. For a proxy that is the first hop, prefer an IP literal over a host name: the name is resolved by the device's resolver, and the first hop's host is not filtered against the deny list.
- `sudo -u sockswebui sockswebui --reset-admin` (stop the service first) clears the admin password and session secret. It then lists the `config.json.bad-*` and `config.json.pre-import-*` files that may still hold the old hash. Delete them if the password may be compromised, and finish setup immediately after restarting.

### SOCKS limits

- A relay is closed after 15 minutes with no payload bytes in either direction. TCP keepalive packets do not count. Idle SSH and IMAP IDLE sessions are cut unless they send application keepalives under 15 minutes, for example `ServerAliveInterval 60` in `~/.ssh/config`.
- Each SOCKS listener accepts up to 256 concurrent connections, and 128 from any one source (an IPv4 address or an IPv6 /64). A full slot can hold roughly 20-25 MB of buffers for up to 15 minutes on small routers.

### UI access

- The Settings value `ui_bind_addr` takes effect after a restart, unless `--ui-addr` is passed (the flag wins).
- The UI accepts requests whose `Host` is an IP literal, `localhost`, the device hostname or `<hostname>.local`. Any other name is refused with "host not allowed"; add it with `--ui-host <name>` (repeatable).
- The login rate limit counts one IPv6 /64 as one client.
- One sockswebui instance runs per data directory. It takes a lock on `<data-dir>/.lock`; a second start logs an error and exits with status 1. Stop the first instance before using `--reset-admin` or starting another.

## Reverse proxy

sockswebui serves plain HTTP and does not terminate TLS. To reach the UI over HTTPS, put a reverse proxy in front of it. Keep the UI bound to loopback (`--ui-addr 127.0.0.1:8787`, see "Changing the UI address") so the proxy is the only way in. Replace `<public name>` with the DNS name users type in the browser.

What the proxy must do:

- **Forward the original `Host`.** The UI refuses any `Host` that is not an accepted name (`internal/api/hostguard.go:19-23`). Caddy forwards it by default; nginx needs `proxy_set_header Host $host`.
- **Start sockswebui with `--ui-host <public name>`.** This adds the public name to the accepted list used for both the `Host` check and the `Origin` check (`internal/api/names.go:10-19`, `internal/api/origin.go:13-16`). The port is ignored, so a proxy on 443 works. Without it, every request gets 403 "host not allowed". In the systemd drop-in, extend the `ExecStart` line: `... --ui-addr 127.0.0.1:8787 --ui-host <public name>`.
- **Add `Secure` to the session cookie at the proxy.** The app sets `Secure` only when the request itself arrived over TLS (`internal/api/auth.go:146`, `auth.go:181`: `r.TLS != nil`). Behind a proxy the app sees plain HTTP, so the cookie `sockswebui_session` leaves without `Secure` unless the proxy adds it. The app never trusts `X-Forwarded-Proto` or any other forwarded header.
- **Rate-limit at the proxy.** The app limits setup, login, password change and config export/import to 5 per minute per client, keyed on the TCP peer address (`internal/api/authlimit.go:16-30`); `X-Forwarded-For` is ignored. Behind a proxy every browser has the proxy's address, so all clients share one limiter key: a few failed logins from anyone lock everyone out for the window, and the app cannot tell attackers apart. Apply a per-client limit in the proxy to `POST /api/setup`, `/api/login`, `/api/config/export` and `/api/config/import`. A password change is a `PUT /api/settings` with a body, which a path rule cannot single out; limit that path too if you want it covered, since ordinary settings saves are rare.
- **Do not buffer `/api/events`.** It is a Server-Sent Events stream.
- **Keep `Content-Type: application/json` on JSON bodies.** The API answers 415 to any other type. Do not rewrite this header in the proxy.

### nginx

Needs nginx 1.19.3 or newer for `proxy_cookie_flags`. Put `limit_req_zone` in the `http` block, the rest in a `server` block that already has your TLS certificate settings.

```nginx
limit_req_zone $binary_remote_addr zone=sockswebui_auth:1m rate=5r/m;

server {
    listen 443 ssl;
    server_name <public name>;
    # ssl_certificate / ssl_certificate_key: your own settings

    location ~ ^/api/(setup|login|config/export|config/import)$ {
        limit_req zone=sockswebui_auth burst=5 nodelay;
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_cookie_flags sockswebui_session secure;
    }

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_cookie_flags sockswebui_session secure;
        proxy_http_version 1.1;
        proxy_buffering off;   # keeps /api/events streaming
        proxy_read_timeout 1h;
    }
}
```

### Caddy

Caddy forwards the original `Host` by default and gets its own certificate for `<public name>`. The `header_down` line adds `Secure` to the session cookie. Stock Caddy has no rate limiter: add one with a plugin such as `caddy-ratelimit`, or put the limit in a firewall in front of it.

```caddyfile
<public name> {
    reverse_proxy 127.0.0.1:8787 {
        flush_interval -1
        header_down Set-Cookie "^(sockswebui_session=.*)$" "$1; Secure"
    }
}
```

`flush_interval -1` keeps `/api/events` streaming.

### Keep SSH as a fallback

Do not rely on the proxy as the only way in. If the proxy or its certificate breaks, reach the UI directly with an SSH tunnel to the loopback listener (`ssh -L 8787:127.0.0.1:8787 <user>@<device-ip>`, then open `http://127.0.0.1:8787/`). An IP-literal `Host` is always accepted.

## Verifying

Follow [docs/SMOKE.md](SMOKE.md) for the manual smoke checks.
