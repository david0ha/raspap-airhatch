# raspap-airhatch

> **Status: experimental.** Not yet validated on real CM5 hardware; smoke
> tests use veth, a fake hostapd and wired-mode wpa_supplicant.

An optional add-on to the [meta-raspap](https://github.com/david0ha/meta-raspap)
Yocto layer that makes RaspAP's Wi-Fi "client first, hotspot only as a
fallback". At boot the device joins its saved Wi-Fi networks as a normal
client. Only when none connects does it open RaspAP's hotspot, with a
captive portal that leads the user to RaspAP's WiFi client page; once a saved
network connects, the hotspot closes again. A small Go daemon, `airhatchd`,
owns that switch. RaspAP stays unpatched and its own config files stay the
only source of the Wi-Fi settings. Without the `airhatch` distro feature,
adding the layer changes nothing in the image.

This repository holds the sources (the daemon, a RaspAP plugin and the
shipped system files) and the Yocto layer `meta-raspap-airhatch` that builds
them. It is an unofficial project, not affiliated with or endorsed by the
RaspAP project.

## Usage

On top of [meta-raspap](https://github.com/david0ha/meta-raspap)'s setup
(Yocto `wrynose`), add the layer and two lines:

```
bitbake-layers add-layer /path/to/raspap-airhatch/meta-raspap-airhatch
# local.conf or distro conf, next to meta-raspap's lines:
DISTRO_FEATURES:append = " airhatch"
IMAGE_INSTALL:append = " packagegroup-raspap-airhatch"
```

On first boot the device generates its own admin password and hotspot
passphrase and writes them to `/boot/raspap-airhatch.txt` and the console.
The [layer README](meta-raspap-airhatch/README.md) covers usage, the security
model, the source revision and testing.

## Security

- Per-device first-boot credentials replace RaspAP's public defaults
  (`admin` / `secret`, `ChangeMe`); the hotspot refuses to open while either
  default is still in place.
- A sudoers drop-in (`/etc/sudoers.d/095_airhatch`) only narrows RaspAP's
  rules: www-data can no longer start `wpa_supplicant`, so only airhatchd
  drives the Wi-Fi client.
- The plugin checks RaspAP's login itself, because RaspAP 3.5.7 skips its
  login check for URLs containing `/login`.
- RaspAP's own upstream issues are inherited unchanged; see
  [docs/integration.md](docs/integration.md) §4.

Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## Repository map

```
raspap-airhatch/
├── daemon/                  airhatchd (Go, standard library only), its
│                            shipped airhatchd.conf, tests and fixtures
├── plugin/                  RaspAP plugin "Wi-Fi fallback" (Airhatch/) and
│                            its PHP tests
├── system/                  shipped units, drop-ins, tmpfiles, dnsmasq
│                            include and sudoers drop-in
├── meta-raspap-airhatch/    the Yocto layer
├── tests/                   image tests: static, Docker smoke, no-change
│                            proof, yocto-check-layer
└── docs/                    design, interfaces, integration
```

## Documents

- [docs/DESIGN.md](docs/DESIGN.md): goals, state machine, sequences, captive
  portal, first boot, verification.
- [docs/API.md](docs/API.md): socket API and status JSON, configuration
  keys and defaults, command line, runtime files.
- [docs/integration.md](docs/integration.md): what changes in meta-raspap's
  RaspAP integration, and why.
- [meta-raspap-airhatch/README.md](meta-raspap-airhatch/README.md): usage,
  security model, source revision, testing.
- [CONTRIBUTING.md](CONTRIBUTING.md), [CHANGELOG.md](CHANGELOG.md).

## Supported release

Yocto `wrynose`, with meta-raspap and RaspAP 3.5.7.

## Maintainer

david0ha <david0ha@users.noreply.github.com>

Contact is through GitHub issues and pull requests at
<https://github.com/david0ha/raspap-airhatch> only.

## License

The daemon, the plugin, the system files and the tests are GPL-3.0-only
([LICENSE](LICENSE)). The layer metadata in `meta-raspap-airhatch/` is MIT
licensed ([meta-raspap-airhatch/COPYING.MIT](meta-raspap-airhatch/COPYING.MIT)).
