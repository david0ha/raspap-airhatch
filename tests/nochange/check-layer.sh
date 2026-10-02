#!/bin/bash
# yocto-check-layer on meta-raspap-airhatch with meta-raspap and its
# dependencies as --dependency. Every test must pass, test_signatures
# included. It runs in its own build directory: poky on qemux86-64 with the
# shared download and sstate directories and small build limits, without
# any site configuration.
# Env: META_RASPAP, YOCTO_DIR, SHARED_DL_DIR, SHARED_SSTATE_DIR (required);
#      CHECK_LAYER_BUILD (default $BASE_BUILD_DIR/check-layer-airhatch).
# Usage: check-layer.sh
set -euo pipefail

# shellcheck source=SCRIPTDIR/../lib.sh
. "${BASH_SOURCE[0]%/*}/../lib.sh"

require_env YOCTO_DIR SHARED_DL_DIR SHARED_SSTATE_DIR
[[ -n ${CHECK_LAYER_BUILD:-} ]] || require_env BASE_BUILD_DIR
BUILD=${CHECK_LAYER_BUILD:-$BASE_BUILD_DIR/check-layer-airhatch}
LAYER=$AIRHATCH_DIR/meta-raspap-airhatch
DEPENDENCIES=(
    "$META_RASPAP"
    "$YOCTO_DIR/meta-openembedded/meta-oe"
    "$YOCTO_DIR/meta-openembedded/meta-python"
    "$YOCTO_DIR/meta-openembedded/meta-networking"
)

write_conf() {
    mkdir -p "$BUILD/conf"
    cat >"$BUILD/conf/bblayers.conf" <<EOF
LCONF_VERSION = "7"
BBPATH = "\${TOPDIR}"
BBFILES ?= ""
BBLAYERS = " \\
  $YOCTO_DIR/openembedded-core/meta \\
  $YOCTO_DIR/meta-yocto/meta-poky \\
"
EOF
    cat >"$BUILD/conf/local.conf" <<EOF
MACHINE = "qemux86-64"
DISTRO = "poky"
CONF_VERSION = "2"
BB_NUMBER_THREADS = "3"
BB_NUMBER_PARSE_THREADS = "2"
PARALLEL_MAKE = "-j 4"
DL_DIR = "$SHARED_DL_DIR"
SSTATE_DIR = "$SHARED_SSTATE_DIR"
EOF
}

# test_results <log>: one "<test> <result>" line per test. yocto-check-layer
# logs the test name and, on the next line, " ... ok", " ... FAIL",
# " ... ERROR" or " ... skipped <reason>".
test_results() {
    awk '/^INFO: [a-zA-Z_]+ \(/ { name = $2; next }
        name != "" && /^INFO:  \.\.\. / { print name, $3; name = "" }' "$1"
}

# The dependencies set up the build only; --no-auto-dependency keeps
# yocto-check-layer from testing them as well.
main() {
    (($# == 0)) || die "usage: ${0##*/}"
    bitbake_is_running && die "another BitBake is running; run this alone"
    write_conf
    local log=$BUILD/yocto-check-layer.log status=0 results
    rm -f "$log"
    in_build_env "$BUILD" yocto-check-layer --output-log "$log" --no-auto-dependency \
        --dependency "${DEPENDENCIES[@]}" -- "$LAYER" || status=$?
    results=$(test_results "$log")
    check "yocto-check-layer exit status 0 (was $status)" test "$status" = 0
    check "meta-raspap-airhatch ... PASS" grep -qxF 'INFO: meta-raspap-airhatch ... PASS' "$log"
    check "test_signatures ok" grep -qxF 'test_signatures ok' <<<"$results"
    check "no FAIL or ERROR result" test -z "$(grep -vE ' (ok|skipped)$' <<<"$results" || true)"
    awk '{ print "    " $0 }' <<<"$results"
    report
}

main "$@"
