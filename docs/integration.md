# raspap-airhatch: integration with meta-raspap and RaspAP

Every place where the add-on changes how meta-raspap integrates RaspAP 3.5.7,
with the reason and the evidence. The add-on overrides only installer-level
integration, and each override is listed here; anything not listed is
unchanged. Design context: [DESIGN.md](DESIGN.md).

Evidence keys as in DESIGN.md (`U:` upstream at `8a201504`, `img:` a
meta-raspap rootfs, `MR:` [meta-raspap](https://github.com/david0ha/meta-raspap)).

All entries exist only when `DISTRO_FEATURES` contains `airhatch`: O1 is a
bbappend gated with `require ${@bb.utils.contains('DISTRO_FEATURES',
'airhatch', …)}`; O2-O8 are files of the package `raspap-airhatch`, whose
recipe has `REQUIRED_DISTRO_FEATURES = "raspap airhatch systemd usrmerge"` and
is skipped otherwise.

With the feature on, the rootfs also differs from the meta-raspap image in
files the add-on does not ship. Both are generated at image build time:

- **Dynamic user and group numbers.** In the meta-raspap image `dhcpcd` is
  998:997 and `avahi` 999:998; with the add-on's packages installed the
  package order changes, so `avahi` gets 998:997 and `dhcpcd` 999:998.
  `/etc/passwd`, `/etc/group`, `/etc/shadow` and `/etc/gshadow` change in
  those numbers and in their row order, and `/var/lib/dhcpcd/` carries the
  new numbers (its owner is still `dhcpcd`). The add-on adds no user or
  group. It does not pin the numbers, since that belongs to the image's own
  `useradd` policy.
- **NSS integrity files.** `/usr/lib/libfreebl3.chk`, `/usr/lib/libnssdbm3.chk`
  and `/usr/lib/libsoftokn3.chk` are regenerated: the `nss` package's
  postinstall runs `shlibsign`, which signs each library with a fresh random
  HMAC key, so every rootfs build gets new `.chk` files. The libraries
  themselves are byte-identical, and each `.chk` verifies against its library
  in both images.

## 1. Overrides

| ID | Path in the image | What | Why | Evidence |
|---|---|---|---|---|
| O1 | `/etc/raspap/hostapd/servicestart.sh` | Upstream content replaced by a short exec shim that runs `/usr/sbin/airhatchd servicestart "$@"`. Same owner and mode (root 0750). Shipped by `raspap-webgui` through `raspap-webgui_%.bbappend` → `raspap-webgui-airhatch.inc` | RaspAP's Start/Restart hotspot calls this script through sudo with any arguments. Upstream's script stops dhcpcd, rewrites `/usr` and `/etc`, and starts hostapd on wlan0 regardless of client state. airhatchd is the only owner of wlan0, so the restart action must go through it. The script path is the only interception point that needs no PHP or sudoers change | `U:includes/hostapd.php:67-82`; `U:installers/raspap.sudoers` (`servicestart.sh` line); `U:installers/servicestart.sh:60-128` |
| O2 | `/usr/lib/systemd/system/raspapd.service.d/50-airhatch.conf` | `[Unit]` `ConditionPathExists=!/usr/sbin/airhatchd` | `raspapd` runs `servicestart.sh --seconds 1` after every boot, which would start the AP over the daemon's client attempt and, with O1, would ask the daemon to do so. The unit stays enabled; the journal says why it was skipped | `img:/usr/lib/systemd/system/raspapd.service`; `MR:…/raspap-webgui_3.5.7.bb` (`SYSTEMD_SERVICE`) |
| O3 | `/usr/lib/systemd/system/hostapd.service.d/50-airhatch.conf` | `[Unit]` `ConditionPathExists=/run/airhatch/ap.lease` | Hard constraint: no hostapd on wlan0 in client mode, whoever runs `systemctl start` (boot, RaspAP's sudoers-allowed `systemctl start hostapd.service`, a script). `/run` is tmpfs, so the lease is absent at every boot. The unit stays enabled, so meta-raspap's enabled-units check still passes | `img:/etc/fstab` (`/run` tmpfs); `img:/etc/systemd/system/multi-user.target.wants/hostapd.service`; sudoers `systemctl start hostapd.service` |
| O4 | `/usr/lib/systemd/system/hostapd@.service.d/50-airhatch.conf` | Same condition as O3 | RaspAP installs `hostapd@.service` (not enabled); the fence covers it too | `MR:…/raspap-webgui_3.5.7.bb` (`_install_raspap_hostapd`) |
| O5 | `/usr/lib/systemd/system/dhcpcd.service.d/50-airhatch.conf` | `[Service]` `ExecStartPre=/usr/sbin/airhatchd render-dhcpcd`, `ExecStart=`, `ExecStart=/sbin/dhcpcd -q --nobackground -f /run/airhatch/dhcpcd.conf`, `TimeoutStopSec=5s` | RaspAP's `/etc/dhcpcd.conf` makes wlan0 static `10.3.141.1/24` and leaves dhcpcd's `10-wpa_supplicant` hook active on wlan0 (meta-raspap D23), so at boot wlan0 would carry the AP address on a foreign LAN and the hook would start a supplicant behind airhatchd's back (also with Yocto's open-wildcard `/etc/wpa_supplicant.conf` when RaspAP's file is empty). The render keeps RaspAP's file as the only source and changes only wlan0: client mode → DHCP client with `nohook wpa_supplicant`; AP mode → `denyinterfaces wlan0` (airhatchd sets the address). The stock `ExecStart` is repeated with `-f` only. The stop timeout: dhcpcd 10.3.1 receives SIGTERM after wlan0 lost its carrier (the usual case when the AP opens after a drop), logs `removing interface` and never exits, so every client-to-AP switch would wait the default 90 s; airhatchd flushes wlan0 itself right after the restart | `U:config/dhcpcd.conf`; `img:/usr/lib/systemd/system/dhcpcd.service`; `img:/usr/libexec/dhcpcd-hooks/10-wpa_supplicant:8-21`; `MR:docs/installer-mapping.md` D23 |
| O6 | `/etc/dnsmasq.d/095_airhatch.conf` | `conf-file=/run/airhatch/dnsmasq.conf` (an added file, not a changed one) | Hard constraint: no DHCP or DNS service on wlan0 in client mode. RaspAP's `090_wlan0.conf` says `interface=wlan0` with a DHCP range; the included runtime file says `except-interface=wlan0` (which overrides `interface=`) except while the AP is up. dnsmasq keeps running, so RaspAP's `systemctl reload/restart dnsmasq` and any DHCP the user configured on other interfaces keep working. The name sorts after `090_*`, is not matched by RaspAP's `090_` prefix, and cannot be removed through sudoers (`rm /etc/dnsmasq.d/090_*.conf` only) | `U:config/090_wlan0.conf`; `U:config/090_raspap.conf` (`conf-dir=/etc/dnsmasq.d`); `img:/usr/lib/systemd/system/dnsmasq.service` (`-7 /etc/dnsmasq.d`); `U:src/RaspAP/Networking/Hotspot/DnsmasqManager.php:328-337` |
| O7 | `/usr/lib/tmpfiles.d/raspap-airhatch.conf` | `d /run/airhatch 0755 root root -` and `f /run/airhatch/dnsmasq.conf 0644 root root - except-interface=wlan0` | Creates O6's target with the client-mode content at every boot, before dnsmasq starts (tmpfiles runs before `sysinit.target`). A missing target makes dnsmasq fail to start, which is fail-closed | `MR:docs/installer-mapping.md` D22 (tmpfiles ordering) |
| O8 | `/etc/sudoers.d/095_airhatch` (root 0440; an added file, `090_raspap` is unchanged) | `www-data ALL=(ALL) NOPASSWD: !/sbin/wpa_supplicant, !/usr/sbin/wpa_supplicant` | airhatchd is the only thing that switches modes, but RaspAP's WiFi page starts `sudo /sbin/wpa_supplicant -i wlan0 … -B` whenever `wpa_cli` cannot reach the control socket, and "Reinitialize" deletes the socket and starts one. A supplicant airhatchd did not start can associate wlan0 while the AP closes or is open; reaping it afterwards cannot prevent that. sudo applies the last matching entry and reads `sudoers.d` in lexical order, so this file, read after `090_raspap`, refuses both of RaspAP's forms before anything runs (`NOPASSWD` makes the refusal immediate, "is not allowed to execute", instead of a password prompt); every other RaspAP rule (`rm` of the socket, `wpa_cli`) still works. airhatchd's own supplicant is a systemd unit and needs no sudo; it restarts it when the socket is deleted in client mode. Options rejected: a wrapper at `/sbin/wpa_supplicant` would replace the binary of another package and run in RaspAP's sudo context; systemd cannot see a `sudo` exec; patching `WiFiManager.php` or `090_raspap` is forbidden. Like O6 it adds a file rather than changing RaspAP's, and like O1 it narrows an installer-level integration file | `U:src/RaspAP/Networking/Hotspot/WiFiManager.php:325-333,398-411`; `U:installers/raspap.sudoers` lines 7-8; sudoers(5) ("the last match is used"); `MR:…/raspap-webgui_3.5.7.bb` (`sudoers.d` 0750, `090_raspap` 0440) |

## 2. Runtime writes into RaspAP's own files

| What | When | Why | Evidence |
|---|---|---|---|
| `/etc/hostapd/hostapd.conf`: `ssid=RaspAP` → `ssid=RaspAP-XXXX`, `wpa_passphrase=ChangeMe` → per-device passphrase, only when the line equals upstream's default | first boot, once | No public defaults on the hotspot. RaspAP's file stays the source of truth; custom values are never touched | `U:config/hostapd.conf` |
| `/etc/raspap/raspap.auth` created (`admin`, bcrypt hash, `0`), www-data 0600 | first boot, only if absent | Absent file = public `admin`/`secret`. Format and owner are what RaspAP itself writes | `U:src/RaspAP/Auth/HTTPAuth.php:28-31,102-137`; `U:includes/admin.php:97-136` |
| `/tmp/raspap/nearby_wifi_stations_<mtime>` (and `/tmp/raspap/`), www-data | each AP open and while in AP mode | RaspAP's nearby-network list on the hotspot, where a live scan probably fails. Key format pinned by tier-2 tests | `U:src/RaspAP/Networking/Hotspot/WiFiManager.php:83-104`; `U:includes/functions.php:435-481` |
| `/var/run/wpa_supplicant/wlan0` (a stand-in control socket) | from an AP open until the next client start (it is released right before airhatchd starts its supplicant) or daemon stop; a close keeps it | Answers RaspAP's `wpa_cli` while there is no supplicant, so the WiFi page works on the AP and never finds the path free while the AP side comes down | `WiFiManager.php:398-411`; `U:includes/configure_client.php:21` |

airhatchd never writes `/etc/wpa_supplicant/wpa_supplicant.conf`,
`/etc/dhcpcd.conf`, `/etc/dnsmasq.d/090_*.conf`, `/etc/raspap/hostapd.ini`,
sudoers, or any PHP, JS or template.

## 3. Effect on meta-raspap's static suite

`MR:tests/run-static.sh` on an image built with the feature fails on exactly
these labels (the add-on's `tests/static/run-base.sh` asserts the set):

| Label | Cause |
|---|---|
| `content /etc/raspap/hostapd/servicestart.sh = installers/servicestart.sh` | O1 |
| `webroot differs from upstream` (every listed line is `Only in <webroot>/plugins: Airhatch`) | the plugin (its path is RaspAP's plugin directory) |
| `webroot ownership` (only `/var/www/html/plugins/Airhatch/…` file mode lines, because upstream has no mode for them) | the plugin |

Unchanged and still passing: every enabled unit (hostapd, raspapd, dnsmasq,
dhcpcd included), every unit executable (the dhcpcd drop-in names
`/usr/sbin/airhatchd` and `/sbin/dhcpcd`, both present), sudoers (`090_raspap`
byte-equal; its `visudo -c` also parses O8), configs.

## 4. Inherited upstream issues (not changed by the add-on)

| Issue | Evidence | Relevance to the add-on |
|---|---|---|
| Any request whose URI contains `/login` skips `HTTPAuth::authenticate` | `U:src/RaspAP/Auth/HTTPAuth.php:53-58` | The fallback AP adds an audience: holders of the per-device passphrase. The plugin checks the session itself (`HTTPAuth::isLogged`) before it reads or changes anything, so `/plugin__Airhatch?x=/login` gets only a login hint (plugin harness and smoke test). RaspAP's own pages stay exposed as upstream ships them: a lighttpd deny rule cannot match every variant (lighttpd matches the normalised path, PHP the raw `REQUEST_URI`) and would change upstream behaviour, so none is shipped. Report upstream |
| `app/img/wifi-qr-code.php` exposes the AP passphrase | upstream | same audience note |
| `writeCache` writes only when the cache directory is missing, and then stores an array (`file_put_contents` of an array concatenates lines without separators) | `U:includes/functions.php:455-462`, `WiFiManager.php:94-104` | The add-on's seed creates the directory first, so RaspAP never writes its own cache while it exists |
| `reassociate` is not in sudoers; `select_network [0-9]` allows one digit only | `MR:docs/installer-mapping.md` §6 | Unchanged; in AP mode the stand-in answers `SELECT_NETWORK` itself |

## 5. Note on the sudoers drop-in

The overrides are meant to be units, drop-ins, `servicestart.sh` and the
dhcpcd render. O8 is the one exception: a sudoers drop-in. It is an added
file that only narrows RaspAP's rules (it refuses two commands and grants
nothing), and no override of units or scripts can stop RaspAP's PHP from
starting a supplicant through sudo (see O8).
