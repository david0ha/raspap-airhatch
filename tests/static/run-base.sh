#!/bin/bash
# meta-raspap's static suite on an image built with the airhatch feature
# (docs/DESIGN.md §11.2). It must fail on exactly the labels of docs/integration.md §3,
# and the two webroot failures may list only the plugin.
# Env: META_RASPAP, RASPAP_GIT, PLUGINS_GIT (required; see CONTRIBUTING.md).
# Usage: run-base.sh <rootfs.tar.bz2>
set -euo pipefail

# shellcheck source=SCRIPTDIR/../lib.sh
. "${BASH_SOURCE[0]%/*}/../lib.sh"

EXPECTED_FAILURES=(
    'content /etc/raspap/hostapd/servicestart.sh = installers/servicestart.sh'
    'webroot differs from upstream'
    'webroot ownership'
)
WEBROOT_DIFF_LINE='Only in <webroot>/plugins: Airhatch'
# Upstream has no mode for the plugin's files, so meta-raspap wants "".
WEBROOT_MODE_LINE='^/var/www/html/plugins/Airhatch/[^ ]+: mode 0[0-7]{3}, want $'

# failure_labels <suite output>: the label of every FAIL line, which is the
# text before a " (" count or detail.
failure_labels() {
    sed -n 's/^FAIL //p' <<<"$1" | sed 's/ (.*//' | sort
}

# listed_lines <suite output> <label>: the indented lines printed under the
# FAIL line of that label.
listed_lines() {
    awk -v label="$2" '
        /^FAIL / { on = index($0, "FAIL " label " (") == 1; next }
        /^    / { if (on) print substr($0, 5); next }
        { on = 0 }' <<<"$1"
}

# listed_count <suite output> <label>: the count in "FAIL <label> (<n>)".
listed_count() {
    sed -n "s/^FAIL $2 (\([0-9]*\))\$/\1/p" <<<"$1"
}

# check_listed <suite output> <label> <grep -E pattern>: every listed line
# matches, and the list is complete (meta-raspap prints at most 40 lines).
check_listed() {
    local lines count unexpected
    lines=$(listed_lines "$1" "$2")
    if [[ -z $lines ]]; then
        fail "'$2': no FAIL line with listed lines"
        return
    fi
    count=$(listed_count "$1" "$2")
    check "'$2': all $count lines listed" test "$(wc -l <<<"$lines")" = "$count"
    unexpected=$(grep -vE -- "$3" <<<"$lines" || true)
    if [[ -z $unexpected ]]; then
        pass "'$2': every line is the Airhatch plugin"
    else
        fail "'$2': lines not caused by the plugin"
        awk '{ print "    " $0 }' <<<"$unexpected"
    fi
}

main() {
    require_tarball "$@"
    require_env RASPAP_GIT PLUGINS_GIT
    local output summary expected
    output=$("$META_RASPAP/tests/run-static.sh" "$1" 2>&1) || true
    summary=$(grep -E '^[0-9]+ passed, [0-9]+ failed' <<<"$output" || true)
    check "meta-raspap run-static.sh ran to its summary: ${summary:-none}" test -n "$summary"

    expected=$(printf '%s\n' "${EXPECTED_FAILURES[@]}" | sort)
    if [[ $(failure_labels "$output") == "$expected" ]]; then
        pass "meta-raspap fails exactly on the documented overrides (integration.md §3)"
    else
        fail "meta-raspap failures differ from integration.md §3"
        diff <(echo "$expected") <(failure_labels "$output") | sed -n 's/^[<>]/    &/p' || true
    fi

    check_listed "$output" 'webroot differs from upstream' "^${WEBROOT_DIFF_LINE}\$"
    check_listed "$output" 'webroot ownership' "$WEBROOT_MODE_LINE"
    report
}

main "$@"
