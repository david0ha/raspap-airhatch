<?php

/**
 * Runs upstream RaspAP's WiFiManager from the image's webroot the way the
 * WiFi client page does (docs/DESIGN.md §11.2), so the smoke test can drive RaspAP's
 * real code against airhatchd. Run it as root with sudo-shim.sh on PATH as
 * "sudo": RaspAP's sudo commands then run directly and are logged.
 *
 * Usage: php wifimanager-harness.php known|connected|nearby
 *        php wifimanager-harness.php write <ssid> [<passphrase>]
 *
 * known      setKnownStationsWPA, then prints "<ssid>\t<index>" per network
 * connected  prints the SSIDs that connectedWifiStations marks connected
 * nearby     prints the SSIDs of nearbyWifiStations (the scan cache first)
 * write      writeWpaSupplicant with only this network (open without a
 *            passphrase), as a Save on the page with one network does
 */

declare(strict_types=1);

use RaspAP\Networking\Hotspot\WiFiManager;

const WEBROOT = '/var/www/html';
const IFACE = 'wlan0';

chdir(WEBROOT);
require WEBROOT . '/includes/config.php';
require WEBROOT . '/includes/autoload.php';
require WEBROOT . '/includes/functions.php';

$_SESSION['wifi_client_interface'] = IFACE;
$_SESSION['ap_interface'] = IFACE;

function knownNetworks(WiFiManager $wifi): array
{
    $networks = [];
    $wifi->knownWifiStations($networks);
    return $networks;
}

function printLines(array $lines): void
{
    foreach ($lines as $line) {
        echo $line, "\n";
    }
}

function usage(): never
{
    fwrite(STDERR, "usage: wifimanager-harness.php known|connected|nearby|write <ssid> [<passphrase>]\n");
    exit(2);
}

$wifi = new WiFiManager();
switch ($argv[1] ?? '') {
    case 'known':
        $networks = knownNetworks($wifi);
        $wifi->setKnownStationsWPA($networks);
        printLines(array_map(
            fn(array $network): string => $network['ssid'] . "\t" . var_export($network['index'], true),
            array_values($networks)
        ));
        break;
    case 'connected':
        $networks = knownNetworks($wifi);
        $wifi->connectedWifiStations($networks);
        printLines(array_keys(array_filter($networks, fn(array $network): bool => $network['connected'])));
        break;
    case 'nearby':
        $networks = [];
        $wifi->nearbyWifiStations($networks);
        printLines(array_keys($networks));
        break;
    case 'write':
        $ssid = $argv[2] ?? usage();
        $passphrase = $argv[3] ?? '';
        $network = $passphrase === ''
            ? ['protocol' => WiFiManager::SECURITY_OPEN]
            : ['protocol' => 'WPA', 'passphrase' => $passphrase];
        $result = $wifi->writeWpaSupplicant([$ssid => $network], IFACE);
        echo $result['message'], "\n";
        exit($result['success'] ? 0 : 1);
    default:
        usage();
}
