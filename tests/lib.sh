# shellcheck shell=bash
# Helpers shared by the raspap-airhatch test scripts. The check, report and
# image helpers are meta-raspap's own (tests/lib.sh), reused read-only.
#
# Environment (CONTRIBUTING.md lists every variable):
#   META_RASPAP   a meta-raspap checkout (required by every script)
#   RASPAP_GIT    a raspap-webgui clone, PLUGINS_GIT a RaspAP/plugins clone:
#                 required where meta-raspap's static suite runs
#   YOCTO_DIR     the Yocto tree (bitbake/, openembedded-core/, meta-yocto/,
#                 meta-openembedded/): required by the tests/nochange scripts

# require_env <name...>: fails with a clear message unless every named
# variable is set and not empty.
require_env() {
    local name
    for name; do
        if [[ -z ${!name:-} ]]; then
            echo "error: set $name (see CONTRIBUTING.md)" >&2
            exit 1
        fi
    done
}

# shellcheck disable=SC2034 # used by the scripts that source this file
AIRHATCH_DIR=$(cd "${BASH_SOURCE[0]%/*}/.." && pwd)
require_env META_RASPAP

# shellcheck source=/dev/null
. "$META_RASPAP/tests/lib.sh"

# meta-raspap's suite compares the image with these clones (its README);
# scripts that run it call require_env RASPAP_GIT PLUGINS_GIT.
export RASPAP_GIT PLUGINS_GIT

require_tarball() {
    [[ $# -eq 1 && -f $1 ]] || die "usage: ${0##*/} <rootfs.tar.bz2>"
}

# meta_raspap_array <name>: prints the elements of a bash array that
# meta-raspap's tests/run-static.sh defines, one per line, so both suites
# share one list.
meta_raspap_array() {
    local name=$1 definition
    definition=$(sed -n "/^$1=(/,/)/p" "$META_RASPAP/tests/run-static.sh")
    [[ -n $definition ]] || die "no array $name in $META_RASPAP/tests/run-static.sh"
    (
        eval "$definition"
        local -n array=$name
        printf '%s\n' "${array[@]}"
    )
}

# wait_until <seconds> <command...>: polls the command once a second until
# it succeeds; fails when the time is up.
wait_until() {
    local deadline=$((SECONDS + $1))
    shift
    until "$@"; do
        ((SECONDS < deadline)) || return 1
        sleep 1
    done
}

# Any BitBake process of this Yocto tree (bitbake, its server and workers,
# bitbake-layers, bitbake-getvar): Python running one of its scripts. The
# host has the RAM for one at a time.
bitbake_is_running() {
    pgrep -f "^([^ ]*/)?python3[.0-9]* $YOCTO_DIR/bitbake/bin/" >/dev/null
}

# in_build_env <build dir> <command...>: runs the command in that build
# directory with the BitBake environment set up.
in_build_env() {
    local build=$1
    shift
    (
        # oe-init-build-env is not written for "set -u".
        set +u
        # shellcheck source=/dev/null
        . "$YOCTO_DIR/openembedded-core/oe-init-build-env" "$build" >/dev/null
        set -u
        "$@"
    )
}
