#!/bin/bash
# Stands in for sudo when wifimanager-harness.php runs as root: appends the
# command line to $SUDO_SHIM_LOG, then runs the command.
set -euo pipefail

printf '%s\n' "$*" >>"${SUDO_SHIM_LOG:?set SUDO_SHIM_LOG to the log file}"
exec "$@"
