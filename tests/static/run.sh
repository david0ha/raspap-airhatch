#!/bin/bash
# Tier-2 checks of an image built with the airhatch feature
# (docs/DESIGN.md §11.2): the shipped files, unit state, RaspAP's untouched
# files, and the image's own dnsmasq and airhatchd run in a chroot of the
# image.
# Env: META_RASPAP (required; see CONTRIBUTING.md).
# Usage: run.sh <rootfs.tar.bz2>
set -euo pipefail

# shellcheck source=SCRIPTDIR/../lib.sh
. "${BASH_SOURCE[0]%/*}/../lib.sh"

UNIT_DIR=/usr/lib/systemd/system
PLUGIN_DIR=/var/www/html/plugins/Airhatch
AP_IP=10.3.141.1

AIRHATCH_ENABLED=(airhatchd.service airhatch-firstboot.service)
AIRHATCH_NOT_ENABLED=(airhatch-wpa-supplicant.service)

# Units checked by systemd-analyze verify: the add-on's own and those it
# gives drop-ins.
VERIFIED_UNITS=(
    airhatchd.service airhatch-firstboot.service airhatch-wpa-supplicant.service
    hostapd.service hostapd@wlan0.service dhcpcd.service raspapd.service
)

# Package files that are not copies of a repository file: path, owner, mode.
package_paths() {
    cat <<EOF
/usr/sbin/airhatchd 0:0 0755
/etc/airhatch 0:0 0755
/etc/sudoers.d 0:0 0750
$PLUGIN_DIR 33:33 0755
$PLUGIN_DIR/templates 33:33 0755
EOF
}

# Shipped copies of repository files: image path, repository path, owner,
# mode. Every file under system/ and plugin/Airhatch/ is listed.
shipped_files() {
    local file
    printf '/etc/airhatch/airhatchd.conf daemon/airhatchd.conf 0:0 0644\n'
    printf '/etc/raspap/hostapd/servicestart.sh %s 0:0 0750\n' \
        meta-raspap-airhatch/recipes-webadmin/raspap/files/servicestart.sh
    while read -r file; do
        case $file in
            system/systemd/*) printf '%s/%s %s 0:0 0644\n' "$UNIT_DIR" "${file#system/systemd/}" "$file" ;;
            system/tmpfiles.d/*) printf '/usr/lib/tmpfiles.d/%s %s 0:0 0644\n' "${file##*/}" "$file" ;;
            system/dnsmasq.d/*) printf '/etc/dnsmasq.d/%s %s 0:0 0644\n' "${file##*/}" "$file" ;;
            system/sudoers.d/*) printf '/etc/sudoers.d/%s %s 0:0 0440\n' "${file##*/}" "$file" ;;
            plugin/Airhatch/*) printf '%s/%s %s 33:33 0644\n' "$PLUGIN_DIR" "${file#plugin/Airhatch/}" "$file" ;;
            *) printf 'unmapped %s - -\n' "$file" ;;
        esac
    done < <(cd "$AIRHATCH_DIR" && find system plugin/Airhatch -type f | sort)
}

# The lines of the shipped files that carry the paths of docs/API.md §2: image file, line.
contract_lines() {
    cat <<EOF
$UNIT_DIR/hostapd.service.d/50-airhatch.conf|ConditionPathExists=/run/airhatch/ap.lease
$UNIT_DIR/hostapd@.service.d/50-airhatch.conf|ConditionPathExists=/run/airhatch/ap.lease
$UNIT_DIR/raspapd.service.d/50-airhatch.conf|ConditionPathExists=!/usr/sbin/airhatchd
$UNIT_DIR/dhcpcd.service.d/50-airhatch.conf|ExecStartPre=/usr/sbin/airhatchd render-dhcpcd
$UNIT_DIR/dhcpcd.service.d/50-airhatch.conf|ExecStart=/sbin/dhcpcd -q --nobackground -f /run/airhatch/dhcpcd.conf
$UNIT_DIR/dhcpcd.service.d/50-airhatch.conf|TimeoutStopSec=5s
/etc/dnsmasq.d/095_airhatch.conf|conf-file=/run/airhatch/dnsmasq.conf
/usr/lib/tmpfiles.d/raspap-airhatch.conf|d /run/airhatch 0755 root root -
/usr/lib/tmpfiles.d/raspap-airhatch.conf|f /run/airhatch/dnsmasq.conf 0644 root root - except-interface=wlan0
/etc/raspap/hostapd/servicestart.sh|exec /usr/sbin/airhatchd servicestart "\$@"
/etc/sudoers.d/095_airhatch|www-data ALL=(ALL) NOPASSWD: !/sbin/wpa_supplicant, !/usr/sbin/wpa_supplicant
EOF
}

# RaspAP's files that the add-on must leave as upstream ships them.
raspap_files() {
    cat <<'EOF'
/etc/dhcpcd.conf config/dhcpcd.conf
/etc/hostapd/hostapd.conf config/hostapd.conf
/etc/dnsmasq.d/090_raspap.conf config/090_raspap.conf
/etc/dnsmasq.d/090_wlan0.conf config/090_wlan0.conf
/etc/sudoers.d/090_raspap installers/raspap.sudoers
EOF
}

# in_root_mounted <command...>: in_root with the host's /dev and a fresh
# /proc in the image, for programs that need them (dnsmasq seeds its random
# generator from /dev/urandom; systemd-tmpfiles requires /proc).
in_root_mounted() {
    # shellcheck disable=SC2016 # $0 and $@ expand in the inner shell
    unshare -rmpf env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C \
        sh -c 'mount --rbind /dev "$0/dev" && mount -t proc proc "$0/proc" && exec chroot "$0" "$@"' \
        "$ROOT" "$@"
}

has_owner_mode() {
    local actual
    actual=$(owner_mode "$1") || return 1
    [[ ${actual% *} == "$2" ]]
}

check_files() {
    local path source owner mode
    while read -r path owner mode; do
        check "owner/mode $path: $owner $mode" has_owner_mode "$path" "$owner $mode"
    done < <(package_paths)
    while read -r path source owner mode; do
        if [[ $path == unmapped ]]; then
            fail "no image path for repository file $source"
            continue
        fi
        check "content $path = $source" cmp -s "$AIRHATCH_DIR/$source" <(root_cat "$path")
        check "owner/mode $path: $owner $mode" has_owner_mode "$path" "$owner $mode"
    done < <(shipped_files)
    check "plugin directory holds only the plugin's files" \
        diff <(cd "$AIRHATCH_DIR/plugin/Airhatch" && find . -mindepth 1 | sort) \
        <(cd "$ROOT$PLUGIN_DIR" && find . -mindepth 1 | sort)

    local line file
    while IFS='|' read -r file line; do
        check "$file has '$line'" grep -qxF -- "$line" <(root_cat "$file")
    done < <(contract_lines)
}

check_raspap_files() {
    local path source
    # shellcheck disable=SC2034 # read by upstream_file
    SRCREV=$(raspap_srcrev)
    while read -r path source; do
        check "RaspAP's $path = upstream $source" cmp -s <(upstream_file "$source") <(root_cat "$path")
    done < <(raspap_files)
}

check_units() {
    local enabled unit
    enabled=$(awk -F '\t' '$1 ~ /^\/etc\/systemd\/system\/[^\/]+\.wants\// {
        sub(/.*\//, "", $1); print $1 }' "$META" | sort -u)
    while read -r unit; do
        check "unit $unit enabled" grep -qxF "$unit" <<<"$enabled"
    done < <(meta_raspap_array ENABLED_UNITS; printf '%s\n' "${AIRHATCH_ENABLED[@]}")
    for unit in "${AIRHATCH_NOT_ENABLED[@]}"; do
        check "unit $unit not enabled" test -z "$(grep -xF "$unit" <<<"$enabled" || true)"
    done
}

# The host's systemd-analyze reads the image's units through --root. It may
# be older than the image's systemd, so only its findings about the verified
# units count, not its warnings about unrelated image units.
check_unit_verify() {
    local output findings
    if ! output=$(systemd-analyze verify --root="$ROOT" "${VERIFIED_UNITS[@]}" 2>&1); then
        fail "systemd-analyze verify ${VERIFIED_UNITS[*]}"
        awk '{ print "    " $0 }' <<<"$output"
        return
    fi
    findings=$(grep -E 'airhatch|hostapd|dhcpcd|raspapd' <<<"$output" || true)
    if [[ -z $findings ]]; then
        pass "systemd-analyze verify ${VERIFIED_UNITS[*]}"
    else
        fail "systemd-analyze verify: findings about the verified units"
        awk '{ print "    " $0 }' <<<"$findings"
    fi
}

has_no_interpreter() {
    local headers
    headers=$(readelf -l "$1") || return 1
    ! grep -qi 'program interpreter' <<<"$headers"
}

check_static_binary() {
    local binary=$ROOT/usr/sbin/airhatchd description
    description=$(file -b "$binary")
    check "airhatchd is an aarch64 ELF executable: $description" grep -q 'ELF 64-bit.*ARM aarch64' <<<"$description"
    check "airhatchd is statically linked" grep -q 'statically linked' <<<"$description"
    check "airhatchd has no program interpreter" has_no_interpreter "$binary"
}

dnsmasq_test() {
    in_root_mounted dnsmasq --test -C /dev/null -7 /etc/dnsmasq.d
}

dnsmasq_test_fails() {
    ! dnsmasq_test >/dev/null 2>&1
}

# dnsmasq_test_with <content>: writes /run/airhatch/dnsmasq.conf, then tests.
dnsmasq_test_with() {
    printf '%s' "$1" >"$ROOT/run/airhatch/dnsmasq.conf" && dnsmasq_test
}

# The include of 095_airhatch.conf fails closed without its target, accepts
# what tmpfiles creates at boot, and both contents airhatchd writes.
check_dnsmasq() {
    local runtime=$ROOT/run/airhatch/dnsmasq.conf
    check "no /run/airhatch/dnsmasq.conf before tmpfiles runs" test ! -e "$runtime"
    check "dnsmasq --test fails while /run/airhatch/dnsmasq.conf is missing" dnsmasq_test_fails
    check "systemd-tmpfiles creates /run/airhatch" \
        in_root_mounted systemd-tmpfiles --create /usr/lib/tmpfiles.d/raspap-airhatch.conf
    check "tmpfiles content of /run/airhatch/dnsmasq.conf is except-interface=wlan0" \
        test "$(cat "$runtime" 2>/dev/null)" = except-interface=wlan0
    check "dnsmasq --test with the boot content" dnsmasq_test
    check "dnsmasq --test with the client content" \
        dnsmasq_test_with $'# airhatch: client\nexcept-interface=wlan0\n'
    check "dnsmasq --test with the AP content ($AP_IP)" \
        dnsmasq_test_with "# airhatch: ap
address=/#/$AP_IP
except-interface=lo
dhcp-option=tag:wlan0,114,\"http://$AP_IP/captive-portal/api\"
"
}

# render_in_mode <mode>: runs render-dhcpcd in the image with the mode file
# for that mode (none for client, as at boot).
render_in_mode() {
    local mode_file=$ROOT/run/airhatch/mode
    rm -f "$mode_file"
    if [[ $1 == ap ]]; then
        printf 'ap\n' >"$mode_file" || return 1
    fi
    in_root /usr/sbin/airhatchd render-dhcpcd
}

# render-dhcpcd of the shipped /etc/dhcpcd.conf equals the daemon's golden
# renders of upstream's file.
check_render_dhcpcd() {
    local mode golden
    for mode in client ap; do
        golden=daemon/testdata/dhcpcd/upstream-$mode.golden
        check "airhatchd render-dhcpcd runs in the image ($mode mode)" render_in_mode "$mode"
        check "rendered /run/airhatch/dhcpcd.conf ($mode) = $golden" \
            cmp -s "$AIRHATCH_DIR/$golden" "$ROOT/run/airhatch/dhcpcd.conf"
    done
}

# sudo reads 095_airhatch after RaspAP's 090_raspap, so its refusal of
# wpa_supplicant is the last match; visudo checks the whole policy.
check_sudoers() {
    check "sudoers syntax: visudo -cf /etc/sudoers.d/095_airhatch" in_root visudo -cf /etc/sudoers.d/095_airhatch
    check "sudoers syntax: visudo -c" in_root visudo -c
    check "sudoers.d order: 090_raspap, then 095_airhatch" \
        test "$(cd "$ROOT/etc/sudoers.d" && printf '%s ' *)" = '090_raspap 095_airhatch '
}

main() {
    require_tarball "$@"
    make_workdir
    extract_rootfs "$1" "$WORKDIR/rootfs"

    check_files
    check_sudoers
    check_raspap_files
    check_units
    check_unit_verify
    check_static_binary
    check_dnsmasq
    check_render_dhcpcd
    report
}

main "$@"
