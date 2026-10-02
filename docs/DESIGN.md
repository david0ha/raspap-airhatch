# raspap-airhatch: design

raspap-airhatch is an optional add-on to
[meta-raspap](https://github.com/david0ha/meta-raspap) that changes RaspAP's
Wi-Fi behaviour to "client first, hotspot only as a fallback". This document
describes how it works and why. [API.md](API.md) holds the exact interfaces
and file contents, and [integration.md](integration.md) lists every change to
meta-raspap's RaspAP integration with its evidence.

Evidence keys: `U:<path>:<line>` is upstream RaspAP at tag 3.5.7
(`8a201504`); `img:<path>` is a meta-raspap `core-image-base` rootfs for
`raspberrypi-cm5-io-board`; `MR:<path>` is
[meta-raspap](https://github.com/david0ha/meta-raspap); `src:<pkg>/<path>`
is the unpacked source of that package in the Yocto `wrynose` build.
**HW** marks what only CM5 hardware (CYW43455/brcmfmac) can show.

---

## Goals and constraints

1. On boot the device first tries to join its saved Wi-Fi networks as a
   normal client.
2. If no saved network connects (none saved, a wrong password, a network
   that is gone), it opens its own hotspot with a captive portal that sends
   the user to RaspAP's login and then its WiFi client page.
3. Once a network is saved and connects, the hotspot closes. If the
   connection drops and does not come back within a grace period, the
   hotspot opens again.

Constraints:

- **Separate layer, double gate.** Every bbappend is gated on
  `DISTRO_FEATURES` containing `airhatch`, and every new recipe has
  `REQUIRED_DISTRO_FEATURES = "raspap airhatch ..."`. Adding the layer
  without the feature changes nothing in the image; yocto-check-layer's
  `test_signatures` and the no-change proof (§11.5) show it.
- **RaspAP stays unpatched.** No patch or sed of RaspAP's PHP, JS or
  templates. RaspAP's own files stay the single source of the Wi-Fi
  settings: `/etc/hostapd/hostapd.conf` (hotspot SSID and passphrase),
  `/etc/wpa_supplicant/wpa_supplicant.conf` (saved networks) and its dnsmasq
  and dhcpcd settings. The add-on keeps no parallel copy of an SSID or a
  password. Where RaspAP's boot or service integration conflicts with the
  goals, the add-on overrides installer-level integration only (units,
  drop-ins, `servicestart.sh`, the dhcpcd render, and additive drop-ins that
  narrow sudoers); each override is listed in integration.md.
- **One owner of wlan0.** `airhatchd` (Go, static, standard library only)
  is the only component that switches wlan0 between client and hotspot.
  RaspAP's hotspot buttons are routed to it.
- **Hard constraint.** In client mode the device never runs a DHCP server or
  a hostapd on wlan0 toward the foreign network, even while airhatchd is
  not running.
- **No public defaults on the hotspot.** RaspAP ships `admin`/`secret` and
  the passphrase `ChangeMe`; the hotspot never opens with them.
- Bluetooth is out of scope.

---

## 1. Decisions

### 1.1 Design decisions

- **D1** `hostapd.service` stays enabled and unmodified; a drop-in
  `ConditionPathExists=/run/airhatch/ap.lease` makes every start a no-op unless
  airhatchd declared AP mode. No hostapd bbappend.
- **D2** `raspapd.service` stays enabled; a drop-in
  `ConditionPathExists=!/usr/sbin/airhatchd` skips it. No `SYSTEMD_SERVICE`
  change.
- **D3** The captive HTTP layer is a small Go listener inside airhatchd that
  exists only while the AP is up (redirect, RFC 8908, reverse proxy to
  lighttpd). lighttpd's config and unit are never touched or restarted.
- **D4** RaspAP's bridged and AP+client topologies are rejected with a public
  error. There is no "passive" regime that replays upstream's
  `servicestart.sh`.
- **D5** First boot writes only RaspAP's own files plus the one `/boot`
  notice; the marker holds no secrets.
- **D6** The hand-off trigger is a *new or changed* network in
  `wpa_supplicant.conf` (or "Connect"); a delete-only save does not drop the AP.
- **D7** A busy retry interval (15 min) so a phone that auto-joins the AP
  cannot keep the device in AP mode forever.
- **D8** One gated bbappend (`raspap-webgui`, `servicestart.sh` replaced).
- **D9** dnsmasq keeps running in both modes. Its fence is content, not a
  unit gate: `/etc/dnsmasq.d/095_airhatch.conf` includes
  `/run/airhatch/dnsmasq.conf`, which tmpfiles creates at boot as
  `except-interface=wlan0`. A unit gate would break RaspAP's dnsmasq reload
  and any eth0 DHCP the user configured; an `ExecStart` override is not
  needed, because `090_raspap.conf` already reads the whole directory.
- **D10** The AP opens in a fixed order: client stop → address → nft and
  DNS → dnsmasq → hostapd. The AP-mode dhcpcd render denies wlan0
  (`denyinterfaces wlan0`) and airhatchd sets the AP address itself.
- **D11** `hostapd@.service` gets the same lease fence (RaspAP installs it,
  `MR:recipes-webadmin/raspap/raspap-webgui_3.5.7.bb`).
- **D12** "Stop hotspot" is honoured: AP closed, client tried, and if that fails
  the AP stays off for `off_hold` (15 min). "Start hotspot" while the device is
  a working client does not open the AP.
- **D13** The plugin API is one JSON request line, one JSON reply line, over a
  unix stream socket (no HTTP parsing in PHP or Go).
- **D14** Only airhatchd starts `wpa_supplicant` on wlan0: a sudoers drop-in
  refuses the starts RaspAP's WiFi page and "Reinitialize" make
  (integration.md O8). A stray started some other way is reaped, and in AP
  mode treated as "connect now"; a failed reap stops the operation.
- **D15** The AP is also refused while RaspAP's login is its public
  `admin`/`secret` as the web server sees it: `/etc/raspap/raspap.auth` must
  be a regular file www-data can read, with a user and a hash, and the hash
  must not verify `secret`. Otherwise RaspAP falls back to (or holds) the
  default (`U:src/RaspAP/Auth/HTTPAuth.php:28-31,102-110`). The hash is
  checked with RaspAP's own `password_verify` through the image's php-cli,
  password and hash on stdin, as first boot hashes it; this keeps the daemon
  stdlib-only and matches every algorithm PHP accepts. The passphrase check
  refuses `ChangeMe` and other placeholders (`changeme`, `secret`,
  `password`, `12345678`, any case) and an empty passphrase.
- **D16** The scan-cache seed relies on the verified upstream fact that
  `writeCache` writes only when `/tmp/raspap` does not exist yet
  (`U:includes/functions.php:455-462`), so RaspAP never overwrites the seed.
- **D17** Image tests in a chroot and in Docker use the image's real
  `wpa_cli`, `dnsmasq --test` and RaspAP's real `WiFiManager` under the
  image's `php`, against the target airhatchd binary.
- **D18** One Go binary, `/usr/sbin/airhatchd`, with subcommands (`run`,
  `firstboot`, `render-dhcpcd`, and the control commands). No separate
  control tool: half the image size, one recipe output.
- **D19** One recipe (`raspap-airhatch`) builds two packages (`raspap-airhatch`,
  `raspap-airhatch-plugin`) from one pinned git revision of this repository.
- **D20** airhatchd never restarts a working client on its own restart: if the
  supplicant unit is up and connected at start, it adopts it.
- **D21** The station count comes from hostapd's own control socket
  (`STATUS` → `num_sta[0]=`, `src:hostapd/src/ap/ctrl_iface_ap.c:1057`), so no
  `iw station dump` parsing.

### 1.2 Rejected alternatives

- A lighttpd login-bypass guard. RaspAP 3.5.7 lets any request whose URI
  contains `/login` through `HTTPAuth::authenticate`
  (`U:src/RaspAP/Auth/HTTPAuth.php:53-58`). A lighttpd rule cannot cover every
  variant and would change upstream behaviour; it is recorded as an upstream
  issue in `integration.md` §4. The plugin checks the session itself
  (`HTTPAuth::isLogged`) before it talks to the daemon, so its status and
  controls stay behind the login. On the fallback AP the audience is limited to
  holders of the per-device passphrase.
- `dhcpcd -n` instead of a dhcpcd restart: undocumented for a newly denied
  interface. The restart matches what RaspAP's own `servicestart.sh` does on
  every hotspot restart (`U:installers/servicestart.sh:63,125`).
- Disabling `hostapd.service` and `raspapd.service`, or adding lighttpd
  configuration for the portal: more overrides of RaspAP's integration than
  the drop-ins and the Go listener above, and meta-raspap's enabled-units
  check would no longer pass.
- A copy of the generated credentials in `/etc`: a parallel store of
  secrets beside RaspAP's own files.

### 1.3 Verified upstream facts

| Claim | Evidence |
|---|---|
| The WiFi page starts a real `wpa_supplicant` on the AP interface when `list_networks` fails | `U:src/RaspAP/Networking/Hotspot/WiFiManager.php:398-411`, called on every page load from `U:includes/configure_client.php:21` |
| PHP adds networks missing from `list_networks` through `add_network`/`set_network`/`enable_network` | `WiFiManager.php:500-540` |
| Save = `sudo cp /tmp/wifidata …` then `wpa_cli reconfigure`; Connect = `select_network <id>` | `WiFiManager.php:834-836`, `:664` |
| Reinitialize = `rm -f /var/run/wpa_supplicant/<if>` then `wpa_supplicant -i <if> -c … -B` | `WiFiManager.php:325-339`; sudoers allows both (`U:installers/raspap.sudoers`) |
| wpa_cli binds its reply socket under `/tmp` (`wpa_ctrl_<pid>-<n>`) | `src:wpa-supplicant/…/src/common/wpa_ctrl.c:80-123` |
| Start/Restart hotspot = `sudo servicestart.sh --interface <if> --seconds 1`; Stop = `sudo /bin/systemctl stop hostapd.service` | `U:includes/hostapd.php:67-104` |
| sudoers allows `servicestart.sh` with any arguments, and `systemctl start/stop hostapd.service`, `start/stop/restart/reload dnsmasq.service` | `U:installers/raspap.sudoers` |
| `scanConfigDir` only deletes `.conf` files via `sudo rm`, which sudoers limits to `090_*.conf` | `U:src/RaspAP/Networking/Hotspot/DnsmasqManager.php:328-337`, sudoers `rm /etc/dnsmasq.d/090_*.conf` |
| `090_raspap.conf` contains `conf-dir=/etc/dnsmasq.d` | `U:config/090_raspap.conf` |
| dnsmasq tags each DHCP request with the receiving interface name | `src:dnsmasq/2.93/…/src/rfc2131.c:113-116` |
| A second dhcpcd only forwards its arguments to the running manager | `src:dhcpcd/…/src/dhcpcd.c:2424-2458` |
| Stock unit lines: `hostapd.service` Type=forking, `/usr/sbin/hostapd /etc/hostapd/hostapd.conf -P /run/hostapd.pid -B`; `dhcpcd.service` `ExecStart=/sbin/dhcpcd -q --nobackground`; `dnsmasq.service` `-7 /etc/dnsmasq.d --local-service`; `raspapd.service` `ExecStart=/bin/bash /etc/raspap/hostapd/servicestart.sh --seconds 1` | `img:/usr/lib/systemd/system/*.service` |
| Enabled in the meta-raspap image: dhcpcd, dnsmasq, hostapd, raspapd, raspap-network-activity@wlan0, raspap-iptables, lighttpd, php-fpm, NetworkManager (wlan0 unmanaged, D21) | `img:/etc/systemd/system/multi-user.target.wants/` |
| php-fpm runs with `PrivateTmp=no` | `MR:recipes-devtools/php/files/php-fpm-raspap.conf` |
| `raspap.auth` is rewritten by PHP as www-data with 3 lines (user, hash, non-privileged bit); absent file = `admin`/`secret` | `U:includes/admin.php:97-136`, `U:src/RaspAP/Auth/HTTPAuth.php:28-31,102-137` |
| Nearby list = `sudo iw dev <if> scan`, cached at `/tmp/raspap/nearby_wifi_stations_<filemtime(conf)>`; a cached string is split on `\n` | `WiFiManager.php:83-104,156-158`; `U:config/config.php:13` |
| `/boot` is the vfat first partition; `/run` is tmpfs | `img:/etc/fstab` |
| nftables 1.1.6 (meta-networking), `nft_redir`, `nft_nat`, `nft_reject_inet` in the image | `img:` manifest and module tree |

---

## 2. Ownership of wlan0

airhatchd is the only component that changes wlan0's mode. Everything RaspAP
or a user can start on wlan0 is either fenced (inert in the wrong mode) or
routed to airhatchd.

| Resource | Client mode | AP mode | Fence that holds even if airhatchd is dead |
|---|---|---|---|
| hostapd (`hostapd.service`, `hostapd@.service`) | not running | started by airhatchd | `ConditionPathExists=/run/airhatch/ap.lease`; `/run` is tmpfs, so the lease is absent at every boot |
| dnsmasq on wlan0 | `dnsmasq.service` runs, `except-interface=wlan0` | restarted with the AP content | `/run/airhatch/dnsmasq.conf` is created by tmpfiles as `except-interface=wlan0`; if it is missing, dnsmasq fails to start (no server at all) |
| wlan0 IPv4 | dhcpcd DHCP client, `nohook wpa_supplicant` | airhatchd `ip addr add` from RaspAP's `interface wlan0` static address; dhcpcd denies wlan0 | the rendered dhcpcd.conf never carries a static wlan0 address in client mode; a missing mode file renders client mode |
| wpa_supplicant on wlan0 | `airhatch-wpa-supplicant.service` (RaspAP's conf and control socket) | not running; airhatchd's stand-in answers on its control socket | not enabled; dhcpcd's hook is off for wlan0; strays are reaped |
| captive nft table, portal listener | absent | `table inet airhatch`, listener on `<ap-ip>:8099` | deleted at every daemon start; the listener dies with the daemon |
| `/run/airhatch/mode` | `client` | `ap` | absent means `client` |

**Boot path with the feature.**

1. `systemd-tmpfiles-setup` creates `/run/airhatch` and
   `/run/airhatch/dnsmasq.conf` (`except-interface=wlan0`).
2. `airhatch-firstboot.service` runs once (section 8), before hostapd, lighttpd,
   php-fpm and airhatchd.
3. `dhcpcd.service` starts: its `ExecStartPre` renders the client config (no
   mode file yet). wlan0 gets no static address and no supplicant.
4. `dnsmasq.service` starts and ignores wlan0.
5. `hostapd.service` is enabled but skipped: "condition check resulted in …
   being skipped".
6. `raspapd.service` is skipped (its condition sees `/usr/sbin/airhatchd`).
7. `airhatchd.service` starts (`After=dhcpcd.service dnsmasq.service
   airhatch-firstboot.service`): clean stale AP state, then client attempt
   (saved networks) or AP at once (none).

Without the feature none of these files exist, and meta-raspap's boot path is
unchanged.

---

## 3. State machine

### 3.1 States

| State | Mode | Meaning |
|---|---|---|
| `init` | – | Daemon start: stale cleanup, adopt a running client |
| `connecting` | client | Supplicant up; waiting for COMPLETED and an IPv4 address within the attempt budget |
| `connected` | client | Normal device |
| `lost` | client | Was connected; link or IPv4 gone; the supplicant keeps roaming |
| `ap` | ap | Fallback hotspot with captive portal |
| `handoff` | ap | A hand-off was requested; waiting `handoff_delay` |
| `ap_failed` | none | AP open failed or is refused (a public error says why); waiting for the backoff |
| `held` | none | The user stopped the hotspot and no client connected; waiting for `off_hold` |

`override ∈ {auto, ap, off}` is runtime-only and is `auto` after boot:

- `ap`: pinned by the plugin's "Open hotspot now". Periodic retries are
  suppressed. A hand-off trigger or "Back to automatic" clears it.
- `off`: set by "Stop hotspot" (RaspAP's button or `servicestart --action
  stop`), with `hold_until = now + off_hold`. A successful client connection,
  `hold_until`, "Back to automatic", or "Start hotspot" clears it.

Operations (`open_ap`, `close_ap`, `client_attempt`) run one at a time in the
supervisor's operation slot with a deadline of 2 min plus the operation's
attempt budget (`boot_timeout` or `connect_timeout`, up to 10 min), so a
configured budget always holds; rollback and cleanup use a
detached 30 s context. An observation starts only while the operation slot
is empty, and an operation starts only from an applied observation or a
finished operation, so the two never overlap. While a `close_ap` is
outstanding (`need_close`), nothing else starts: the next tick retries
`close_ap`, and the follow-up of the operation whose close failed (the boot
attempt, or the AP reopen after a failed attempt) runs once it succeeds.

### 3.2 Timers (`/etc/airhatch/airhatchd.conf`)

| Key | Default | Meaning |
|---|---|---|
| `tick` | 1s | Observation period |
| `observe_timeout` | 5s | Timeout of one observation command |
| `boot_timeout` | 45s | First client attempt after boot (saved networks only) |
| `connect_timeout` | 45s | Every later client attempt (association plus DHCP) |
| `assoc_timeout` | 30s | Part of an attempt: time to `wpa_state=COMPLETED` |
| `disconnect_grace` | 60s | `lost` → AP. Rides out a home-router reboot |
| `retry_interval` | 5m | In AP mode with 0 stations, retry the client |
| `retry_interval_busy` | 15m | In AP mode with stations, retry anyway |
| `handoff_delay` | 5s | Lets the page that triggered a hand-off reach the phone before the AP drops |
| `off_hold` | 15m | How long a user "Stop hotspot" keeps the AP closed when no client connects |
| `backoff_min` / `backoff_max` | 5s / 2m | After a failed or refused AP open, doubled |
| `ap_step_timeout` | 10s | Each wait inside AP open (dnsmasq active, hostapd ENABLED) |
| `scan_timeout` | 10s | Pre-AP scan |
| `captive` | true | Captive layer on the fallback AP |

### 3.3 Transitions

`saved` = number of `network={…}` blocks with an `ssid=` in RaspAP's
`wpa_supplicant.conf` that are not `disabled=1`.

| From | Event / guard | Action | To |
|---|---|---|---|
| `init` | always first | stale cleanup (5.4); read config | below |
| `init` | supplicant unit active and connected | adopt | `connected` |
| `init` | saved > 0 | `client_attempt(boot_timeout)` | `connecting` |
| `init` | saved = 0 | `open_ap` | `ap` / `ap_failed` |
| `connecting` | COMPLETED + IPv4 | clear `override=off`, reset backoff, clear `last_error` | `connected` |
| `connecting` | budget expired, override ≠ off | record error, stop client, `open_ap` | `ap` / `ap_failed` |
| `connecting` | budget expired, override = off | record error, stop client | `held` |
| `connected` | link or IPv4 lost | `down_since = now` | `lost` |
| `lost` | link and IPv4 back | – | `connected` |
| `lost` | `now - down_since ≥ disconnect_grace` and override ≠ off | stop client, `open_ap` | `ap` / `ap_failed` |
| any client state | API `open_ap` | override = ap, stop client, `open_ap` | `ap` |
| `ap` | hand-off trigger (3.4), saved > 0 | override ap → auto; `handoff_at = now + handoff_delay` | `handoff` |
| `ap` | `now ≥ retry_at`, stations = 0, saved > 0, override = auto | `close_ap`, `client_attempt(connect_timeout)` | `connecting` |
| `ap` | `now ≥ busy_retry_at`, saved > 0, override = auto | same | `connecting` |
| `ap` | hostapd inactive with `Result=success`, not stopped by airhatchd (RaspAP "Stop hotspot") | override = off, `hold_until`; `close_ap`; client attempt if saved > 0 | `connecting` / `held` |
| `ap` | hostapd inactive with `Result ≠ success` | `close_ap`, backoff | `ap_failed` |
| `ap` | servicestart start (RaspAP Start/Restart) | `close_ap` + `open_ap` (re-reads every RaspAP file) | `ap` / `ap_failed` |
| `handoff` | another trigger | re-arm `handoff_at` | `handoff` |
| `handoff` | `now ≥ handoff_at` | `close_ap`, `client_attempt(connect_timeout, preferred SSID)` | `connecting` |
| `ap_failed` | `now ≥ next_open_at` | saved > 0: `client_attempt(connect_timeout)`; else `open_ap` | `connecting` / `ap` / `ap_failed` |
| `ap_failed`, `held` | new or changed network saved | `client_attempt(connect_timeout)` | `connecting` |
| `held` | `now ≥ hold_until` or override → auto | saved > 0: `client_attempt`; else `open_ap` | `connecting` / `ap` |
| any | API `auto` | override = auto, clear hold | re-evaluate |
| any | API `retry_client` | AP states: hand-off now (no delay beyond `handoff_delay`); client states: `RECONFIGURE` and restart the attempt budget; `ap_failed`/`held`: client attempt | `handoff` / `connecting` |

Policy invariants (each a test):

- The AP never opens from `connected`, except on an explicit API `open_ap`.
- A hand-off never waits for stations = 0.
- The 5 min retry never fires while stations > 0; the 15 min one does.
- A hand-off trigger clears `override=ap`; a successful connection clears
  `override=off`.
- The AP never opens while a precondition fails (5.1); client mode is never
  blocked.

### 3.4 Hand-off triggers (AP and handoff states)

| Trigger | Source | Notes |
|---|---|---|
| New or changed network in `wpa_supplicant.conf` | RaspAP Save (`sudo cp`, `WiFiManager.php:834`), ssh edits | Detected by a 1 s stat (inode, size, mtime) + SHA-256 poll. Each network is keyed by its SSID and digested over its body; a trigger needs a key or digest absent from the previous set, so delete-only saves and `ensureWpaSupplicant`'s empty file do not trigger |
| `SELECT_NETWORK <id>` on the stand-in | RaspAP "Connect" (`WiFiManager.php:664`) | The SSID of `<id>` becomes the preferred network of the attempt |
| Stray `wpa_supplicant` on wlan0 | started outside RaspAP's sudo, which refuses it (O8) | Reaped first; then a trigger when saved > 0, else an AP reapply if hostapd lost the interface |
| API `retry_client` | Plugin "Retry Wi-Fi now" | |

`RECONFIGURE` alone is not a trigger: it always follows the `cp`, which the
file poll already sees.

---

## 4. RaspAP's WiFi page in AP mode: the control-socket stand-in

On one radio the WiFi page targets wlan0 itself. Without a control socket the
page starts `wpa_supplicant -B` on the AP interface on every load (1.3), which
is expected to drop the AP under the user's phone (**HW**). So while the AP is
up, airhatchd binds a unix datagram socket at `/var/run/wpa_supplicant/wlan0`
(directory 0770 root:netdev, socket 0770 root:netdev, like the real
supplicant's `ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev`,
`WiFiManager.php:593,771`) and answers RaspAP's commands:

| Command | Reply / effect |
|---|---|
| `PING` | `PONG` |
| `STATUS` | `wpa_state=DISCONNECTED\naddress=<wlan0 MAC>\n` |
| `LIST_NETWORKS` | `network id / ssid / bssid / flags` header, then `<id>\t<ssid>\tany\t<flags>` per network in file order (id = file index, as the supplicant numbers them; SSID encoded like wpa_supplicant's `wpa_ssid_txt`; flags `[DISABLED]` for `disabled=1`, else empty). The list is re-read from the file on every request |
| `ADD_NETWORK` | next free id; a scratch entry that is never written anywhere |
| `SET_NETWORK <id> <field> <value>` | `OK`. Only `ssid` is kept (scratch); a psk is discarded at once |
| `ENABLE_NETWORK`, `DISABLE_NETWORK`, `REMOVE_NETWORK`, `DISCONNECT`, `REASSOCIATE`, `SCAN`, `RECONFIGURE` | `OK` |
| `SELECT_NETWORK <id>` | `OK`, plus a hand-off trigger with that network's SSID |
| `SCAN_RESULTS` | the header line only |
| `TERMINATE` | `OK`; the stand-in keeps running |
| anything else | `UNKNOWN COMMAND` |

Replies go to the caller's bound path in `/tmp`, so airhatchd runs with
`PrivateTmp=no` (php-fpm already does). airhatchd's own client sockets for the
real supplicant and hostapd are bound in `/run/airhatch`. If the stand-in's
path disappears (Reinitialize's `rm -f`), it rebinds on the next tick.

**Nearby networks in AP mode.** Before the AP opens, airhatchd runs
`iw dev wlan0 scan` as a station and keeps the raw text. After the AP is up it
creates `/tmp/raspap` (www-data 0755) if absent and writes the text to
`/tmp/raspap/nearby_wifi_stations_<mtime of wpa_supplicant.conf>` (www-data
0644). It rewrites the key whenever the conf's mtime changes (first page load
creates the conf; every save changes it). Because `/tmp/raspap` then exists,
RaspAP's `writeCache` never writes (D16). "Rescan" deletes the key and scans
live; whether brcmfmac can scan while beaconing is **HW**.

---

## 5. Exact sequences

All commands go through `runner` (argv only, timeout, output cap, secrets on
stdin). Order tests assert the exact
argv sequence.

### 5.1 AP open (`open_ap`)

Preconditions, read fresh on every open (failure → `ap_failed` with the public
error, backoff):

| Check | Error code |
|---|---|
| `/etc/raspap/hostapd.ini` (optional) has neither `BridgedEnable = 1` nor `WifiAPEnable = 1` | `unsupported_raspap_mode` |
| `/etc/hostapd/hostapd.conf` has `interface=wlan0` | `unsupported_raspap_mode` |
| `wpa_passphrase` is not `ChangeMe`, another placeholder or empty (D15) | `default_ap_passphrase` |
| `/etc/raspap/raspap.auth` is a regular file www-data (33:33) can read through searchable directories, with a user line and a hash line | `default_admin_login` |
| The hash does not verify `secret` (php-cli `password_verify`; checked last, after the file checks; a failed check refuses) | `default_admin_login` |
| `/etc/dhcpcd.conf` has `static ip_address=<cidr>` in an `interface wlan0` block | `no_ap_address` |
| lighttpd port: `server.port` in `/etc/lighttpd/lighttpd.conf`, default 80 | – |

Steps:

1. **Client stop.** `systemctl stop airhatch-wpa-supplicant.service`; reap
   strays (a failed reap fails the open); bind the stand-in (§4) on the
   supplicant's socket path, or keep the one a close left bound, so nothing
   started during the open can bind it and take wlan0.
2. **Scan** (best effort, `scan_timeout`): `ip link set wlan0 up`,
   `iw dev wlan0 scan`; keep stdout.
3. **dhcpcd off wlan0.** Write `/run/airhatch/mode` = `ap`;
   `systemctl restart dhcpcd.service` (its `ExecStartPre` renders the AP config).
4. **Address.** `ip -4 addr flush dev wlan0`; `ip addr add <cidr> dev wlan0`.
5. **Captive** (if `captive`): `nft -f -` with the table of 6.2.
6. **DNS and DHCP.** Write `/run/airhatch/dnsmasq.conf` with the AP content
   (6.1, or `# airhatch: ap` alone when `captive=false`);
   `systemctl restart dnsmasq.service`; wait for `is-active`.
7. **hostapd.** Create `/run/airhatch/ap.lease`; `systemctl start
   hostapd.service`; wait for `state=ENABLED` on `/var/run/hostapd/wlan0`.
8. **Scan-cache seed.**
9. **Portal** listener on `<ap-ip>:8099` (if `captive`).
10. `systemctl start raspap-network-activity@wlan0.service` (best effort; what
    `servicestart.sh` does).
11. Publish `ap`, `ap_since`, `retry_at`, `busy_retry_at`.

Any failure in 1 (the reap or the stand-in bind) or 3-9 runs `close_ap` and leads to
`ap_failed`. A second
`open_ap` while `ap` is a no-op.

### 5.2 AP close (`close_ap`)

Each step is idempotent. Steps 2-5 are safety steps and each is verified; a
failure in step 2 ends the close there (the fences stay up), a failure in
3-5 lets the other steps run. Any failure makes `close_ap` fail
(`ErrCloseFailed`): the close stays pending in the radio, the policy retries
it on the next tick, a client attempt first finishes it and starts no
supplicant while it fails, and nothing is adopted. Once hostapd has stopped,
steps 3-6 run on a detached 30 s context, so a cancelled operation (SIGTERM)
cannot leave them half done.

1. Close the portal listener. The stand-in stays bound: RaspAP never finds
   the supplicant's socket path free while the AP side comes down. It is
   released right before the client attempt starts the supplicant (5.3), or
   at daemon stop.
2. Remove `/run/airhatch/ap.lease`; `systemctl stop hostapd.service
   'hostapd@*.service'` (recorded as stopped by airhatchd); then `systemctl
   list-units --state=active,activating,deactivating,reloading` for both must
   list none. The lease condition only gates starts, so a running `hostapd@`
   instance is stopped here too.
3. Write `/run/airhatch/dnsmasq.conf` = the client content;
   `systemctl restart dnsmasq.service`; wait for `is-active` (`ap_step_timeout`).
   A running dnsmasq that has loaded the fence is the proof, so a dnsmasq the
   user stopped in AP mode is started again with it.
4. `nft -f -` with `add table inet airhatch` + `delete table inet airhatch`
   (atomic: success means the table is gone).
5. `ip -4 addr flush dev wlan0`; the AP address must be gone from wlan0.
6. Write `/run/airhatch/mode` = `client`; `systemctl restart dhcpcd.service`
   (logged on failure; not a safety step).

DHCP and DNS leave wlan0 (step 3) before wlan0 can become a client (5.3). The
dhcpcd restart also bounces eth0, as RaspAP's own hotspot restart does;
RaspAP's dhcpcd.conf has `persistent` (`U:config/dhcpcd.conf:4`).

### 5.3 Client attempt (`client_attempt(budget, preferred)`)

1. Preconditions: saved > 0 (`no_saved_network`) and the conf has a
   `ctrl_interface=` naming `/var/run/wpa_supplicant` (`wpa_conf_unusable`).
2. If an AP is open or a close is pending: `close_ap`; if it fails, stop.
3. Reap strays (a failed reap ends the attempt before any start); release the
   stand-in; `systemctl start airhatch-wpa-supplicant.service` (if it is
   already active: `RECONFIGURE`).
4. Wait up to 5 s (100 ms steps) for `PING` → `PONG` on
   `/var/run/wpa_supplicant/wlan0`, then `ATTACH` on a second socket.
5. If `preferred` is set: `LIST_NETWORKS`, `SELECT_NETWORK <id>` (in memory
   only; never `SAVE_CONFIG`).
6. Poll every second: `STATUS` until `wpa_state=COMPLETED` (at most
   `assoc_timeout`), then a global IPv4 on wlan0 that is not the AP address
   (`net.InterfaceByName`), for the rest of the budget.
7. Success → `connected`, SSID and address recorded.
8. Failure → `systemctl stop airhatch-wpa-supplicant.service`, error from the
   events seen: `CTRL-EVENT-SSID-TEMP-DISABLED … reason=WRONG_KEY` →
   `wrong_password`; `CTRL-EVENT-NETWORK-NOT-FOUND` → `network_not_found`;
   COMPLETED without address → `no_dhcp_lease`; otherwise `connect_timeout`.

### 5.4 Daemon start and stop

**Start (stale cleanup)**, always before any decision: reap strays; remove a
dead stand-in socket; if any trace of an AP remains, run `close_ap`. Each
trace counts on its own: the lease, the mode file saying `ap`, a dhcpcd
running the AP render (`denyinterfaces wlan0` in `/run/airhatch/dhcpcd.conf`),
a `/run/airhatch/dnsmasq.conf` that is neither the client content nor the
boot line tmpfiles writes, the AP address on wlan0, and a running
`hostapd.service` or `hostapd@*.service` (an unreadable unit list counts as
running). Runtime state counts, so a close cut short after its file writes is
still finished. Without a trace, delete the nft table (a table left alone is
removed this way). A failed reap, close or delete fails the cleanup and
leaves the close pending: the policy runs `close_ap` before anything else,
and nothing is adopted until it succeeds. Then adopt a connected supplicant
(D20) or follow 3.3.

**Stop (SIGTERM):** cancel the running operation, run `close_ap` if an AP is
open or a close is pending (detached 30 s), release the stand-in, leave a
client connection untouched, exit 0.

**After any exit (fail closed):** `airhatchd.service` runs `airhatchd cleanup`
as `ExecStopPost`, after a clean stop and after a crash or SIGKILL alike. It
is the stale cleanup above, so an AP airhatchd left behind is closed while it
is down, and a connected client is left alone. `StartLimitIntervalSec=0`
keeps systemd restarting airhatchd.

### 5.5 Every tick

- Observe: supplicant `STATUS` (client states), wlan0 IPv4, hostapd
  `STATUS` (`state`, `num_sta[0]`), `systemctl show -p ActiveState,Result`
  for hostapd and dnsmasq, the conf poll, strays.
- Client states: if hostapd is active (impossible by construction), stop it
  and record `ap_service_outside_daemon`. If the supplicant's socket path is
  gone (Reinitialize), restart `airhatch-wpa-supplicant.service`.
- AP states: if dnsmasq is inactive, add the notice
  `dnsmasq_stopped_outside_airhatch` (no restart; the user stopped it). Keep
  the scan-cache key current. Rebind the stand-in if its path is gone.
- Observation errors are logged at most once a minute and never written to
  `last_error`.

### 5.6 Details

- `saved` and the hand-off digest come from parsing RaspAP's
  `wpa_supplicant.conf`: a network's key is its decoded SSID (`"…"`, `P"…"`
  or hex), its digest SHA-256 over its body lines with whitespace trimmed and
  comment lines (`#psk=…` from `wpa_passphrase`) excluded.
- A stray supplicant is a process whose `/proc/<pid>/comm` is
  `wpa_supplicant`, whose command line names wlan0 (`-i wlan0`, `-iwlan0`),
  and whose cgroup is not `airhatch-wpa-supplicant.service`. It gets SIGTERM,
  then SIGKILL after 2 s.
- hostapd's state comes from `STATUS` on `/var/run/hostapd/wlan0`
  (`state=`, `num_sta[0]=`).
- "Stopped by airhatchd" is a flag set in step 2 of the AP close and cleared
  on the next AP open.
- The AP close is safe to call when nothing is open.
- The portal listener binds `<ap-ip>:8099` only, never `0.0.0.0`.
- The stand-in removes a file at its path only if it is a socket nobody
  listens on (connect fails with `ECONNREFUSED`).

---

## 6. Captive portal

Active only between AP-open step 5 and AP-close step 4, and only with
`captive=true`.

### 6.1 DNS and DHCP option

`/run/airhatch/dnsmasq.conf` in AP mode:

```
# airhatch: ap
address=/#/<ap-ip>
except-interface=lo
dhcp-option=tag:wlan0,114,"http://<ap-ip>/captive-portal/api"
```

In client mode it is `except-interface=wlan0` (plus a comment line). It is
included by the shipped `/etc/dnsmasq.d/095_airhatch.conf`
(`conf-file=/run/airhatch/dnsmasq.conf`). `except-interface=lo` keeps the
wildcard off the device's own lookups. Accepted edge: an eth0
client that uses this dnsmasq for DNS also gets the wildcard while the AP is
up (setup state only).

### 6.2 nft (one atomic batch, `inet airhatch` only)

```
add table inet airhatch
delete table inet airhatch
table inet airhatch {
  chain prerouting { type nat hook prerouting priority dstnat; policy accept;
    iifname "wlan0" udp dport 53 redirect to :53
    iifname "wlan0" tcp dport 53 redirect to :53
    iifname "wlan0" tcp dport 80 redirect to :8099 }
  chain input { type filter hook input priority filter; policy accept;
    iifname "wlan0" tcp dport { 443, 853 } reject with tcp reset }
  chain forward { type filter hook forward priority filter; policy accept;
    iifname "wlan0" drop
    oifname "wlan0" drop }
}
```

The forward drops override RaspAP's MASQUERADE and `ip_forward=1` for wlan0
while the AP is up. RaspAP's own `ip nat` rules are untouched.

### 6.3 Portal listener (Go `net/http`, `<ap-ip>:8099`)

| Request | Response |
|---|---|
| `GET /captive-portal/api`, any Host | `200`, `Content-Type: application/captive+json`, `Cache-Control: no-store`, body `{"captive":true,"user-portal-url":"<landing>"}` |
| Host is not `<ap-ip>` or `<ap-ip>:80` | `302`, `Location: <landing>`, `Cache-Control: no-store` |
| Host is `<ap-ip>` | reverse proxy (`httputil.ReverseProxy`) to `http://<ap-ip>:<lighttpd port>` with `X-Forwarded-For`; locally generated traffic does not pass `prerouting`, so there is no loop |

`<landing>` is the absolute URL `http://<ap-ip>/login?action=%2Fwpa_conf`
(RaspAP's own redirects are relative and would stay on the probe host). After a
failed hand-off in the same AP session it is
`http://<ap-ip>/login?action=%2Fplugin__Airhatch`, so the user sees why first.
After login RaspAP sends the browser to `action` (`U:includes/login.php:19-27`).

RFC 8908/8910 want an `https` API URI; compliant clients ignore option 114
over http, and detection relies on the probe redirect. The JSON is served for
clients that read it.

---

## 7. RaspAP UI actions under the add-on

| UI action (evidence) | Client states | AP states |
|---|---|---|
| Hotspot Start/Restart → `sudo servicestart.sh --interface wlan0 --seconds 1` (`hostapd.php:67-82`) | Shim → airhatchd: clear `override`; reply "Wi-Fi client is active; the hotspot opens automatically when Wi-Fi is unavailable". No AP (it would cut the session) | Reapply: `close_ap` + `open_ap`, so Hotspot/DHCP page edits apply |
| Same, in `held` or `ap_failed` | override = auto, `open_ap` now | – |
| Hotspot Start with `br0`/`uap0` (bridged, AP+client) | reply `unsupported_raspap_mode`; nothing changes | same |
| Hotspot Stop → `systemctl stop hostapd.service` (`hostapd.php:99-104`) | no-op (hostapd is not running) | observed: override = off, hold, client attempt if saved > 0 (3.3) |
| Hotspot Save (`HotspotService`) | files written; `systemctl reload dnsmasq` works (dnsmasq runs); applied at the next AP open | files written; applied on Restart |
| DHCP page start/stop/restart dnsmasq (`includes/dhcp.php:43,53,65`) | dnsmasq never serves wlan0 (content fence) | stop → notice only |
| DHCP page save → `/etc/dhcpcd.conf` | the next dhcpcd (re)start renders from it | Restart hotspot → reapply |
| WiFi page load (`configure_client.php:15-22`) | real supplicant | stand-in (section 4) |
| WiFi Save (`client_settings`) | real `reconfigure`; the supplicant reconnects itself; a removed current network goes through `lost` and the grace | file change → hand-off |
| WiFi Connect | real `select_network` (`reassociate` is refused by sudoers upstream) | stand-in `SELECT_NETWORK` → hand-off with that SSID |
| WiFi Reinitialize | `sudo rm` of the socket works, `sudo wpa_supplicant` is refused (O8); airhatchd restarts its supplicant because the socket was deleted | `sudo wpa_supplicant` is refused (O8); the stand-in rebinds on the next tick |
| `wifiClientInterface` POST (`WiFiManager.php:293-307`) | `disconnect`/`disable_network all` → `lost` → grace → AP | stand-in `OK` |

No UI path can start hostapd on wlan0 (lease fence), serve DHCP/DNS on wlan0
in client mode (content fence), or write `/run/airhatch` (root 0755; nothing in
sudoers).

---

## 8. First boot

`airhatch-firstboot.service` (`Type=oneshot`, `RemainAfterExit=yes`,
`ConditionPathExists=!/etc/airhatch/firstboot.done`,
`After=local-fs.target sys-subsystem-net-devices-wlan0.device`,
`Before=hostapd.service lighttpd.service php-fpm.service airhatchd.service`,
`WantedBy=multi-user.target`, `ExecStart=/usr/sbin/airhatchd firstboot`).
airhatchd `Wants=` it; if it fails, the client still works and the AP is
refused by the preconditions.

Steps (under `flock /run/airhatch/firstboot.lock`):

1. Suffix: last two bytes of `/sys/class/net/wlan0/address` as 4 upper-case hex
   digits (wait up to 10 s); else the first 4 hex digits of
   SHA-256(`/etc/machine-id`).
2. Secrets from `crypto/rand` over `ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789`:
   admin password 16 characters, AP passphrase 12.
3. `/etc/raspap/raspap.auth`, only if absent: `admin\n<hash>\n0\n` (the format
   RaspAP itself writes), hash from
   `php -r 'echo password_hash(rtrim(fgets(STDIN)), PASSWORD_BCRYPT);'` with the
   password on stdin, checked against `^\$2y\$`; owner www-data:www-data, mode
   0600; atomic.
4. `/etc/hostapd/hostapd.conf`: `ssid=RaspAP` → `ssid=RaspAP-<XXXX>` and
   `wpa_passphrase=ChangeMe` → the passphrase, each only when the line equals
   upstream's default exactly; every other byte kept; owner and mode kept;
   atomic.
5. `/boot/raspap-airhatch.txt` (only the values actually generated; "unchanged"
   otherwise): SSID, passphrase, admin user and password,
   `http://<ap-ip>/` on the hotspot, "change these in RaspAP (Authentication,
   Hotspot) and delete this file".
6. The same text to `/dev/console` (serial and HDMI). The journal gets only
   "credentials written to /boot/raspap-airhatch.txt".
7. Marker `/etc/airhatch/firstboot.done` last (no secrets), so a crash re-runs
   the step; steps 3-4 are idempotent.

The plugin shows a reminder while `/boot/raspap-airhatch.txt` exists.

---

## 9. Plugin and socket API

**Socket.** `/run/airhatch/api.sock`, unix stream, `root:www-data 0660`. The
peer uid (`SO_PEERCRED`) must be 0 or 33 (www-data); `servicestart` is uid 0
only. One request line (≤ 4 KiB JSON) and one reply line per connection; 2 s
read timeout. No secret crosses the socket. Exact JSON in API.md §3.

**Plugin.** `/var/www/html/plugins/Airhatch/` implements
`RaspAP\Plugins\PluginInterface` (constructor `(string $pluginPath, string
$pluginName)`, no I/O; `U:src/RaspAP/Plugins/PluginManager.php:41-94`). Sidebar
item "Wi-Fi fallback" (`fas fa-life-ring`, priority 78), page
`/plugin__Airhatch`. It shows mode and state, SSID/IP or AP SSID and station
count, the last error, the timers, override and hold, notices, the hand-off
warning and the `/boot` reminder; buttons "Open hotspot now", "Retry Wi-Fi
now", "Back to automatic" (POST with RaspAP's CSRF field, only when
`!RASPI_MONITOR_ENABLED`). No sudo, no sudoers entry. Before it talks to the
daemon it checks the login itself (`HTTPAuth::isLogged`, when
`RASPI_AUTH_ENABLED`), because RaspAP's own check is skipped for any URI
containing `/login` (integration.md §4); logged out, it shows only a login
hint.

---

## 10. Packaging

- Names, packages and file ownership: API.md §1-2. Usage: the layer README.
- `meta-raspap-airhatch`: `LAYERDEPENDS = "core raspap networking-layer"`,
  priority 7, `LAYERSERIES_COMPAT = "wrynose"`.
- New recipes: `REQUIRED_DISTRO_FEATURES = "raspap airhatch systemd usrmerge"`.
- The one bbappend: `raspap-webgui_%.bbappend` →
  `require ${@bb.utils.contains('DISTRO_FEATURES', 'airhatch', 'raspap-webgui-airhatch.inc', '', d)}`.
- `raspap-airhatch_git.bb` fetches this repository at a pinned commit
  (`RASPAP_AIRHATCH_GIT_URI`, `RASPAP_AIRHATCH_SRCREV`) and builds airhatchd
  as a static binary (`CGO_ENABLED=0`, no PIE) without network access.

---

## 11. Verification

### 11.1 Unit tests

`go vet ./...` and `go test -race ./...` in `daemon/`; the fixtures are byte
copies of upstream RaspAP 3.5.7 files and the shipped files themselves.

- `policy`: a table test for every row and invariant of §3.3, on a fake
  clock.
- `radio`: exact argv order for the AP open (§5.1), the AP close, the client
  attempt and the stale cleanup. When `systemctl restart dnsmasq.service`
  runs, the AP dnsmasq content is on disk and the nft batch was applied;
  when `systemctl start hostapd.service` runs, the lease exists; on close
  the lease is gone before hostapd stops and the client dnsmasq content is
  written before dhcpcd restarts; rollback at each failing step; a second
  open is a no-op; a failing hostapd stop keeps the fences.
- `dhcpcdrender`: golden renders (API.md §6.3).
- `raspapconf`: every RaspAP fixture, the SSID encodings, new, changed and
  deleted networks, and the empty file of `ensureWpaSupplicant`.
- `standin`: a round trip of every command of §4 with a client speaking the
  wire format; `SELECT_NETWORK` triggers a hand-off with its SSID; a
  `SET_NETWORK … psk` value is kept nowhere; socket mode and group; rebind
  after unlink.
- `captive`: golden nft and dnsmasq texts; the portal through `httptest`.
- `firstboot`: on upstream's `hostapd.conf` only the two lines change;
  custom values and an existing `raspap.auth` are kept; owner and mode; the
  marker last; no secret in the log; php gets the password on stdin only.
- `api`: the uid matrix (0, 33, other) on a real socket, `servicestart` root
  only, every error code, an oversize request.
- `config`: the shipped `daemon/airhatchd.conf` parses to the defaults;
  every bound; unknown and duplicate keys.
- `supervisor`: the key paths of every plugin status fixture equal those of
  the daemon's status.

The plugin tests are described in API.md §7.4.

### 11.2 Image checks (tier 2)

On a rootfs tarball built with the feature, in a chroot of the image:

- `tests/static/run-base.sh` runs meta-raspap's own `tests/run-static.sh`. It
  must fail on exactly the three labels of integration.md §3, and the two
  webroot labels may list only `plugins/Airhatch`.
- `tests/static/run.sh` checks that every packaged path of API.md §2 has its
  owner and mode, that every `system/` file and the plugin are byte-equal in
  the image, that the shim is root 0750, which units are enabled, that
  RaspAP's own `dhcpcd.conf`, `hostapd.conf`, `090_*.conf` and sudoers are
  still upstream's, `visudo -c`, `systemd-analyze verify` for the add-on's
  units and those with drop-ins, that airhatchd is static,
  `dnsmasq --test` with the client and the AP content, and that
  `airhatchd render-dhcpcd` in the image equals the golden renders.
- `tests/static/wifimanager-harness.php` runs upstream `WiFiManager`
  (`setKnownStationsWPA`, `connectedWifiStations`, `nearbyWifiStations`,
  `writeWpaSupplicant`) from the image's webroot with `sudo-shim.sh` as
  `sudo`; the smoke test uses it to drive RaspAP's real code.

### 11.3 Smoke test (tier 3)

`tests/smoke/run.sh` imports the rootfs into Docker and boots it with
systemd as PID 1. A veth pair stands in for the radio: `wlan0` in the
container, `peer0` in a network namespace `air` that plays the phone and the
home router. Test-only drop-ins in `/run/systemd/system/` (never the shipped
files) run hostapd and the supplicant with the `wired` driver; the package's
lease condition stays active. `tests/smoke/fakes/hostapd` answers hostapd's
control socket where veth cannot reach `state=ENABLED`. The home router is a
dnsmasq in `air`; test networks are written through RaspAP's real
`writeWpaSupplicant`. `tests/smoke/airhatchd-test.conf` shortens the timers.

| Scenario | Assertions |
|---|---|
| Boot, no networks | AP within 30 s: lease present, hostapd and dnsmasq active, the nft table present; from `air`, DNS answers 10.3.141.1 for any name, a probe URL gets a 302 to `http://10.3.141.1/login?action=%2Fwpa_conf`, `/captive-portal/api` has the RFC 8908 headers and JSON |
| Boot, reachable network | wlan0 gets a lease from the router; hostapd never starts; no nft table; no DHCP offer from the device on `peer0` |
| Drop | router down → AP within `disconnect_grace` + 15 s |
| WiFi page on the AP | logged out, `/plugin__Airhatch?x=/login` shows only the login hint; login through the portal with the first-boot password; the plugin page shows the status; `/wpa_conf` renders; no `wpa_supplicant` start through sudo; the image's `wpa_cli` gets the stand-in's replies |
| Save on the AP → hand-off | POST `client_settings` with the page's CSRF token; router up → `connected` within `handoff_delay` + `connect_timeout`; AP closed, table gone, dnsmasq back to `except-interface=wlan0`, no listener on 8099 |
| Wrong network | an SSID that is not there → AP back with `network_not_found` or `connect_timeout` |
| Captive only in AP mode | after the hand-off the AP address is gone from wlan0, DNS at the client address gives no wildcard answer, lighttpd answers there without the redirect |
| airhatchd killed on the AP | `ExecStopPost` closes the AP (lease, hostapd, nft table gone), dnsmasq back to the client content, no AP address and no DHCP offer; started again → `ap` |
| Hard constraint, also with airhatchd killed | in client mode `sudo -u www-data sudo systemctl start hostapd.service` / `dnsmasq.service` leave hostapd inactive and wlan0 without DHCP; wlan0 never carries 10.3.141.1 |
| RaspAP hotspot buttons | `servicestart.sh` in client mode → the client line of API.md §5, no AP; in AP mode → reapply; `systemctl stop hostapd.service` on the AP → `held` (or client), AP back after `off_hold` |
| Reinitialize on the AP | a `wpa_supplicant -B` started by root outside sudo is reaped within 2 ticks |
| RaspAP cannot start a supplicant | `sudo -u www-data sudo /sbin/wpa_supplicant` in both forms → "not allowed"; other sudo rules (`wpa_cli status`) still work |
| Close fails on dnsmasq | while dnsmasq cannot start, a hand-off stops hostapd and writes the fence but the close keeps failing: no supplicant start, no lease, not `connected`; a Reinitialize starts nothing; once dnsmasq starts again the close succeeds, the hotspot reopens, and a retry connects |
| Leftover table at start | with an `inet airhatch` table left over, airhatchd starts → `connected`, the table gone, and the supplicant never ran while the table existed |
| Daemon restart while connected | `systemctl restart airhatchd` → `connected` again without restarting the supplicant |

The scenarios are split over the first boot of a fresh image (no saved
network) and a second boot after a reboot of the container, with a reachable
network saved.

### 11.4 What only hardware can show

The smoke report lists these as not covered: station/AP switching on
brcmfmac and its timing; whether a supplicant started on the AP interface
drops the AP; `iw scan` while beaconing; `WRONG_KEY` and
`NETWORK-NOT-FOUND` reporting; the eth0 blip of the dhcpcd restart; phone
captive-portal detection (iOS, Android, Windows); real boot timing of wlan0.

### 11.5 Layer checks

- `tests/nochange/check-layer.sh`: yocto-check-layer on
  `meta-raspap-airhatch` with meta-raspap, meta-oe, meta-python and
  meta-networking as dependencies; every test passes, `test_signatures`
  included.
- `tests/nochange/run.sh`: in a meta-raspap build without the feature,
  adding the layer leaves every locked signature of `core-image-base`
  identical except `do_rootfs_wicenv` and the image tasks after it, which
  differ by `BBLAYERS` alone (wic's layer search path; any added layer
  changes it, as `bitbake-diffsigs` shows); the build runs only those image
  tasks; the manifest is unchanged; meta-raspap's `run-static.sh` stays at
  333/333. The build's `bblayers.conf` is restored afterwards and checked by
  checksum.

---

## 12. Risks

1. The stand-in, the scan-cache key and the `servicestart.sh` arguments depend
   on RaspAP 3.5.7 internals. Tier-2 tests pin them; a RaspAP bump must re-run
   them (the layer README's "Updating RaspAP" checklist).
2. RaspAP's own supplicant starts are refused by the sudoers drop-in O8, an
   added file that narrows RaspAP's sudoers without changing it. A supplicant
   started outside sudo (by root) is only reaped, and could drop the AP for
   up to a tick before the reaper acts (**HW**).
3. Each hand-off cuts the phone off for up to `connect_timeout` plus the AP
   reopen. The plugin warns; RaspAP's own `/wpa_conf` page cannot.
4. Every mode switch restarts dhcpcd, which renews eth0 too (as upstream's
   hotspot restart does).
5. Bridged and AP+client RaspAP modes are rejected; a user who selects them
   loses the fallback AP until they revert.
6. Upstream issues stay upstream (integration.md §4): `/login` bypass, the QR
   passphrase exposure, `writeCache` storing an array.
7. Credentials live in `/etc` on the rootfs slot; a RAUC slot switch can bring
   back defaults, and first boot then generates new ones on that slot.
8. `/boot/raspap-airhatch.txt` is readable by anyone holding the card; the
   plugin keeps reminding the user to delete it.
