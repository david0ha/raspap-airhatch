#!/bin/bash
# Recreates the upstream RaspAP fixtures from tag 3.5.7 and fails when a
# committed copy has drifted from the tag.
# Env: RASPAP_GIT (required), a clone of https://github.com/RaspAP/raspap-webgui
#      that contains tag 3.5.7.
set -euo pipefail

readonly COMMIT=8a201504
readonly FILES=(hostapd.conf dhcpcd.conf 090_wlan0.conf 090_raspap.conf)
RASPAP_GIT=${RASPAP_GIT:?set RASPAP_GIT to a raspap-webgui clone that contains tag 3.5.7}
here=$(cd "${BASH_SOURCE[0]%/*}" && pwd)

drift=0
for name in "${FILES[@]}"; do
    fresh=$(mktemp)
    git -C "$RASPAP_GIT" show "$COMMIT:config/$name" > "$fresh"
    if [[ -f $here/$name ]] && ! cmp -s "$fresh" "$here/$name"; then
        printf 'drift: %s differs from %s:config/%s\n' "$name" "$COMMIT" "$name" >&2
        drift=1
    fi
    mv "$fresh" "$here/$name"
    chmod 0644 "$here/$name"
done
exit "$drift"
