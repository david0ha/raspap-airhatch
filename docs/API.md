# raspap-airhatch: interfaces and file contracts

The fixed facts that the daemon, the plugin, the layer and the tests share:
names, paths, the socket API, the configuration file, the command line and
the exact content of every file airhatchd writes at runtime. The reasoning
behind them is in [DESIGN.md](DESIGN.md); every change to meta-raspap's
RaspAP integration is in [integration.md](integration.md).

---

## 1. Names

| Item | Value |
|---|---|
| DISTRO_FEATURE | `airhatch` (alongside meta-raspap's `raspap`) |
| Layer | `meta-raspap-airhatch`, collection `raspap-airhatch`, priority 7, `LAYERDEPENDS_raspap-airhatch = "core raspap networking-layer"`, `LAYERSERIES_COMPAT_raspap-airhatch = "wrynose"` |
| Recipe | `raspap-airhatch_git.bb` (PN `raspap-airhatch`) |
| Packages | `raspap-airhatch` (daemon, units, drop-ins, tmpfiles, dnsmasq include, sudoers drop-in, config); `raspap-airhatch-plugin` (webroot plugin); `packagegroup-raspap-airhatch` |
| Binary | `/usr/sbin/airhatchd` (static, the only binary) |
| Go module | `github.com/david0ha/raspap-airhatch/daemon`, `go 1.26`, no `require` (standard library only) |
| Units | `airhatchd.service` (enabled), `airhatch-firstboot.service` (enabled), `airhatch-wpa-supplicant.service` (not enabled) |
| Drop-ins | `/usr/lib/systemd/system/{hostapd.service.d,hostapd@.service.d,dhcpcd.service.d,raspapd.service.d}/50-airhatch.conf` |
| Plugin | `/var/www/html/plugins/Airhatch/`, PHP namespace `RaspAP\Plugins\Airhatch`, class `Airhatch`, page `/plugin__Airhatch` |

The interface is always `wlan0` and the captive portal port always 8099;
they are constants, not configuration.

## 2. Paths

| Path | Owner / mode | Written by | Meaning |
|---|---|---|---|
| `/run/airhatch/` | root:root 0755 | tmpfiles | runtime directory; never removed by a unit (no `RuntimeDirectory=`) |
| `/run/airhatch/dnsmasq.conf` | root 0644 | tmpfiles at boot (`except-interface=wlan0`), then airhatchd | dnsmasq's wlan0 policy (§6.6) |
| `/run/airhatch/ap.lease` | root 0644, empty | airhatchd | hostapd may start only while it exists |
| `/run/airhatch/mode` | root 0644 | airhatchd | `client\n` or `ap\n`; absent means client |
| `/run/airhatch/dhcpcd.conf` | root 0644 | `airhatchd render-dhcpcd` | dhcpcd's rendered configuration (§6.3) |
| `/run/airhatch/api.sock` | root:www-data 0660 | airhatchd | plugin and CLI API (§3) |
| `/run/airhatch/wpa_ctrl_<pid>-<n>` | root | airhatchd | its own client sockets for wpa_supplicant and hostapd |
| `/run/airhatch/firstboot.lock` | root | `airhatchd firstboot` | flock |
| `/etc/airhatch/airhatchd.conf` | root 0644, CONFFILE | package | daemon configuration (§4) |
| `/etc/airhatch/firstboot.done` | root 0644 | `airhatchd firstboot` | marker, no secrets |
| `/boot/raspap-airhatch.txt` | vfat | `airhatchd firstboot` | first-boot credential notice |
| `/var/run/wpa_supplicant/wlan0` | root:netdev 0770 | airhatchd (from an AP open until the next client start or daemon stop) | control-socket stand-in |
| `/tmp/raspap/`, `/tmp/raspap/nearby_wifi_stations_<mtime>` | www-data:www-data 0755 / 0644 | airhatchd (AP mode) | scan-cache seed |
| `/etc/dnsmasq.d/095_airhatch.conf` | root 0644 | package | `conf-file=/run/airhatch/dnsmasq.conf` |
| `/etc/sudoers.d/095_airhatch` | root 0440 (directory root 0750, as raspap-webgui ships it) | package | refuses www-data's `wpa_supplicant` starts (integration.md O8) |
| `/etc/raspap/hostapd/servicestart.sh` | root:root 0750 | `raspap-webgui` (bbappend) | shim → `airhatchd servicestart "$@"` |
| `/usr/lib/tmpfiles.d/raspap-airhatch.conf` | root 0644 | package | creates `/run/airhatch` and its dnsmasq file |

RaspAP files that airhatchd reads and never writes (except at first boot):
`/etc/hostapd/hostapd.conf`, `/etc/raspap/hostapd.ini` (optional),
`/etc/dhcpcd.conf`, `/etc/wpa_supplicant/wpa_supplicant.conf`,
`/etc/lighttpd/lighttpd.conf`, `/etc/raspap/raspap.auth` (owner, mode and the
admin hash, which is checked against the default password, DESIGN.md §5.1).

## 3. Socket API

Transport: unix stream socket `/run/airhatch/api.sock`. The client sends one
JSON object followed by `\n` (at most 4096 bytes) and reads one JSON object
followed by `\n`; the server then closes the connection. The server's read
timeout is 2 s. The peer uid (`SO_PEERCRED`) must be 0 or 33 (www-data);
`servicestart` requires 0. Unknown fields in a request are rejected
(`bad_request`).

Requests:

| Request | Callers (uid) | Effect |
|---|---|---|
| `{"op":"status"}` | 0, 33 | none |
| `{"op":"open_ap"}` | 0, 33 | override = `ap`; the AP open is scheduled (from client states after `handoff_delay`, so the reply reaches the browser) |
| `{"op":"retry_client"}` | 0, 33 | DESIGN.md §3.3, "API `retry_client`" |
| `{"op":"auto"}` | 0, 33 | override = `auto`, hold cleared |
| `{"op":"servicestart","args":["--interface","wlan0","--seconds","1"]}` | 0 | DESIGN.md §7; `args` is the shim's argv verbatim |

Replies:

```json
{"ok":true,"message":"…","status":{…}}         // status, open_ap, retry_client, auto ("message" is "" for status)
{"ok":true,"lines":["airhatch: …"]}             // servicestart
{"ok":false,"error":{"code":"…","message":"…"}}
```

Error codes: `bad_request` (malformed JSON, unknown field, wrong type, missing
`op`), `unknown_op`, `forbidden`, `no_saved_network` (`retry_client` with no
saved network), `internal`.

Status object (every key is always present; times are RFC 3339 UTC strings
or `null`):

```json
{
  "version": 1,
  "state": "init|connecting|connected|lost|ap|handoff|ap_failed|held",
  "mode": "client|ap|none",
  "operation": {"name": "open_ap|close_ap|client_attempt", "started_at": "…"},
  "override": "auto|ap|off",
  "hold_until": null,
  "saved_networks": 2,
  "client": {"ssid": "Home", "ipv4": "192.168.1.23/24"},
  "ap": {"ssid": "RaspAP-1A2B", "ipv4": "10.3.141.1/24", "stations": 1, "captive": true},
  "timers": {"attempt_ends_at": null, "grace_ends_at": null, "retry_at": "…",
             "busy_retry_at": "…", "handoff_at": null, "next_open_at": null, "backoff_s": 5},
  "config": {"boot_timeout_s": 45, "connect_timeout_s": 45, "disconnect_grace_s": 60,
             "retry_interval_s": 300, "retry_interval_busy_s": 900,
             "handoff_delay_s": 5, "off_hold_s": 900},
  "last_error": {"code": "wrong_password", "message": "The password for \"Home\" was rejected.", "at": "…"},
  "notices": ["dnsmasq_stopped_outside_airhatch"],
  "credentials_file": "/boot/raspap-airhatch.txt"
}
```

`operation`, `client`, `ap` and `last_error` are `null` when not applicable;
`notices` is `[]` when empty; `credentials_file` is the path while the file
exists, else `null`. `mode` is `client` for connecting, connected and lost,
`ap` for ap and handoff, and `none` otherwise.

Closed vocabulary (the daemon defines the English messages; the plugin shows
`message` and may switch on `code`):

- `last_error.code`: `wrong_password`, `network_not_found`, `no_dhcp_lease`,
  `connect_timeout`, `no_saved_network`, `wpa_conf_unusable`,
  `unsupported_raspap_mode`, `default_ap_passphrase`, `default_admin_login`,
  `no_ap_address`, `ap_start_failed`, `ap_service_outside_daemon`.
- `notices`: `dnsmasq_stopped_outside_airhatch`.

The plugin's test fixtures (`plugin/tests/fixtures/*.json`) are status
replies; a Go test keeps their key paths equal to those of the daemon's
`Status`.

## 4. Daemon configuration (`/etc/airhatch/airhatchd.conf`)

Format: UTF-8 lines; `#` starts a comment line; blank lines are ignored;
otherwise `key=value` with no spaces around `=`. Durations use Go
`time.ParseDuration` syntax; booleans are `true` or `false`. Unknown keys,
duplicate keys, bad values or a value outside the bounds below are errors:
`airhatchd run` exits 2 with a message naming the line. A missing file means
all defaults. The shipped file lists every key with its default, uncommented.

| Key | Default | Bounds | Meaning |
|---|---|---|---|
| `tick` | `1s` | 100ms-10s | observation period |
| `observe_timeout` | `5s` | 1s-30s | timeout of one observation command |
| `boot_timeout` | `45s` | 10s-10m | first client attempt after boot (the attempt's operation deadline is this plus 2 min) |
| `connect_timeout` | `45s` | 10s-10m | every later client attempt, association plus DHCP (likewise) |
| `assoc_timeout` | `30s` | 5s-`connect_timeout` | part of an attempt: time to `wpa_state=COMPLETED` |
| `disconnect_grace` | `60s` | 0s-1h | lost connection → AP; rides out a router reboot |
| `retry_interval` | `5m` | 30s-24h | in AP mode with no stations, retry the client |
| `retry_interval_busy` | `15m` | ≥ `retry_interval` | in AP mode with stations, retry anyway |
| `handoff_delay` | `5s` | 0s-1m | lets the page that triggered a hand-off reach the phone before the AP drops |
| `off_hold` | `15m` | 0s-24h | how long a user's "Stop hotspot" keeps the AP closed when no client connects |
| `backoff_min` | `5s` | 1s-`backoff_max` | first wait after a failed or refused AP open, then doubled |
| `backoff_max` | `2m` | ≤ 1h | longest such wait |
| `ap_step_timeout` | `10s` | 1s-1m | each wait inside an AP open (dnsmasq active, hostapd ENABLED) |
| `scan_timeout` | `10s` | 0s-1m | pre-AP scan (0 disables it) |
| `captive` | `true` | | captive portal on the fallback AP |

## 5. Command line (`/usr/sbin/airhatchd <command>`)

| Command | Run by | Behaviour | Exit |
|---|---|---|---|
| `run` | systemd | the supervisor | 0 on SIGTERM, 2 on a configuration error |
| `cleanup` | `airhatchd.service` `ExecStopPost` | the stale cleanup of DESIGN.md §5.4 with default timers (it needs none), 2 min limit | 0; 1 when the reap, `close_ap` or the table delete failed |
| `firstboot` | systemd | DESIGN.md §8 | 0 done or nothing to do, 1 error |
| `render-dhcpcd` | dhcpcd drop-in | reads `/run/airhatch/mode` and `/etc/dhcpcd.conf`, writes `/run/airhatch/dhcpcd.conf` atomically; never fails on input (§6.3) | 0; 1 only on a write failure |
| `status` | root | prints the status object as indented JSON | 0; 1 daemon unreachable |
| `open-ap`, `retry`, `auto` | root | the API op; prints `message` | 0; 1 error |
| `servicestart [args…]` | shim (root) | sends `servicestart`, prints each line to stdout | 0; 1 with `airhatch: airhatchd is not running.` when unreachable |

`servicestart` parses upstream `servicestart.sh`'s grammar
(`-i`/`--interface X`, `-s`/`--seconds N`, `-a`/`--action stop`; unknown
arguments are ignored as upstream does). Its reply lines (exact text; the
smoke test asserts them):

| Situation | Line |
|---|---|
| `--interface` not `wlan0` (`br0`, `uap0`, …) | `airhatch: RaspAP bridged and AP+client modes are not supported by raspap-airhatch; nothing changed.` |
| `--action stop` | `airhatch: hotspot off; it stays off for <off_hold> unless you start it again.` |
| start in `connecting`, `connected` or `lost` | `airhatch: Wi-Fi client is active; the hotspot opens automatically when Wi-Fi is unavailable.` |
| start in `ap` | `airhatch: restarting the hotspot with the current settings.` |
| start in `handoff` | `airhatch: a Wi-Fi hand-off is in progress; try again in a minute.` |
| start in `init`, `ap_failed` or `held` | `airhatch: opening the hotspot.` |

`<off_hold>` is formatted like Go's `time.Duration.String()` without trailing
zero units (`15m`).

## 6. Files written at runtime

### 6.1 `/run/airhatch/mode`

`client\n` or `ap\n`.

### 6.2 `/run/airhatch/ap.lease`

An empty file.

### 6.3 `/run/airhatch/dhcpcd.conf`

Rendered from RaspAP's `/etc/dhcpcd.conf`. A block starts at a line whose
first word is `interface`, `profile` or `ssid` and runs to the next such
line. The global section is everything before the first block.

- The first line is always
  `# Rendered by airhatchd render-dhcpcd from /etc/dhcpcd.conf (mode <m>). Do not edit.`
- **client**: the global section verbatim; then every block verbatim except
  `interface wlan0` blocks, which are dropped; then
  `interface wlan0\nnohook wpa_supplicant\n`, plus the dropped block's
  `metric` line if it had one.
- **ap**: the global section verbatim plus `denyinterfaces wlan0`; then every
  block verbatim except `interface wlan0` blocks.
- A missing or unreadable `/etc/dhcpcd.conf` renders the header plus the
  mode's wlan0 lines alone. The render never fails.

Golden tests cover upstream's `config/dhcpcd.conf` in both modes and a
variant with an eth0 block, a `profile static_wlan0` with `fallback`, a wlan0
`metric` and two wlan0 blocks (`daemon/testdata/dhcpcd/`).

### 6.4 nft batches

The AP batch is DESIGN.md §6.2 verbatim (golden file
`daemon/testdata/captive/nft-open.golden`); the close batch is
`add table inet airhatch\ndelete table inet airhatch\n`.

### 6.5 Scan cache

`/tmp/raspap/nearby_wifi_stations_<mtime>` holds the raw `iw dev wlan0 scan`
output.

### 6.6 `/run/airhatch/dnsmasq.conf`

Client mode:

```
# airhatch: client
except-interface=wlan0
```

AP mode:

```
# airhatch: ap
address=/#/<ap-ip>
except-interface=lo
dhcp-option=tag:wlan0,114,"http://<ap-ip>/captive-portal/api"
```

With `captive=false` the AP content is `# airhatch: ap\n` alone.

## 7. Plugin

### 7.1 `Airhatch/Airhatch.php`

- `class Airhatch implements \RaspAP\Plugins\PluginInterface` in namespace
  `RaspAP\Plugins\Airhatch`.
- The constructor `(string $pluginPath, string $pluginName)` stores both and
  does no I/O: RaspAP constructs plugins once per request for the sidebar
  and once more for the page.
- `initialize()` adds the sidebar item "Wi-Fi fallback" (`fas fa-life-ring`,
  priority 78) for the page `plugin__Airhatch`.
- `handlePageAction()` handles pages starting with `/plugin__Airhatch`. It
  first checks the login itself (`HTTPAuth::isLogged`, when
  `RASPI_AUTH_ENABLED`); logged out, it renders only a login hint. On POST,
  and only when RaspAP's monitor mode is off, the buttons
  `airhatch_open_ap`, `airhatch_retry_client` and `airhatch_auto` send
  `open_ap`, `retry_client` and `auto`; the reply's message goes to a RaspAP
  status message. Then it asks for `status` and renders the page. RaspAP's
  CSRF check runs before plugins; the forms carry RaspAP's CSRF field.

### 7.2 `Airhatch/DaemonClient.php`

`DaemonClient::request(array $req): array` writes one JSON line to
`/run/airhatch/api.sock` (2 s timeout) and returns the decoded reply. When
the daemon cannot be asked it returns
`['ok' => false, 'error' => ['code' => 'unreachable', …]]`; when it answers
with something that is not a reply,
`['ok' => false, 'error' => ['code' => 'bad_reply', …]]`. These are the only
client-side codes; no exception escapes.

### 7.3 `Airhatch/templates/main.php`

A RaspAP card: state and mode; SSID and IPv4 (client) or AP SSID, address,
station count and captive portal on or off (AP); override and hold; the last
error and its time; the timers that are set, as local times with a small
countdown; notices; the hand-off warning; the reminder to delete
`/boot/raspap-airhatch.txt` while it exists; the three buttons. An
unreachable daemon shows an error box and no buttons. Every value is
escaped with `htmlspecialchars`, every string goes through `_()`.

### 7.4 Tests (`plugin/tests/`)

`run.sh` runs `php -l` on every PHP file, then `harness.php` in RaspAP's
normal and monitor modes. The harness extracts `PluginInterface`, `Sidebar`,
`StatusMessage`, the CSRF classes and `HTTPAuth` from upstream RaspAP 3.5.7
(`8a201504`) with `git show`, constructs `Airhatch` twice with the socket
pointing nowhere (no I/O during construction), checks the sidebar item, and
renders the page against `fake-daemon.php`, a unix-socket server answering
from `fixtures/*.json` (client connected, connecting, AP with stations and an
error, hand-off, held, malformed reply, unreachable). It asserts key strings
in the HTML, that each POST sends the right `op`, that values are escaped,
and that a logged-out request sees only the login hint.
