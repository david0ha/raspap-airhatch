# raspap-airhatch: airhatchd owns wlan0's mode, so RaspAP's hotspot
# Start/Restart/Stop script hands the request to it (docs/integration.md O1).
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"
SRC_URI += "file://servicestart.sh"

do_install:append() {
    install -m 0750 ${UNPACKDIR}/servicestart.sh ${D}${sysconfdir}/raspap/hostapd/servicestart.sh
}
