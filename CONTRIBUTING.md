# Contributing

Bug reports and pull requests are welcome at
<https://github.com/david0ha/raspap-airhatch>. Report vulnerabilities
privately as described in [SECURITY.md](SECURITY.md), not in a public issue.

Keep changes small and covered by tests. RaspAP stays unpatched: a change
that would patch RaspAP code belongs upstream, and every change to
meta-raspap's RaspAP integration needs an entry with its reason and evidence
in [docs/integration.md](docs/integration.md). Tests are never relaxed to
make a change pass. Add an entry to [CHANGELOG.md](CHANGELOG.md) for
user-visible changes.

## Daemon (Go)

Go 1.26, standard library only:

```
cd daemon
gofmt -l .          # must print nothing
go vet ./...
go test -race ./...
```

The fixtures in `daemon/testdata/upstream/` are byte copies of RaspAP 3.5.7
files. `daemon/testdata/upstream/sync.sh` recreates them from a raspap-webgui
clone (`RASPAP_GIT`) and fails on drift; `gen-wpa.sh` and
`daemon/testdata/image/sync.sh` regenerate the others from a meta-raspap
rootfs tarball.

## Plugin (PHP)

PHP 8.4 or newer and shellcheck:

```
git clone --depth 1 --branch 3.5.7 https://github.com/RaspAP/raspap-webgui.git /path/to/raspap-webgui
RASPAP_GIT=/path/to/raspap-webgui plugin/tests/run.sh
```

`PHP` and `SHELLCHECK` select other binaries (default `php` and
`shellcheck`).

## Shell

Every shell script is bash with `set -euo pipefail` and must stay
shellcheck-clean:

```
shellcheck tests/*.sh tests/*/*.sh plugin/tests/run.sh daemon/testdata/*/*.sh \
    meta-raspap-airhatch/recipes-webadmin/raspap/files/servicestart.sh
```

## Image tests

These run against images and builds, not in CI. They need a
[meta-raspap](https://github.com/david0ha/meta-raspap) checkout, whose test
helpers they reuse, and run the image's own binaries, so run them on a host
of the image's architecture (or with qemu-user binfmt handlers). No variable
has a machine-specific default; a script that needs an unset one stops with
an error.

| Variable | Needed by | Value |
|---|---|---|
| `META_RASPAP` | every script in `tests/` | a meta-raspap checkout |
| `RASPAP_GIT` | `tests/static/run-base.sh`, `tests/nochange/run.sh`, `plugin/tests/run.sh` | a clone of <https://github.com/RaspAP/raspap-webgui> that contains tag 3.5.7 |
| `PLUGINS_GIT` | `tests/static/run-base.sh`, `tests/nochange/run.sh` | a clone of <https://github.com/RaspAP/plugins> with the submodule commit pinned at 3.5.7 (BitBake's mirror in `${DL_DIR}/git2/github.com.RaspAP.plugins` works) |
| `GO` | `tests/smoke/run.sh` | Go 1.26 (default `go`) |
| `YOCTO_DIR` | `tests/nochange/*` | the Yocto tree, holding `bitbake/`, `openembedded-core/`, `meta-yocto/` and `meta-openembedded/` |
| `BASE_BUILD_DIR` | `tests/nochange/run.sh`, and `check-layer.sh` unless `CHECK_LAYER_BUILD` is set | a build directory with meta-raspap and without this layer |
| `BASE_MACHINE` | `tests/nochange/run.sh` | that build's `MACHINE` (default `raspberrypi-cm5-io-board`) |
| `UNCHANGED_CONF_DIR` | `tests/nochange/run.sh` (optional) | a directory whose files must also be unchanged after the run, for example the `conf/` of another build that shares the caches |
| `SHARED_DL_DIR`, `SHARED_SSTATE_DIR` | `tests/nochange/check-layer.sh` | `DL_DIR` and `SSTATE_DIR` for its own build directory |
| `CHECK_LAYER_BUILD` | `tests/nochange/check-layer.sh` (optional) | its build directory (default `$BASE_BUILD_DIR/check-layer-airhatch`) |

With an image built with the `airhatch` feature:

```
tests/static/run-base.sh <rootfs.tar.bz2>   # meta-raspap's suite fails on exactly the documented overrides
tests/static/run.sh <rootfs.tar.bz2>        # shipped files, units, sudoers, dnsmasq and dhcpcd renders
tests/smoke/run.sh <rootfs.tar.bz2>         # Docker, systemd as PID 1, veth radios
```

In the base build (meta-raspap, no add-on), one BitBake at a time:

```
tests/nochange/run.sh
tests/nochange/check-layer.sh
```

`tests/nochange/run.sh` adds the layer to `$BASE_BUILD_DIR/conf/bblayers.conf`
for the run, restores the file on exit and verifies the restore by checksum
(and `UNCHANGED_CONF_DIR`, when set). Its logs stay in
`$BASE_BUILD_DIR/airhatch-nochange/`. [docs/DESIGN.md](docs/DESIGN.md) §11
describes what each suite checks.

## Building from a local checkout

The recipe fetches this repository from GitHub at `RASPAP_AIRHATCH_SRCREV`.
To build your own commits, point it at your checkout in `local.conf`:

```
RASPAP_AIRHATCH_GIT_URI = "git:///path/to/raspap-airhatch;protocol=file;branch=main"
RASPAP_AIRHATCH_SRCREV = "<full hash of the commit to build>"
```

Only committed changes are built.

## Bumping the source revision

The recipe pins the sources it builds. For a release, or whenever `daemon/`,
`plugin/` or `system/` changed:

1. Merge the source change to `main`.
2. Set `RASPAP_AIRHATCH_SRCREV` in
   `meta-raspap-airhatch/recipes-connectivity/raspap-airhatch/raspap-airhatch_git.bb`
   to that commit's full hash; for a release, also update `PV` and
   [CHANGELOG.md](CHANGELOG.md).
3. Rebuild the image and run the image tests (`tests/static/run.sh` checks
   that the shipped files equal the checkout, so it needs the bumped image).
4. Commit the recipe change on its own. The pin always names an earlier
   commit than the one that sets it.
