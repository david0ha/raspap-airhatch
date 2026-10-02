# Changelog

All notable changes to raspap-airhatch are listed here. The version is the
recipe's `PV` (`0.1.0+git`); a release pins `RASPAP_AIRHATCH_SRCREV` to its
commit.

## 0.1.0

First public release. Experimental: not yet validated on real CM5 hardware;
smoke tests use veth, a fake hostapd and wired-mode wpa_supplicant.

- airhatchd: client-first Wi-Fi with RaspAP's hotspot as a fallback, a
  captive portal (DNS wildcard, HTTP redirect, RFC 8908 API) only while the
  hotspot is up, hand-off after a network is saved on the hotspot, a
  control-socket stand-in for RaspAP's WiFi page in AP mode, fail-closed
  cleanup after any exit.
- Per-device first-boot admin password and hotspot passphrase; the hotspot
  is refused while RaspAP's public defaults are in place.
- RaspAP plugin "Wi-Fi fallback": state, last error, timers and the
  "Open hotspot now", "Retry Wi-Fi now" and "Back to automatic" buttons,
  behind its own login check.
- meta-raspap-airhatch for Yocto `wrynose`, gated on the `airhatch` distro
  feature; fetches this repository from GitHub at a pinned commit.
- Tests: Go unit tests, the plugin harness, image checks, a Docker smoke
  test, a no-change proof and yocto-check-layer.
