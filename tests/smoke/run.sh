#!/bin/bash
# Tier-3 smoke test (docs/DESIGN.md §11.3): boots the airhatch image under
# Docker with systemd as PID 1 and a veth pair as wlan0, then
# runs inner.sh's scenarios on the first boot and after a reboot.
# Env: META_RASPAP (required; see CONTRIBUTING.md), GO (default go): a Go
#      1.26 toolchain that builds the fake hostapd.
# Usage: run.sh <rootfs.tar.bz2>
set -euo pipefail

# shellcheck source=SCRIPTDIR/../lib.sh
. "${BASH_SOURCE[0]%/*}/../lib.sh"

GO=${GO:-go}

# What only CM5 hardware can show (docs/DESIGN.md §11.4), and the stand-ins
# this test uses instead.
NOT_COVERED=(
    'station/AP switching on brcmfmac and its timing'
    'whether a wpa_supplicant started on the AP interface drops the AP'
    'iw scan while beaconing (the scan-cache seed holds no networks on veth)'
    'WRONG_KEY and NETWORK-NOT-FOUND reporting: the wired driver associates with any SSID'
    'the eth0 blip of the dhcpcd restart (there is no eth0)'
    'captive-portal detection by iOS, Android and Windows'
    'real boot timing of wlan0'
    'hostapd: tests/smoke/fakes/hostapd answers the control socket (state=ENABLED, station count) instead of hostapd'
    'wpa_supplicant: the real one with -D wired and ap_scan=0 instead of nl80211'
)

# build_fakes <dir>: the stand-in hostapd as a static binary for the image.
build_fakes() {
    GOTOOLCHAIN=local GOMODCACHE=$AIRHATCH_DIR/.gocache/mod GOCACHE=$AIRHATCH_DIR/.gocache/build \
        CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
        "$GO" -C "$AIRHATCH_DIR/tests/smoke/fakes" build -trimpath -buildvcs=false \
        -o "$1/hostapd" ./hostapd
}

cleanup() {
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker image rm "$IMAGE" >/dev/null 2>&1 || true
    remove_workdir
}

start_container() {
    docker run --detach --name "$CONTAINER" --privileged --cgroupns=host --network none \
        --tmpfs /run --tmpfs /tmp --stop-signal SIGRTMIN+3 --env META_RASPAP=/meta-raspap \
        --volume "$AIRHATCH_DIR/tests:/tests:ro" \
        --volume "$META_RASPAP/tests:/meta-raspap/tests:ro" \
        --volume "$WORKDIR/bin:/smoke-bin:ro" \
        "$IMAGE" /bin/bash /tests/smoke/inner.sh boot >/dev/null
}

# run_phase <phase>: the phase's scenarios inside the running container.
run_phase() {
    printf '=== %s\n' "$1"
    docker exec --env META_RASPAP=/meta-raspap "$CONTAINER" /bin/bash /tests/smoke/inner.sh "$1"
}

main() {
    require_tarball "$@"
    local status=0 item
    IMAGE=airhatch-smoke:$$-$RANDOM
    CONTAINER=airhatch-smoke-$$
    make_workdir
    trap cleanup EXIT

    build_fakes "$WORKDIR/bin"
    docker import "$1" "$IMAGE" >/dev/null
    start_container
    run_phase first-boot || status=1
    docker restart --time 60 "$CONTAINER" >/dev/null
    run_phase second-boot || status=1

    echo '=== not covered (hardware only) and stand-ins'
    for item in "${NOT_COVERED[@]}"; do
        printf -- '- %s\n' "$item"
    done
    if ((status)); then
        echo 'smoke test FAILED'
    else
        echo 'smoke test passed'
    fi
    return "$status"
}

main "$@"
