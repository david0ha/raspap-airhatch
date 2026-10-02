# meta-raspap-airhatch

> **Status: experimental.** Not yet validated on real CM5 hardware; smoke
> tests use veth, a fake hostapd and wired-mode wpa_supplicant.

An optional add-on to [meta-raspap](https://github.com/david0ha/meta-raspap)
that makes RaspAP's Wi-Fi "client first, hotspot only as a fallback":

1. At boot the device joins its saved Wi-Fi networks as a normal client.
2. If none connects (no saved network, a wrong password, a network that is
   gone), it opens RaspAP's hotspot with a captive portal that leads to
   RaspAP's login and then its WiFi client page.
3. Once a network is saved and connects, the hotspot closes. If the client
   connection drops and does not come back within a grace period, the
   hotspot opens again.

The daemon `airhatchd` is the only component that switches `wlan0` between
client and hotspot. A RaspAP plugin, "Wi-Fi fallback", shows its state and
offers "Open hotspot now", "Retry Wi-Fi now" and "Back to automatic".

RaspAP itself stays unpatched. Its files stay the only source of the Wi-Fi
settings: the hotspot SSID and passphrase in `/etc/hostapd/hostapd.conf`, the
saved networks in `/etc/wpa_supplicant/wpa_supplicant.conf`, and its dnsmasq
and dhcpcd settings.

This is an unofficial add-on. It is not affiliated with or endorsed by the
RaspAP project; see [raspap.com](https://raspap.com/) for RaspAP itself.

## Dependencies

- Yocto release series `wrynose` (`LAYERSERIES_COMPAT_raspap-airhatch =
  "wrynose"`); no other release is supported
- `openembedded-core` (`meta`)
- [meta-raspap](https://github.com/david0ha/meta-raspap) and its
  dependencies (`meta-oe`, `meta-networking`, `meta-python`)

The requirements of meta-raspap apply: systemd, `usrmerge`, and a `wlan0`
that supports AP mode.

## Usage

On top of meta-raspap's setup, add this layer and two lines:

```
bitbake-layers add-layer /path/to/meta-raspap
bitbake-layers add-layer /path/to/raspap-airhatch/meta-raspap-airhatch
# local.conf or distro conf:
DISTRO_FEATURES:append = " raspap"
IMAGE_INSTALL:append = " packagegroup-raspap"
DISTRO_FEATURES:append = " airhatch"
IMAGE_INSTALL:append = " packagegroup-raspap-airhatch"
```

Adding the layer alone changes nothing. The bbappend is a no-op and the
recipes are skipped unless `DISTRO_FEATURES` contains `airhatch`.

The recipe fetches its sources from
<https://github.com/david0ha/raspap-airhatch> at a pinned commit (see
[Source revision](#source-revision)).

## Changes to other recipes

With the `airhatch` distro feature:

- **raspap-webgui**: `/etc/raspap/hostapd/servicestart.sh` (root 0750) is a
  3-line shim that hands RaspAP's hotspot Start/Restart/Stop to
  `airhatchd servicestart`.

The `raspap-airhatch` package also adds systemd drop-ins for `hostapd`,
`hostapd@`, `dhcpcd` and `raspapd`, a tmpfiles entry,
`/etc/dnsmasq.d/095_airhatch.conf` and `/etc/sudoers.d/095_airhatch`. No
unit's enable state changes. Each change, with its reason and evidence, is
listed in [docs/integration.md](../docs/integration.md); the design is in
[docs/DESIGN.md](../docs/DESIGN.md) and the interfaces in
[docs/API.md](../docs/API.md).

Unsupported under the add-on: RaspAP's bridged and AP+client modes. Starting
the hotspot in either mode is refused and changes nothing.

## Packages

| Package | Contents |
|---|---|
| `raspap-airhatch` | `/usr/sbin/airhatchd` (static), `/etc/airhatch/airhatchd.conf`, `airhatchd.service` and `airhatch-firstboot.service` (enabled), `airhatch-wpa-supplicant.service` (started by airhatchd), the drop-ins, tmpfiles entry, dnsmasq include and sudoers drop-in |
| `raspap-airhatch-plugin` | `/var/www/html/plugins/Airhatch/` |
| `packagegroup-raspap-airhatch` | both, plus `packagegroup-raspap` |

Timers and limits are in `/etc/airhatch/airhatchd.conf`; every key is listed
there with its default (also in [docs/API.md](../docs/API.md) §4).

## Security model

The add-on keeps RaspAP's security model and adds these measures:

- **Per-device first-boot credentials.** RaspAP's defaults (login `admin` /
  `secret`, hotspot passphrase `ChangeMe`) must never reach a hotspot. On
  first boot `airhatch-firstboot.service`:
  - creates `/etc/raspap/raspap.auth` with a random admin password, unless
    the file exists;
  - changes `ssid=RaspAP` to `ssid=RaspAP-XXXX` (from the Wi-Fi MAC address)
    and `wpa_passphrase=ChangeMe` to a random passphrase in
    `/etc/hostapd/hostapd.conf`, only where the line still has upstream's
    value;
  - writes the values to `/boot/raspap-airhatch.txt` and prints them on the
    console (serial and HDMI). The journal never carries them.

  Note them, change them in RaspAP (Authentication, Hotspot), and delete
  `/boot/raspap-airhatch.txt`; the plugin reminds you while the file exists.
  Anyone holding the SD card or eMMC can read that file until it is deleted.

  The hotspot never opens with a guessable login: airhatchd refuses it while
  `/etc/raspap/raspap.auth` is missing, unreadable by www-data or malformed
  (RaspAP would then accept `admin` / `secret`), while its password is
  `secret`, and while the hotspot passphrase is `ChangeMe`, another common
  placeholder or empty. The plugin's last error says which.
- **Sudoers drop-in.** `/etc/sudoers.d/095_airhatch` refuses www-data's
  `wpa_supplicant` starts, so only airhatchd starts the Wi-Fi client. It
  only narrows RaspAP's own `090_raspap`, which stays unchanged; it grants
  nothing.
- **Plugin auth guard.** The plugin checks RaspAP's login itself before it
  shows status or accepts an action, because RaspAP 3.5.7 skips its login
  check for any URL containing `/login`. The daemon socket
  `/run/airhatch/api.sock` accepts only root and www-data, and no secret
  crosses it.
- **Inherited upstream issues.** RaspAP's own pages stay exposed to the
  `/login` URL bypass as upstream ships them, and `app/img/wifi-qr-code.php`
  shows the hotspot passphrase. On the fallback hotspot the audience is
  anyone who knows the hotspot passphrase, and RaspAP gives www-data powerful
  sudo rules, so treat the passphrase and the admin password as root access
  to the device. Details: [docs/integration.md](../docs/integration.md) §4.

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## Source revision

`raspap-airhatch_git.bb` builds the daemon, the plugin and the `system/`
files from git:

```
RASPAP_AIRHATCH_GIT_URI ?= "git://github.com/david0ha/raspap-airhatch.git;protocol=https;branch=main"
RASPAP_AIRHATCH_SRCREV ?= "<commit>"
```

Only committed sources are built. To build from a local checkout, for
example while developing, override the URI and, for a commit that is not the
pinned one, the revision in `local.conf`:

```
RASPAP_AIRHATCH_GIT_URI = "git:///path/to/raspap-airhatch;protocol=file;branch=main"
RASPAP_AIRHATCH_SRCREV = "<full hash of a commit on main>"
```

A release bumps the pin:

1. Commit the change to `daemon/`, `plugin/` or `system/` and push it to
   `main`.
2. Set `RASPAP_AIRHATCH_SRCREV` in the recipe to that commit's full hash.
3. Commit the recipe change, add a [CHANGELOG](../CHANGELOG.md) entry, and
   push.

The pin always names an earlier commit than the one that sets it.

## Testing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the environment each script
needs. In short, from the repository root:

```
(cd daemon && go vet ./... && go test -race ./...)
RASPAP_GIT=/path/to/raspap-webgui plugin/tests/run.sh
```

Against a `core-image-base-<machine>.rootfs.tar.bz2` built with the feature
(on a host of the image's architecture):

```
tests/static/run-base.sh <rootfs.tar.bz2>   # meta-raspap's suite, only the documented failures
tests/static/run.sh <rootfs.tar.bz2>        # files, units, dnsmasq and dhcpcd renders
tests/smoke/run.sh <rootfs.tar.bz2>         # needs Docker: the state machine on veth radios
```

In a meta-raspap build directory without the feature:

```
tests/nochange/run.sh          # same packages and rootfs; only core-image-base image tasks differ, by BBLAYERS alone
tests/nochange/check-layer.sh  # yocto-check-layer
```

Only real hardware can show: client and hotspot switching on brcmfmac and its
timing, whether a supplicant started on the hotspot interface drops it,
`iw scan` while beaconing, wrong-password reporting, the eth0 blip of a
dhcpcd restart, phone captive-portal detection (iOS, Android, Windows), and
real boot timing of `wlan0`.

## Updating RaspAP

The control-socket stand-in, the scan-cache key and the `servicestart.sh`
arguments depend on RaspAP 3.5.7 internals. After meta-raspap moves to a new
RaspAP:

1. Re-run `daemon/testdata/upstream/sync.sh` and `gen-wpa.sh` against the new
   tag and run the Go tests.
2. Re-check every entry of [docs/integration.md](../docs/integration.md)
   against the new upstream files.
3. Rebuild the image and run `tests/static/run-base.sh`, `tests/static/run.sh`
   and `tests/smoke/run.sh`.

## Maintainer

david0ha <david0ha@users.noreply.github.com>

Contact is through GitHub issues and pull requests at
<https://github.com/david0ha/raspap-airhatch> only. Report vulnerabilities
privately as described in [SECURITY.md](SECURITY.md).

## License

The layer metadata in this directory is MIT licensed
([COPYING.MIT](COPYING.MIT)). The daemon, the plugin and the other sources
of this repository are GPL-3.0-only ([LICENSE](../LICENSE) at the repository
root).
