SUMMARY = "RaspAP with Wi-Fi client first and the hotspot as a fallback"
LICENSE = "MIT"

inherit packagegroup features_check

REQUIRED_DISTRO_FEATURES = "raspap airhatch systemd usrmerge"

RDEPENDS:${PN} = "packagegroup-raspap raspap-airhatch raspap-airhatch-plugin"
