<?php

/**
 * Plugin harness (docs/API.md §7.4): runs the Airhatch plugin against
 * RaspAP's own plugin, sidebar, message and CSRF classes from the upstream tag
 * and a fake airhatchd.
 *
 * Usage: php harness.php <raspap-git-dir> normal|monitor
 * "monitor" runs with RASPI_MONITOR_ENABLED (a constant, hence a second run).
 */

declare(strict_types=1);

use RaspAP\Auth\HTTPAuth;
use RaspAP\Plugins\Airhatch\Airhatch;
use RaspAP\Plugins\Airhatch\DaemonClient;
use RaspAP\UI\Sidebar;

const RASPAP_TAG = '8a201504';
const UPSTREAM_FILES = [
    'src/RaspAP/Plugins/PluginInterface.php',
    'src/RaspAP/UI/Sidebar.php',
    'src/RaspAP/Messages/StatusMessage.php',
    'src/RaspAP/Tokens/CSRFTokenizer.php',
    'src/RaspAP/Auth/HTTPAuth.php',
    'includes/CSRF.php',
];
const STATUS_LINE = "{\"op\":\"status\"}\n";

[, $raspapGit, $mode] = $argv + [null, null, null];
if ($raspapGit === null || !in_array($mode, ['normal', 'monitor'], true)) {
    fwrite(STDERR, "usage: php harness.php <raspap-git-dir> normal|monitor\n");
    exit(2);
}

$work = makeWorkdir();
$upstream = "$work/upstream";
extractUpstream($raspapGit, $upstream);
bootRaspAP($upstream, $work, $mode === 'monitor');
enterWebroot(dirname(__DIR__), $work);
require_once 'plugins/Airhatch/Airhatch.php';

$failures = 0;
$mode === 'monitor' ? monitorSuite($work) : normalSuite($work);
echo $failures === 0 ? "harness ($mode): all checks passed\n" : "harness ($mode): $failures check(s) failed\n";
exit($failures === 0 ? 0 : 1);

function check(string $name, bool $ok): void
{
    global $failures;
    echo ($ok ? 'PASS ' : 'FAIL ') . $name . "\n";
    $failures += $ok ? 0 : 1;
}

function makeWorkdir(): string
{
    $work = sys_get_temp_dir() . '/airhatch-plugin-' . bin2hex(random_bytes(6));
    mkdir($work, 0700);
    register_shutdown_function(fn () => exec('rm -rf ' . escapeshellarg($work)));
    return $work;
}

function extractUpstream(string $raspapGit, string $dir): void
{
    foreach (UPSTREAM_FILES as $path) {
        $cmd = sprintf('git -C %s show %s', escapeshellarg($raspapGit), escapeshellarg(RASPAP_TAG . ':' . $path));
        exec($cmd, $lines, $code);
        if ($code !== 0) {
            fwrite(STDERR, "cannot read $path at " . RASPAP_TAG . " from $raspapGit\n");
            exit(2);
        }
        if (!is_dir(dirname("$dir/$path"))) {
            mkdir(dirname("$dir/$path"), 0700, true);
        }
        file_put_contents("$dir/$path", implode("\n", $lines) . "\n");
        $lines = [];
    }
}

/**
 * Recreates the part of RaspAP's request bootstrap the plugin relies on:
 * config constants, gettext, the session with its CSRF token, and the classes.
 */
function bootRaspAP(string $upstream, string $work, bool $monitor): void
{
    // Every reported warning is a failure, except deprecations raised by
    // upstream code (Sidebar's implicitly nullable parameter). Errors silenced
    // with @ stay silent, as under RaspAP, which installs no error handler.
    set_error_handler(function (int $level, string $message, string $file, int $line) use ($upstream): bool {
        $silenced = (error_reporting() & $level) === 0;
        if ($silenced || ($level === E_DEPRECATED && str_starts_with($file, $upstream))) {
            return true;
        }
        throw new ErrorException($message, 0, $level, $file, $line);
    });
    date_default_timezone_set('UTC');

    define('RASPI_MONITOR_ENABLED', $monitor);
    define('RASPI_AUTH_ENABLED', true);
    define('RASPI_CONFIG', "$work/raspap");
    foreach (['HOTSPOT', 'DHCP', 'ADBLOCK', 'NETWORK', 'WIFICLIENT', 'OPENVPN', 'WIREGUARD',
              'VPN_PROVIDER', 'VNSTAT', 'RESTAPI', 'SYSTEM'] as $feature) {
        define("RASPI_{$feature}_ENABLED", true);
    }
    if (!function_exists('_')) {
        function _(string $message): string
        {
            return $message;
        }
    }
    function getProviderValue($id, $key): string
    {
        return 'VPN provider';
    }

    ini_set('session.use_cookies', '0');
    ini_set('session.save_path', $work);
    session_start();
    // Logged in as admin, as RaspAP's login leaves the session.
    $_SESSION = ['csrf_token' => bin2hex(random_bytes(32)), 'bridgedEnabled' => false, 'providerID' => 1,
        'user_id' => 'admin'];
    $_SERVER['REQUEST_METHOD'] = 'GET';
    $_SERVER['REQUEST_URI'] = '/plugin__Airhatch';

    foreach (UPSTREAM_FILES as $path) {
        require_once "$upstream/$path";
    }
}

/** RaspAP resolves its relative plugin path 'plugins' from the webroot. */
function enterWebroot(string $pluginsDir, string $work): void
{
    mkdir("$work/webroot");
    symlink($pluginsDir, "$work/webroot/plugins");
    chdir("$work/webroot");
}

/**
 * Renders the plugin page against a fake daemon serving $fixture (or against
 * $socket when given) and returns the HTML and the raw request lines.
 *
 * @return array{0: string, 1: list<string>}
 */
function renderPage(string $work, ?string $fixture, array $post = [], ?string $socket = null): array
{
    $socket ??= "$work/api.sock";
    $log = "$work/requests.log";
    if (file_exists($socket)) {
        unlink($socket);
    }
    file_put_contents($log, '');
    $daemon = $fixture === null ? null : startDaemon($socket, __DIR__ . "/fixtures/$fixture", $log);

    $_SERVER['REQUEST_METHOD'] = $post === [] ? 'GET' : 'POST';
    $_POST = $post === [] ? [] : $post + ['csrf_token' => $_SESSION['csrf_token']];
    // PluginManager passes the fully qualified class name as the plugin name.
    $plugin = new Airhatch('plugins', Airhatch::class, new DaemonClient("unix://$socket"));
    ob_start();
    $handled = $plugin->handlePageAction('/plugin__Airhatch');
    $html = ob_get_clean();

    if ($daemon !== null) {
        proc_terminate($daemon);
        proc_close($daemon);
    }
    check('page handled (' . ($fixture ?? 'no daemon') . ')', $handled);
    return [$html, file($log, FILE_IGNORE_NEW_LINES)];
}

/** @return resource */
function startDaemon(string $socket, string $fixture, string $log)
{
    $proc = proc_open([PHP_BINARY, __DIR__ . '/fake-daemon.php', $socket, $fixture, $log], [1 => ['pipe', 'w']], $pipes);
    if (fgets($pipes[1]) !== "ready\n") {
        fwrite(STDERR, "fake daemon did not start\n");
        exit(2);
    }
    return $proc;
}

function contains(string $html, string ...$needles): bool
{
    foreach ($needles as $needle) {
        if (!str_contains($html, $needle)) {
            fwrite(STDERR, "  missing: $needle\n");
            return false;
        }
    }
    return true;
}

function hasButtons(string $html): bool
{
    return str_contains($html, 'name="airhatch_open_ap"')
        || str_contains($html, 'name="airhatch_retry_client"')
        || str_contains($html, 'name="airhatch_auto"');
}

function normalSuite(string $work): void
{
    ob_start();
    new Airhatch('plugins', 'Airhatch', new DaemonClient("unix://$work/missing/api.sock"));
    $second = new Airhatch('plugins', 'Airhatch');
    check('construction twice: no I/O, no output', ob_get_clean() === '');

    $sidebar = new Sidebar();
    $second->initialize($sidebar);
    $items = array_values(array_filter($sidebar->getItems(), fn (array $item) => $item['link'] === 'plugin__Airhatch'));
    check('sidebar item', count($items) === 1
        && $items[0]['label'] === 'Wi-Fi fallback'
        && $items[0]['icon'] === 'fas fa-life-ring'
        && $items[0]['priority'] === 78);

    ob_start();
    $handled = $second->handlePageAction('/wpa_conf');
    check('other pages are left to RaspAP', !$handled && ob_get_clean() === '');

    check('duration format', Airhatch::duration(0) === '0s' && Airhatch::duration(45) === '45s'
        && Airhatch::duration(900) === '15m' && Airhatch::duration(5400) === '1h30m'
        && Airhatch::duration(3661) === '1h1m1s');

    [$html, $requests] = renderPage($work, 'client-connected.json');
    check('request is one JSON line', $requests === [rtrim(STATUS_LINE)]
        && file_get_contents("$work/requests.log") === STATUS_LINE);
    check('client connected: state and network', contains($html,
        'Connected to Wi-Fi', 'Wi-Fi client', '<td>Home</td>', '<td>192.168.1.23/24</td>', 'Automatic'));
    check('client connected: CSRF field and buttons', contains($html,
        '<input type="hidden" name="csrf_token" value="' . $_SESSION['csrf_token'] . '">',
        'action="plugin__Airhatch" method="POST"',
        'name="airhatch_open_ap"', 'name="airhatch_retry_client"', 'name="airhatch_auto"',
        'Open hotspot now', 'Retry Wi-Fi now', 'Back to automatic'));
    check('client connected: fallback timers', contains($html,
        'Fallback timers', '<td>45s</td>', '<td>1m</td>', '<td>5m</td>', '<td>15m</td>', '<td>5s</td>'));
    check('client connected: hand-off warning', contains($html,
        'Saving a Wi-Fi network on the hotspot closes the hotspot for up to a minute while the device tries it. If it fails, the hotspot comes back and this page shows why.'));
    check('client connected: no error, reminder or countdown', !str_contains($html, 'Last error')
        && !str_contains($html, '/boot/raspap-airhatch.txt') && !str_contains($html, '<script'));

    [$html] = renderPage($work, 'connecting.json');
    check('connecting: operation and attempt timer', contains($html,
        'Connecting to Wi-Fi', 'Joining a Wi-Fi network', 'Wi-Fi attempt gives up', '2026-10-01 12:00:45',
        'data-airhatch-seconds="0"', '<td>Office</td>'));

    [$html] = renderPage($work, 'ap-stations-error.json');
    check('ap: hotspot details', contains($html,
        'Hotspot open', '<td>10.3.141.1/24</td>', '<th>Connected devices</th><td>2</td>',
        '<th>Captive portal</th><td>on</td>', '<td>1h30m</td>'));
    check('ap: values escaped', contains($html,
        'RaspAP-1A2B&lt;img src=x onerror=alert(1)&gt;',
        'The password for &quot;Home &amp; &lt;Garden&gt;&quot; was rejected.')
        && !str_contains($html, '<img src=x') && !str_contains($html, '<Garden>'));
    check('ap: last error time', contains($html, '2026-10-01 11:59:30'));
    check('ap: notice and credentials reminder', contains($html,
        'dnsmasq was stopped outside airhatch',
        'Delete /boot/raspap-airhatch.txt after noting the credentials.'));
    check('ap: retry timers with countdown', contains($html,
        'Next Wi-Fi retry (no devices on the hotspot)', '2026-10-01 12:05:00',
        'Next Wi-Fi retry (devices on the hotspot)', '2026-10-01 12:15:00',
        'data-airhatch-seconds=', '<script type="text/javascript">'));

    [$html] = renderPage($work, 'handoff.json');
    check('handoff: state and timer', contains($html,
        'Closing the hotspot to try Wi-Fi', 'Hotspot closes to try Wi-Fi', '2026-10-01 12:00:05',
        '<th>Captive portal</th><td>off</td>'));

    [$html] = renderPage($work, 'held.json');
    check('held: override and hold', contains($html,
        'Hotspot stopped', 'Hotspot stopped by the user', 'Hotspot stays off until', '2026-10-01 12:15:00',
        '<span class="badge bg-light text-dark">Off</span>'));

    foreach (['airhatch_open_ap' => 'open_ap', 'airhatch_retry_client' => 'retry_client', 'airhatch_auto' => 'auto'] as $button => $op) {
        [$html, $requests] = renderPage($work, 'ap-stations-error.json', [$button => '']);
        check("POST $button sends $op, then status", $requests === ["{\"op\":\"$op\"}", rtrim(STATUS_LINE)]);
        check("POST $button shows the reply, escaped", contains($html,
            'alert alert-success', "$op accepted &lt;&amp;&gt;"));
    }

    [$html, $requests] = renderPage($work, 'held.json', ['airhatch_retry_client' => '']);
    check('daemon error shown as danger', contains($html, 'alert alert-danger', 'No Wi-Fi network is saved &lt;yet&gt;.')
        && count($requests) === 2);

    [$html, $requests] = renderPage($work, 'client-connected.json', ['unrelated' => '1']);
    check('POST without a plugin button only reads status', $requests === [rtrim(STATUS_LINE)]);

    [$html] = renderPage($work, null, ['airhatch_open_ap' => ''], "$work/missing/api.sock");
    check('unreachable: error box, no buttons', contains($html, 'airhatchd is not running.')
        && !hasButtons($html) && !str_contains($html, 'Fallback timers'));

    loginBypassChecks($work);

    [$html] = renderPage($work, 'malformed-reply.txt');
    check('malformed reply: reported as unexpected, not as a stopped daemon, no buttons',
        contains($html, 'airhatchd sent an unexpected reply.') && !str_contains($html, 'airhatchd is not running.')
        && !hasButtons($html));
}

/**
 * RaspAP's HTTPAuth::authenticate() skips the login for any URI containing
 * "/login", so a logged-out request to /plugin__Airhatch?x=/login reaches
 * the plugin. The plugin must then refuse on its own.
 */
function loginBypassChecks(string $work): void
{
    $_SERVER['REQUEST_URI'] = '/plugin__Airhatch?x=/login';
    unset($_SESSION['user_id']);
    (new HTTPAuth())->authenticate();
    check('upstream authenticate() lets a logged-out /login URI through', true);
    foreach (['GET' => [], 'POST' => ['airhatch_open_ap' => '']] as $method => $post) {
        [$html, $requests] = renderPage($work, 'client-connected.json', $post);
        check("logged out ($method, bypass URI): no daemon request", $requests === []);
        check("logged out ($method, bypass URI): no status, no buttons, a login hint",
            !hasButtons($html) && !str_contains($html, 'Connected to Wi-Fi') && contains($html, 'Log in to RaspAP'));
    }
    $_SESSION['user_id'] = 'admin';
    $_SERVER['REQUEST_URI'] = '/plugin__Airhatch';
}

function monitorSuite(string $work): void
{
    [$html, $requests] = renderPage($work, 'ap-stations-error.json', ['airhatch_open_ap' => '']);
    check('monitor: POST sends no action', $requests === [rtrim(STATUS_LINE)]);
    check('monitor: status shown without buttons', contains($html, 'Hotspot open') && !hasButtons($html)
        && !str_contains($html, 'csrf_token'));
}
