#!/bin/bash
# No-change proof: adding meta-raspap-airhatch to a meta-raspap build
# without the airhatch feature changes no package or rootfs task signature,
# leaves the image manifest byte-identical and meta-raspap's static suite at
# 333/333.
# The one signature input any added layer changes is BBLAYERS in
# core-image-base:do_rootfs_wicenv (wic searches every layer for .wks files),
# so that task and the image tasks after it get new signatures and rerun.
# The script proves with bitbake-diffsigs that BBLAYERS is the only cause.
# The base build directory's conf/bblayers.conf is changed during the run and
# restored on exit; the restore is verified by checksum.
# Env: META_RASPAP, RASPAP_GIT, PLUGINS_GIT, YOCTO_DIR (required);
#      BASE_BUILD_DIR (required): a build directory with meta-raspap and
#        without this layer, whose image the script builds;
#      BASE_MACHINE (default raspberrypi-cm5-io-board): that build's MACHINE;
#      UNCHANGED_CONF_DIR (optional): a directory whose files must also be
#        unchanged after the run, such as the conf of a build that shares
#        DL_DIR and SSTATE_DIR.
# Usage: run.sh
set -euo pipefail

# shellcheck source=SCRIPTDIR/../lib.sh
. "${BASH_SOURCE[0]%/*}/../lib.sh"

require_env RASPAP_GIT PLUGINS_GIT YOCTO_DIR BASE_BUILD_DIR
BUILD=$BASE_BUILD_DIR
LAYER=$AIRHATCH_DIR/meta-raspap-airhatch
TARGET=core-image-base
MACHINE=${BASE_MACHINE:-raspberrypi-cm5-io-board}
MACHINE_DEPLOY=$BUILD/tmp/deploy/images/$MACHINE
MANIFEST=$MACHINE_DEPLOY/$TARGET-$MACHINE.rootfs.manifest
TARBALL=$MACHINE_DEPLOY/$TARGET-$MACHINE.rootfs.tar.bz2
STATIC_RESULT='333 passed, 0 failed'
# Logs and the saved state of a run, inside the build directory.
LOGS=$BUILD/airhatch-nochange

bitbake_in_build() {
    in_build_env "$BUILD" "$@"
}

# conf_checksums: the base build's bblayers.conf and, when set, every file
# of UNCHANGED_CONF_DIR.
conf_checksums() {
    local files=("$BUILD/conf/bblayers.conf")
    if [[ -n ${UNCHANGED_CONF_DIR:-} ]]; then
        files+=("$UNCHANGED_CONF_DIR"/*)
    fi
    sha256sum "${files[@]}"
}

restore_bblayers() {
    cp "$LOGS/bblayers.conf" "$BUILD/conf/bblayers.conf"
    if conf_checksums | cmp -s "$LOGS/conf.sha256" -; then
        echo "$BUILD/conf/bblayers.conf restored${UNCHANGED_CONF_DIR:+; $UNCHANGED_CONF_DIR unchanged}"
    else
        echo "error: $BUILD/conf/bblayers.conf${UNCHANGED_CONF_DIR:+ or $UNCHANGED_CONF_DIR} differ from before the run" >&2
        exit 1
    fi
}

# run_logged <log name> <command...>: runs the command in the base build,
# keeping its output in $LOGS; shows the end of the log when it fails.
run_logged() {
    local log=$LOGS/$1.log
    shift
    if bitbake_in_build "$@" >"$log" 2>&1; then
        return 0
    fi
    echo "error: $* failed; end of $log:" >&2
    tail -n 30 "$log" >&2
    return 1
}

# locked_sigs <name>: the task signatures of $TARGET as $LOGS/<name>.inc.
locked_sigs() {
    run_logged "sigs-$1" bitbake -S lockedsigs "$TARGET" &&
        mv "$BUILD/locked-sigs.inc" "$LOGS/$1.inc"
}

# The task that records BBLAYERS for wic, and the only one whose own
# signature inputs may change.
WICENV_TASK=$TARGET:do_rootfs_wicenv

# changed_tasks: "<recipe>:<task> <hash before> <hash after>" for every task
# whose locked signature differs.
changed_tasks() {
    join <(locked_hashes "$LOGS/before.inc") <(locked_hashes "$LOGS/after.inc") |
        awk '$2 != $3'
    # A task present on one side only is a change too.
    join -v1 -v2 <(locked_hashes "$LOGS/before.inc") <(locked_hashes "$LOGS/after.inc") |
        awk '{ print $1, "missing", "missing" }'
}

# locked_hashes <locked-sigs.inc>: "<recipe>:<task> <hash>" of the
# SIGGEN_LOCKEDSIGS_* lists (not the "task:hash:unihash" lines of
# SIGGEN_UNIHASHMAP).
locked_hashes() {
    sed -nE 's/^ +([^ :]+:do_[a-z0-9_]+):([0-9a-f]{64}) \\$/\1 \2/p' "$1" | sort
}

# sigdata <recipe:task> <hash>: the task's signature file in the base build.
sigdata() {
    local recipe=${1%%:*} task=${1#*:}
    find "$BUILD/tmp/stamps" -path "*/$recipe/*" -name "*.$task.sigdata.$2" -print -quit
}

# only_wicenv_bblayers: every changed task is one of $TARGET's, and
# bitbake-diffsigs of each finds no changed input except BBLAYERS in
# $WICENV_TASK; the others change only through their dependency on it.
only_wicenv_bblayers() {
    local task before after old new out bad=0
    [[ -s $LOGS/changed-tasks ]] || return 1
    while read -r task before after; do
        if [[ $task != "$TARGET":* || $before == missing ]]; then
            echo "    $task: not an image task or added/removed" >&2
            bad=1
            continue
        fi
        old=$(sigdata "$task" "$before")
        new=$(sigdata "$task" "$after")
        if [[ -z $old || -z $new ]]; then
            echo "    $task: no sigdata for $before or $after" >&2
            bad=1
            continue
        fi
        out=$(bitbake_in_build bitbake-diffsigs "$old" "$new" 2>&1 | grep -v '^NOTE: ') || true
        printf '%s\n' "$out" >>"$LOGS/diffsigs.log"
        if [[ $task != "$WICENV_TASK" ]] && ! head -n 1 <<<"$out" | grep -q '^Hash for task dependency '; then
            echo "    $task: its own inputs changed" >&2
            bad=1
        fi
        # Allowed lines: a changed dependency of $TARGET, the basehash of
        # $WICENV_TASK, the BBLAYERS change and its quoted value.
        if grep -vE "^ *(Hash for task dependency $TARGET:do_[a-z0-9_]+ changed from|basehash changed from|Variable BBLAYERS value changed:|\")" <<<"$out" | grep -q .; then
            echo "    $task: a change other than BBLAYERS (see $LOGS/diffsigs.log)" >&2
            bad=1
        fi
        # A basehash line belongs to the task named on the line above it, or
        # to the compared task itself when it is the first line.
        if ! awk -v wicenv="$WICENV_TASK" -v task="$task" '
            /basehash changed from/ {
                owner = NR == 1 ? task : previous
                if (NR > 1) { sub(/^ *Hash for task dependency /, "", owner); sub(/ changed from.*/, "", owner) }
                if (owner != wicenv) bad = 1
            }
            { previous = $0 }
            END { exit bad }' <<<"$out"; then
            echo "    $task: a basehash other than $WICENV_TASK's changed" >&2
            bad=1
        fi
    done <"$LOGS/changed-tasks"
    return "$bad"
}

# only_changed_tasks_ran <log>: the build executed no task outside the
# changed set (setscene tasks do not count).
only_changed_tasks_ran() {
    local ran
    ran=$(sed -nE 's/^NOTE: recipe ([^ ]+)-[^- ]+-r[0-9]+: task (do_[a-z0-9_]+): Started$/\1:\2/p' "$1" | sort -u)
    [[ -z $(comm -23 <(printf '%s\n' "$ran" | sed '/^$/d') <(cut -d ' ' -f 1 "$LOGS/changed-tasks" | sort -u)) ]]
}

distro_features_lack_airhatch() {
    local features
    features=$(bitbake_in_build bitbake-getvar --value DISTRO_FEATURES 2>/dev/null) || return 1
    [[ -n $features && " $features " != *' airhatch '* ]]
}

layer_is_added() {
    grep -qF "$LAYER" "$BUILD/conf/bblayers.conf"
}

meta_raspap_static() {
    "$META_RASPAP/tests/run-static.sh" "$TARBALL" >"$LOGS/run-static.log" 2>&1 || true
    grep -qxF "$STATIC_RESULT" "$LOGS/run-static.log"
}

main() {
    (($# == 0)) || die "usage: ${0##*/}"
    [[ -f $BUILD/conf/bblayers.conf ]] || die "$BUILD/conf/bblayers.conf not found; set BASE_BUILD_DIR"
    if [[ -n ${UNCHANGED_CONF_DIR:-} ]]; then
        [[ -d $UNCHANGED_CONF_DIR ]] || die "UNCHANGED_CONF_DIR $UNCHANGED_CONF_DIR is not a directory"
    fi
    bitbake_is_running && die "another BitBake is running; run this alone"
    [[ ! -e $BUILD/locked-sigs.inc ]] || die "$BUILD/locked-sigs.inc exists; move it away first"
    layer_is_added && die "$LAYER is already in $BUILD/conf/bblayers.conf"
    mkdir -p "$LOGS"
    cp "$BUILD/conf/bblayers.conf" "$LOGS/bblayers.conf"
    conf_checksums >"$LOGS/conf.sha256"
    trap restore_bblayers EXIT
    trap 'exit 130' INT TERM

    # Bring the image up to date first, so the comparison measures only the layer.
    run_logged baseline-build bitbake "$TARGET" || die "baseline build failed"
    cp "$MANIFEST" "$LOGS/manifest.before"
    readlink "$MANIFEST" >"$LOGS/manifest-link.before"
    locked_sigs before || die "signatures without the layer failed"

    run_logged add-layer bitbake-layers add-layer "$LAYER" || die "bitbake-layers add-layer failed"
    check "layer added to the base build" layer_is_added
    check "DISTRO_FEATURES lacks airhatch" distro_features_lack_airhatch

    if locked_sigs after; then
        changed_tasks >"$LOGS/changed-tasks"
        : >"$LOGS/diffsigs.log"
        echo "    $(wc -l <"$LOGS/changed-tasks") of $(locked_hashes "$LOGS/before.inc" | wc -l) locked signatures differ:"
        awk '{ print "      " $1 }' "$LOGS/changed-tasks"
        check "only $WICENV_TASK and the image tasks after it differ, by BBLAYERS alone" \
            only_wicenv_bblayers
    else
        fail "signatures with the layer"
    fi

    if run_logged build bitbake "$TARGET"; then
        check "bitbake $TARGET ran only those image tasks" only_changed_tasks_ran "$LOGS/build.log"
    else
        fail "bitbake $TARGET with the layer"
    fi
    check "image manifest unchanged" cmp -s "$LOGS/manifest.before" "$MANIFEST"
    check "no new image (same manifest file)" test "$(readlink "$MANIFEST")" = "$(cat "$LOGS/manifest-link.before")"
    check "meta-raspap run-static.sh: $STATIC_RESULT" meta_raspap_static
    report
}

main "$@"
