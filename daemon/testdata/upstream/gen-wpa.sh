#!/bin/bash
# Produces the wpa_supplicant-*.conf fixtures with RaspAP's own
# WiFiManager::writeWpaSupplicant (and ensureWpaSupplicant for the empty file),
# run by the image's php and wpa_passphrase inside a meta-raspap rootfs:
#   upstream/gen-wpa.sh <build>/tmp/deploy/images/<machine>/core-image-base-<machine>.rootfs.tar.bz2
# sudo is replaced by a shim that runs the command without sudo.
set -euo pipefail

(($# == 1)) || { echo "usage: $0 <rootfs.tar.bz2>" >&2; exit 2; }
here=$(cd "${BASH_SOURCE[0]%/*}" && pwd)
root=$(mktemp -d "${TMPDIR:-/tmp}/airhatch-gen-wpa.XXXXXX")
cleanup() {
    chmod -R u+rwX "$root" 2>/dev/null || true
    rm -rf "$root"
}
trap cleanup EXIT

tar --numeric-owner -xjpf "$1" -C "$root" --no-same-owner --exclude='./dev/*'
mkdir -p "$root/opt/gen-wpa"
cat > "$root/opt/gen-wpa/sudo" <<'EOF'
#!/bin/sh
exec "$@"
EOF
chmod 0755 "$root/opt/gen-wpa/sudo"

cat > "$root/opt/gen-wpa/gen.php" <<'EOF'
<?php
define('RASPI_WPA_SUPPLICANT_CONFIG', '/etc/wpa_supplicant/wpa_supplicant.conf');
define('RASPI_WPA_CTRL_INTERFACE', '/var/run/wpa_supplicant');
require '/var/www/html/includes/functions.php';
require '/var/www/html/src/RaspAP/Networking/Hotspot/WiFiManager.php';

$cases = [
    'one-wpa' => [
        'Home' => ['protocol' => 'WPA', 'passphrase' => 'correct horse battery'],
    ],
    'wpa-open' => [
        'Home' => ['protocol' => 'WPA', 'passphrase' => 'correct horse battery'],
        'Cafe Free' => ['protocol' => 'OPEN', 'passphrase' => ''],
    ],
    'hex-ssid' => [
        'Caf\xc3\xa9' => ['protocol' => 'WPA', 'passphrase' => 'croissant42'],
        'Gr\xc3\xbcn' => ['protocol' => 'OPEN', 'passphrase' => ''],
    ],
    'passkey' => [
        'Office' => ['protocol' => 'WPA', 'passphrase' => '',
            'passkey' => '8c1f0c9b3e5b7d2a4f6e8a0c2e4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b'],
    ],
    'priorities' => [
        'Home' => ['protocol' => 'WPA', 'passphrase' => 'correct horse battery', 'priority' => 5],
        'Office' => ['protocol' => 'WPA', 'passphrase' => 'paperclips!', 'priority' => 1],
    ],
];

$manager = new \RaspAP\Networking\Hotspot\WiFiManager();
@unlink(RASPI_WPA_SUPPLICANT_CONFIG);
$manager->ensureWpaSupplicant();
copy(RASPI_WPA_SUPPLICANT_CONFIG, '/opt/gen-wpa/out/wpa_supplicant-empty.conf');
foreach ($cases as $name => $networks) {
    @unlink(RASPI_WPA_SUPPLICANT_CONFIG);
    $manager->writeWpaSupplicant($networks, 'wlan0');
    if (!copy(RASPI_WPA_SUPPLICANT_CONFIG, "/opt/gen-wpa/out/wpa_supplicant-$name.conf")) {
        fwrite(STDERR, "writeWpaSupplicant wrote nothing for $name\n");
        exit(1);
    }
}
EOF
mkdir -p "$root/opt/gen-wpa/out"

# writeWpaSupplicant ends with `wpa_cli reconfigure`, which finds no supplicant
# in the chroot; its "Failed to connect" lines are expected.
unshare -r env -i PATH=/opt/gen-wpa:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C \
    chroot "$root" php /opt/gen-wpa/gen.php
for file in "$root"/opt/gen-wpa/out/*.conf; do
    install -m 0644 "$file" "$here/${file##*/}"
done
