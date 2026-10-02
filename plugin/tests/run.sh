#!/bin/bash
# Plugin tests (docs/API.md §7.4): lint every PHP file, then run the harness
# with and without RaspAP's monitor mode.
# Env: RASPAP_GIT (required), a clone of https://github.com/RaspAP/raspap-webgui
#      that contains tag 3.5.7;
#      PHP (default php), SHELLCHECK (default shellcheck).
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
php=${PHP:-php}
raspap_git=${RASPAP_GIT:?set RASPAP_GIT to a raspap-webgui clone that contains tag 3.5.7}

"${SHELLCHECK:-shellcheck}" "$here/run.sh"

while IFS= read -r -d '' file; do
    "$php" -l "$file"
done < <(find "$here/.." -name '*.php' -print0 | sort -z)

"$php" "$here/harness.php" "$raspap_git" normal
"$php" "$here/harness.php" "$raspap_git" monitor
