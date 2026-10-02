SUMMARY = "Wi-Fi client first, RaspAP hotspot with captive portal as a fallback"
DESCRIPTION = "airhatchd joins the saved Wi-Fi networks of RaspAP and opens \
RaspAP's hotspot, with a captive portal to its WiFi client page, only when no \
saved network connects. Includes the RaspAP plugin that shows its state."
HOMEPAGE = "https://github.com/david0ha/raspap-airhatch"
SECTION = "net"
LICENSE = "GPL-3.0-only"
LIC_FILES_CHKSUM = "file://LICENSE;md5=4fe869ee987a340198fb0d54c55c47f1"

# The repository that holds this layer and the sources it builds, at a
# pinned commit. A build from a local checkout overrides the URI (and, for
# another commit, the revision) in local.conf; the layer README describes
# that and the release bump of the revision.
RASPAP_AIRHATCH_GIT_URI ?= "git://github.com/david0ha/raspap-airhatch.git;protocol=https;branch=main"
RASPAP_AIRHATCH_SRCREV ?= "ea3d03a8e10c6ad950515f2f426af2213546003f"

SRC_URI = "${RASPAP_AIRHATCH_GIT_URI}"
SRCREV = "${RASPAP_AIRHATCH_SRCREV}"
PV = "0.1.0+git"

inherit go-mod systemd features_check

REQUIRED_DISTRO_FEATURES = "raspap airhatch systemd usrmerge"

# The plugin files are owned by www-data, which packaging resolves through
# the sysroot's /etc/passwd.
DEPENDS += "base-passwd"

GO_IMPORT = "github.com/david0ha/raspap-airhatch"
GO_WORKDIR = "${GO_IMPORT}/daemon"
GO_INSTALL = "${GO_IMPORT}/daemon/cmd/airhatchd"

# airhatchd is a static binary: no PIE, no shared Go runtime, no ELF interpreter.
GOBUILDFLAGS:remove = "-buildmode=pie"
GO_LINKSHARED = ""
GO_LINUXLOADER = ""
export CGO_ENABLED = "0"
export GOFLAGS = "-buildvcs=false"
export GOPROXY = "off"
export GOTOOLCHAIN = "local"
do_compile[network] = "0"

# go.bbclass expects ${S}/src; here the whole checkout is the import path.
do_configure() {
    install -d $(dirname ${B}/src/${GO_IMPORT})
    ln -snf ${S} ${B}/src/${GO_IMPORT}
}

do_install() {
    install -D -m 0755 ${B}/${GO_BUILD_BINDIR}/airhatchd ${D}${sbindir}/airhatchd
    install -D -m 0644 ${S}/daemon/airhatchd.conf ${D}${sysconfdir}/airhatch/airhatchd.conf

    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${S}/system/systemd/*.service ${D}${systemd_system_unitdir}/
    for dropin in ${S}/system/systemd/*.service.d/50-airhatch.conf; do
        unit_d=$(basename "$(dirname "$dropin")")
        install -D -m 0644 "$dropin" ${D}${systemd_system_unitdir}/$unit_d/50-airhatch.conf
    done

    install -D -m 0644 ${S}/system/tmpfiles.d/raspap-airhatch.conf \
        ${D}${nonarch_libdir}/tmpfiles.d/raspap-airhatch.conf
    install -D -m 0644 ${S}/system/dnsmasq.d/095_airhatch.conf \
        ${D}${sysconfdir}/dnsmasq.d/095_airhatch.conf
    # The directory mode is raspap-webgui's, which owns it too.
    install -d -m 0750 ${D}${sysconfdir}/sudoers.d
    install -m 0440 ${S}/system/sudoers.d/095_airhatch ${D}${sysconfdir}/sudoers.d/095_airhatch

    webroot=${D}/var/www/html
    install -d $webroot/plugins/Airhatch
    cp -R --no-dereference ${S}/plugin/Airhatch/. $webroot/plugins/Airhatch/
    find $webroot/plugins/Airhatch -type d -exec chmod 0755 {} +
    find $webroot/plugins/Airhatch -type f -exec chmod 0644 {} +
    # raspap-webgui owns the webroot and its plugins directory as www-data;
    # the package shares those directories, so it must not change their owner.
    chown -R www-data:www-data $webroot
}

PACKAGES =+ "${PN}-plugin"

FILES:${PN}-plugin = "/var/www/html/plugins/Airhatch"
FILES:${PN} += "${systemd_system_unitdir} ${nonarch_libdir}/tmpfiles.d"

# airhatch-wpa-supplicant.service is installed but started only by airhatchd.
SYSTEMD_SERVICE:${PN} = "airhatchd.service airhatch-firstboot.service"

CONFFILES:${PN} = "${sysconfdir}/airhatch/airhatchd.conf"

RDEPENDS:${PN} = "raspap-webgui dhcpcd dnsmasq hostapd wpa-supplicant iw iproute2 nftables php-cli sudo"
# The captive portal's nft table: DNS and HTTP redirects and the reject rule.
RRECOMMENDS:${PN} = "kernel-module-nft-redir kernel-module-nft-nat kernel-module-nft-reject-inet kernel-module-nft-chain-nat"
RDEPENDS:${PN}-plugin = "${PN} raspap-webgui"
