# sockswebui — smoke notes

## Corrupt config.json on boot

If `config.json` cannot be parsed or fails validation, sockswebui moves it to
`config.json.bad-<ts>` (UTC timestamp, original bytes kept), starts with default settings,
no endpoints and no slots. The parse error is exposed by the API as `config_error` in
`GET /api/settings` (the UI banner that displays it arrives in the UI phase).

Admin credentials are salvaged when possible: if the bad file is still valid JSON and its
`settings` object holds an `admin_password_hash` that is a well-formed argon2id PHC
string (version, cost bounds and base64 salt/key all checked, as at login) **and** a
non-empty `session_secret`, those two values (and nothing else) are carried into the new
default `config.json`, which is written immediately. Setup stays locked (`POST /api/setup`
→ 409) and the existing admin password still logs in.

When the credentials cannot be salvaged (JSON syntax error anywhere in the file, missing or
non-argon2id hash, missing session secret), the log shows a `level=WARN` line saying
"admin credentials could not be recovered ... setup is open". In that state
`POST /api/setup` re-opens, and **anyone on the LAN who reaches the UI first can claim the
admin account**. Complete setup promptly (or restrict access to the UI port until you do),
then restore endpoints and slots by hand from `config.json.bad-<ts>`.

A quarantine-only boot is salvaged, not fresh: if `config.json` is missing but a
`config.json.bad-<ts>[.N]` exists (e.g. power was lost before the salvaged config reached
disk, or saving it failed), sockswebui reads only the **newest** quarantine file (latest
timestamp, then highest `.N`), salvages the credentials from it as above and writes
`config.json`; `config_error` names that file. If the newest file holds nothing
salvageable, `config_error` is still set and the same WARN is logged (setup open). A
quarantine file that cannot be read stops the boot (exit 1). `config.json.pre-import-*`
backups are never used as a salvage source.

Smoke check:

1. `sockswebui --data-dir /tmp/sw` and complete setup in the UI.
2. Stop it; edit `/tmp/sw/config.json` so `"slots"` is `"oops"`; start it again.
3. Expect `config.json.bad-<ts>`, a non-empty `config_error` in `GET /api/settings`, `POST /api/setup` → 409, login with the old password → 200.
4. Stop it; truncate `config.json` mid-file; start it again.
5. Expect the WARN line and `POST /api/setup` open again.

Deleting `config.json` no longer resets the admin password: a boot with no `config.json` but a
`config.json.bad-*` file salvages the hash from the newest one (see above). To clear the admin
password use `sockswebui --reset-admin` with the service stopped.

## Restoring a pre-import backup

`config.json.pre-import-*` backups keep every endpoint and slot secret but write
`admin_password_hash` and `session_secret` empty, so a restored backup opens first-run setup.

1. With `sockswebui --data-dir /tmp/sw` set up, run a config import so a
   `config.json.pre-import-<ts>` file appears; `grep -c 'admin_password_hash\|session_secret'`
   on it prints `0`.
2. Stop it; `cp /tmp/sw/config.json.pre-import-<ts> /tmp/sw/config.json`; start it on
   `--ui-addr 127.0.0.1:8787`.
3. Expect login with the old password → 409 ("setup required"), `POST /api/setup` → 204, the
   restored endpoints and slots present, and any `config.json.bad-*` files byte-for-byte unchanged.

## Unreadable config.json on boot

If `config.json` exists but cannot be read (permission denied, it is a directory, I/O
error), or a corrupt file cannot be moved to `config.json.bad-<ts>`, sockswebui logs one
`level=ERROR` line naming `config.json` and the error, does **not** listen on the UI port,
and exits with status 1. The file is left untouched (no `.bad-*` copy). Under systemd the
unit shows as failed; fix ownership/permissions of the data dir and restart.

Smoke check:

1. `D=$(mktemp -d); mkdir $D/config.json; sockswebui --data-dir $D --ui-addr 127.0.0.1:18084; echo "exit=$?"`
2. Expect the error line, `exit=1`, nothing listening on 18084, and no `config.json.bad-*`.
3. As a non-root user, `chmod 000` on a real `config.json` gives the same result
   (root ignores file modes, so use the directory case when testing as root).

# On-device smoke checklist

Manual, human-run, on a real Raspberry Pi / thin client. Automated tests cannot run any of this (no
device, no real Mullvad, no real upstream proxy). Never paste real private keys,
preshared keys or proxy passwords into this repo, an issue or a log; keep results to IPs you
are happy to share, and redact the rest.

Conventions used below: slot states are `STOPPED`, `STARTING`, `UP`, `DEGRADED`, `ERROR`.
The slot row in the Slots table shows the state badge, the exit IP/country and latency
from the health probe, and the kill-switch badge. `<dev>` is the device LAN address and
`<port>` the slot's bind port.

## First login (argon2)

1. Start `sockswebui` fresh (empty data dir) and open the UI from a LAN browser.
   Expect: first-run page forcing creation of the admin password (stored as an argon2id hash).
2. Set a password, log out, log in again. Expect: login succeeds; a wrong password is refused.
3. Note the time the first login takes on the device. Expect: noticeable but short (argon2id
   on a small CPU); record it, a multi-second stall is worth a note.

## Real Mullvad configs

Needs a Mullvad account with WireGuard config files downloaded from Mullvad (`.conf` or the zip).

1. Drag the zip (or several `.conf` files) onto the Endpoints drop zone.
   Expect: a per-file results table with `imported`, `skipped-duplicate` or `error: <reason>`
   per file; relay name and country filled in from names like `se-got-wg-001.conf`.
2. Drop the same zip again. Expect: every file `skipped-duplicate`.
3. Create a slot with one Mullvad endpoint as its only hop, then start it.
   Expect: `STARTING` then `UP`; the slot row shows an exit IP and country.
4. From a LAN client run `curl --socks5-hostname <dev>:<port> https://am.i.mullvad.net/json`.
   Expect: the exit IP equals the one on the slot row and is the Mullvad relay.
5. Edit the slot's route while it runs and save. Expect: stop/rebuild/start, no hot mutation.

Mullvad allows a limited number of devices (keys) per account, and one WireGuard endpoint
(one key/config) can be used by only one running slot at a time. Starting a second slot on
an endpoint a running slot holds is refused with a 409 "wireguard endpoint in use". For
several concurrent slots, generate one Mullvad key per concurrent slot and import each
separately. Proxy endpoints can be shared between slots.

## Real upstream proxy

Needs a real upstream SOCKS5 or HTTP proxy account. Enter its credentials
in the UI only.

1. Endpoints: add an upstream proxy (type, host, port, user/password). Expect: saved; the
   password is never shown again, only that it is set.
2. Slot with route `[proxy]` only. Start it. Expect: `UP`, exit IP is the proxy's IP.
3. Slot with route `[Mullvad, proxy]`. Start it. Expect: `UP`, exit IP is the proxy's exit
   IP, not the Mullvad relay (exit == last hop).
4. Try to save `[proxy, Mullvad]`. Expect: rejected with a field error (WireGuard hops must
   come first).
5. Use a deliberately wrong proxy password. Expect: slot does not reach `UP` and reports
   the failure; fix it and restart, expect `UP`.

## Kill switch: pull the cable

Policy `block` (default, fail closed). Use a slot that is `UP` with a route that has a Mullvad hop.
Have a LAN client ready, and know the device's own public IP (what you would see with no proxy).

1. Confirm `curl --socks5-hostname <dev>:<port> https://am.i.mullvad.net/json` works and shows
   the exit IP. Expect: exit IP, not the device's own.
2. Pull the WAN cable (or block the hop's host: upstream proxy off, or WireGuard endpoint
   dropped at the router). Expect: no change yet.
3. Wait one health interval (default `interval_s` 30, times `fail_threshold` 2 failed
   probes). Expect: the slot row goes `DEGRADED` and the exit IP/latency stop updating; the log
   shows the transition.
4. Repeat the `curl --socks5-hostname` from step 1. Expect: it FAILS (SOCKS reply 0x01,
   general failure). It must never print the device's own public IP.
5. Watch the log while it stays `DEGRADED`. Expect: rebuild attempts with growing backoff
   (5 s doubling up to 5 min), no direct connections.
6. Restore the cable. Expect: a rebuild succeeds, the slot returns to `UP`, and the
   `curl` from step 1 works again with the same exit IP.

Policy `allow-direct` (opposite expectation). Set the slot's kill switch to `allow-direct`
and repeat steps 2-6.

1. Expect: the slot row shows a persistent red allow-direct badge.
2. At step 4 the `curl` now SUCCEEDS but shows the DEVICE's own public IP, and each fallback
   connection is logged. This is the intended behavior of the opt-in, not a leak.
3. At step 6, `UP` returns and the exit IP is the route's exit again.
4. Set the slot back to `block` afterwards.

## Throughput

Automated tests do not measure this. The expected speed
for userspace WireGuard on a Pi is "~100-300 Mbps",
which is acceptable for proxy use. The table below is for the human to fill in.

Recipe, with a LAN client and a fast target:

- `iperf3`: run `iperf3 -s` on a host you control behind the exit. iperf3 cannot speak
  SOCKS5, so run it through `proxychains4` with a config entry `socks5 <device-ip> <slot-port>`
  (TCP only): `proxychains4 iperf3 -c <iperf-host> -t 30` (and `-R` for download).
- or `curl`: `curl --socks5-hostname <dev>:<port> -o /dev/null -w '%{speed_download}\n' <large-file-url>`;
  multiply bytes/s by 8 and divide by 1e6 for Mbps. Run it three times and take the median.

Watch `top` on the device during the run; note whether the CPU is the bottleneck.

| Device | Arch | Route | Mbps down | Mbps up | Notes |
|--------|------|-------|-----------|---------|-------|
|        |      | WG only (Mullvad) |  |  |  |
|        |      | WG -> proxy |  |  |  |

## Four slots at once

Needs four different routes and therefore four WireGuard endpoints (one Mullvad key per
concurrent WG slot, see the Mullvad section) or a mix of WG and proxy routes, e.g.:
slot 1 `[Mullvad A]`, slot 2 `[Mullvad B]`, slot 3 `[Mullvad C, proxy]`, slot 4 `[proxy]`.

1. Configure the four slots on different ports and start them all. Expect: all four `UP`.
2. Try to start a further slot that reuses a WG endpoint held by a running slot.
   Expect: refused (409, endpoint in use).
3. Use four LAN clients (or four shells), one per slot, each running
   `curl --socks5-hostname <dev>:<port_n> https://am.i.mullvad.net/json` (any IP-echo URL
   works for proxy-only routes). Expect: four different exit IPs, each equal to the last
   hop of its route and equal to the slot row in the Slots table.
4. Run traffic on all four at the same time for a few minutes. Expect: no slot leaves `UP`.
5. Stop one slot only. Expect: the other three keep working.
6. Stop traffic and leave all four idle for a few minutes, then on the device run
   `ps -o rss,cmd -C sockswebui` (RSS in kB). Expect: RSS < 65536 kB (64 MB).
   Record the value and the arch.
7. CI-side measure: `make rss-check` runs the `-tags testrig` amd64 build with four rig
   slots idle and reads VmRSS/VmHWM; it must exit 0. It is an upper bound for the amd64
   build only; the `ps -o rss` check above on the real device (arm64, arm v7) is the one that
   counts for the Pi.

| Device | Arch | RSS kB (4 idle) | make rss-check hwm_kb |
|--------|------|-----------------|-----------------------|
|        |      |                 |                       |

## Rotate a proxy session (New IP)

Needs a rotating proxy account that picks the exit IP from a session ID in the username
or password (example: `USER-country-GB-ssid-{session}`).
Each New IP is billed per IP by many providers, so this costs real money: do at most two or
three rotations. Rotation is manual only: the app never rotates by itself, on a timer, on a
backoff rebuild or on a restart. Use your own account values only in the UI, never here.

1. Endpoints → Upstream proxies → **Add proxy** (or **Edit** an existing one). Enter the
   gateway host and port, and put `{session}` exactly once in the username or the password.
   Tick **Rotating session**, leave **ID characters** on "Letters and digits" and set
   **ID length (1-32)** to what the provider accepts, then **Save proxy**.
   Expect: the list row shows `session ID <id>` (and `password set, contains {session}` if
   the token is in the password); the password text is never shown.
2. Create a slot whose route ends with this proxy (route `[proxy]` or `[Mullvad, proxy]`) and
   start it. Expect: `UP`; the slot row shows an exit IP.
3. From a LAN client run `curl --socks5-hostname <dev>:<port> https://am.i.mullvad.net/json`
   (any IP-echo URL works) twice, a minute apart. Expect: the same exit IP both times, equal to
   the slot row (sticky session). Write it down as "exit IP before".
4. Press **New IP** on the endpoint row (or on the slot row). Expect: a dialog "Get a new IP
   for <endpoint>? Slots <names> will reconnect and open connections will drop. Some providers
   bill per IP." If an affected slot uses `allow-direct` it also says the slot will use this
   device's own connection if the provider rejects the new session.
5. **Confirm new IP**. Expect: the `session ID` on the endpoint row changes; the slot goes
   `STARTING` then `UP`; the log shows one `session rotated endpoint=<name> id=<new id>` line
   per affected slot; slots that do not use this endpoint keep running untouched.
6. Repeat the `curl` from step 3. Expect: a different exit IP ("exit IP after"), equal to the
   slot row once it is `UP`. The provider dashboard, if it lists sessions, shows the new ID.
7. Press **New IP** again within 5 s of step 5. Expect: "New IP was just requested. Try again
   in N seconds." and nothing changes (no second billed IP).
8. Stop and start the slot, then restart the service (`sudo systemctl restart sockswebui`).
   Expect: the `session ID` and the exit IP stay the same as "exit IP after" (no implicit
   rotation).

Result (date, provider, exit IP before → after, session ID kept after restart y/n): ______

## Encrypted export on device A, import on device B

Needs two devices running sockswebui, each already set up with its **own** admin password
and UI address. Device A has endpoints (Mullvad WireGuard and at least one proxy, ideally
one with a rotating session) and slots. Device B may have its own, different config.
Note B's endpoints/slots first: they are replaced. Before starting slots on B, stop the same
slots on A: two devices using the same WireGuard key at once fight over the Mullvad session.

1. On A: Settings → **Export config**. Mode **Encrypted (recommended)**, enter a passphrase
   of at least 12 bytes in **Passphrase (12 to 1024 bytes)** and **Confirm passphrase**, and
   your **Current admin password**, then **Export**. Expect: a download named
   `sockswebui-<host>-<yyyymmdd-hhmmss>.json`; the log shows `config exported mode=encrypted`
   and no passphrase. Opening the file shows no endpoint names, hosts or keys (all in
   `payload` ciphertext).
2. On A, try again with a wrong admin password. Expect: field error "is incorrect", no download.
3. Copy the file to a machine that can reach B's UI. On B: Settings → **Import config**,
   choose the file under **Export file (.json, drop also accepted)**. Expect: the
   **Export passphrase** field appears (the file is encrypted).
4. Wrong passphrase: enter a wrong passphrase and B's **Current admin password**, **Review**,
   then confirm. Expect: "could not decrypt: wrong passphrase or damaged file"; B's endpoints,
   slots and running state are unchanged and no new `config.json.pre-import-*` file appears in
   B's data dir (`/var/lib/sockswebui`, or `/etc/sockswebui` on OpenWrt).
5. Modified file (optional): change one character inside `payload` in a copy of the file and
   import it with the right passphrase. Expect: the same error and nothing changes.
6. Right passphrase: import the original file with the right passphrase. The confirm step
   says "This replaces all endpoints and slots on this device. All slots will stop and stay
   stopped until you start them. A backup of the current configuration is kept."
   Confirm. Expect:
   - "Import complete", the endpoint and slot counts match A, "All slots are stopped.",
     and no "Secrets to re-enter" list;
   - every slot on B is `STOPPED` (B's previous slots were stopped first, imported slots are
     saved disabled);
   - B's data dir has a new `config.json.pre-import-<yyyymmddThhmmssZ>` (mode 0600; it holds
     B's previous config with plaintext secrets, see docs/INSTALL.md for restore);
   - B still logs in with **B's** admin password (A's is refused) and is still on B's own UI
     address and port;
   - every endpoint and slot from A is present with the same settings; proxies show
     `password set`, WireGuard endpoints and slot SOCKS auth keep their secrets, and a
     rotating proxy shows the same `session ID` as on A.
7. Press **Start all** (or start slots one by one). Expect: each slot reaches `UP` with the
   same exit IP as on A (same `session ID` = same sticky IP, if the provider still holds it).
8. UI port conflict (optional): restart B with `--ui-addr <B-addr>:<port of one of A's slots>`
   and import the file again. Expect: a field error on that slot's port; nothing changes and
   no new backup is written. Restore B's normal `--ui-addr` afterwards.

Result (date, A arch → B arch, counts, wrong passphrase unchanged y/n, backup file name): ______

## Export without secrets, then re-entering the secrets

Same two devices. This export is for sharing a layout; keys and passwords stay on A.

1. On A: Settings → **Export config**, mode **Without secrets**. The dialog says "Keys and
   passwords are left out. You will need to re-enter them after import." Enter the
   **Current admin password**, **Export**. Expect: a plain JSON file; search it for your
   private key, proxy password and SOCKS password: none appears; objects that had them carry
   `"omitted": [...]` naming the fields.
2. On B: **Import config** with this file. Expect: no passphrase field. Confirm. Expect: the
   counts, "All slots are stopped.", a `config.json.pre-import-*` backup as above, and a
   "Secrets to re-enter" list linking each endpoint and slot (slots blocked only by an
   endpoint say "blocked by endpoint <id>").
3. Start a slot before re-entering anything (Slots → **Start**). Expect: refused with 409
   "secrets missing: re-enter the secrets omitted by the config import before enabling this
   slot"; the slot stays `STOPPED` and opens no listener.
4. Re-enter the secrets:
   - Proxy password: Endpoints → **Edit** on the proxy, type the whole password into
     **Password** (including `{session}` if the template was in the password), **Save proxy**.
     Expect: `password set`; the `session ID` is unchanged.
   - Slot SOCKS password: open the slot, under **SOCKS authentication** type the **Password**,
     **Save slot**.
   - WireGuard private key (and preshared key, if the `.conf` had one): Endpoints → follow the
     "Secrets to re-enter" link, or press **Edit keys** on the WireGuard row (rows missing the key say
     "private key missing"). Paste the **Private key** (and the **Preshared key** if the `.conf` had one;
     both are in your Mullvad `.conf`), **Save keys**. Expect: "Keys saved", the inputs are empty, and the
     row no longer says "private key missing". Re-dropping the `.conf` is `skipped-duplicate`; use the editor.
5. Expect: once all its secrets are re-entered, an endpoint or slot no longer appears as missing
   (the "private key missing" and "password not set" hints are gone).
6. Start each slot again (Slots → **Start**). Expect: no 409; the slot reaches `UP` with the
   same exit IP as the matching slot on A.

Result (date, secrets re-entered, 409 before y/n, all slots UP after y/n): ______

## Device-only criteria index

What automated tests cannot prove, where the manual proof lives in this file, and
which hermetic test (or Makefile target) covers the logic. The "SMOKE.md section" column
gives each heading exactly as it appears above.

| Criterion | Why automated tests cannot prove it | SMOKE.md section that proves it | Hermetic test that covers the logic |
|-----------|------------------------------|---------------------------------|-------------------------------------|
| a LAN client browses via `socks5://<device>:<port>`, exit IP == last hop | needs real Mullvad relays, a real upstream proxy and a real LAN client on a real network | Real Mullvad configs; Real upstream proxy | `TestE2ECriterion1` (internal/supervisor/e2e_test.go) |
| four slots at once with four different routes | needs four real Mullvad keys / real proxies and real LAN clients | Four slots at once | `TestManagerFourSlots` (internal/supervisor/manager_test.go) |
| killing a hop stops traffic within one health interval, no direct fallback unless `allow-direct` | needs a real cable pulled or a real hop dropped at a real router, with the device's own WAN route present | Kill switch: pull the cable | `TestKillSwitch` (internal/supervisor/killswitch_test.go) |
| linux/arm64, arm v7, amd64 with < 64 MB RSS, 4 slots idle | needs real arm64 / armv7 hardware; CI only measures the amd64 `-tags testrig` build | Four slots at once | `make rss-check` (scripts/rss-check.sh) |
| Throughput: userspace WireGuard on a Pi, ~100-300 Mbps | needs a real Pi CPU, a real Mullvad relay and a real LAN link | Throughput | `TestWGPeerEcho` (internal/testrig/wgpeer_test.go; data through userspace WireGuard, correctness only, not speed) |
| Release binary (no `-tags testrig`) running 4 configured slots on arm64 and armv7 | `make release` cross-compiles, but no arm64 / armv7 device exists available to run it with real endpoints | Four slots at once | `TestNoTestrigInRelease` (cmd/sockswebui/devrig_test.go); `TestManagerFourSlots` |
| New IP gives the upstream a new session ID, every slot using the endpoint is rebuilt and reaches `UP`; rebuilds, backoff and restarts keep the ID | needs a real rotating proxy that maps session IDs to exit IPs, and each New IP is billed per IP | Rotate a proxy session (New IP) | `TestRotateRebuildsAndNewIDReachesUpstream`, `TestRotateBackoffRebuildKeepsID` (internal/supervisor/rotate_test.go) |
| encrypted export on one device, imported on a second, reproduces endpoints and slots (all stopped), keeps the second device's admin login and UI address; wrong passphrase or modified file changes nothing; UI port conflict is a field error | needs two real devices with their own admin logins and UI addresses, and real Mullvad / proxy secrets to carry across | Encrypted export on device A, import on device B; Export without secrets, then re-entering the secrets | `TestExportEncryptedRoundTrip` (internal/config/export_test.go, internal/api/config_export_test.go), `TestImportWrongPassword` (internal/api/config_import_test.go), `TestImportNothingChanges/UIPortConflict` (internal/api/config_import_nochange_test.go) |
| First login on a slow device (argon2id timing) | argon2id cost is only meaningful on the real small CPU | First login (argon2) | `TestLoginCorrectPassword` (internal/api/login_test.go) |

Run every row's SMOKE.md section with the release binary from `make release` (not a
`-tags testrig` build) on each arch you ship, and record arch and results in the tables above.

See also the "Security notes" section of [docs/INSTALL.md](INSTALL.md) for what the `config.json.bad-*` files hold and how to dispose of them.
