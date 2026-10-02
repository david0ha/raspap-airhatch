#!/bin/bash
# Recreates image/lighttpd.conf from a meta-raspap rootfs tarball:
#   image/sync.sh <build>/tmp/deploy/images/<machine>/core-image-base-<machine>.rootfs.tar.bz2
set -euo pipefail

(($# == 1)) || { echo "usage: $0 <rootfs.tar.bz2>" >&2; exit 2; }
here=$(cd "${BASH_SOURCE[0]%/*}" && pwd)
tar -xjOf "$1" ./etc/lighttpd/lighttpd.conf > "$here/lighttpd.conf"
chmod 0644 "$here/lighttpd.conf"
