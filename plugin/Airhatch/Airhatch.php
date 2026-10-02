<?php

/**
 * Airhatch: status and controls of raspap-airhatch's client-first Wi-Fi
 * supervisor (airhatchd). Talks to the daemon over its socket API only.
 *
 * @license GPL-3.0-only
 * @see     src/RaspAP/Plugins/PluginInterface.php
 * @see     docs/API.md §3, §7
 */

declare(strict_types=1);

namespace RaspAP\Plugins\Airhatch;

use RaspAP\Auth\HTTPAuth;
use RaspAP\Messages\StatusMessage;
use RaspAP\Plugins\PluginInterface;
use RaspAP\UI\Sidebar;

require_once __DIR__ . '/DaemonClient.php';

class Airhatch implements PluginInterface
{
    /** POST button name => daemon op. */
    private const ACTIONS = [
        'airhatch_open_ap' => 'open_ap',
        'airhatch_retry_client' => 'retry_client',
        'airhatch_auto' => 'auto',
    ];

    private const STATES = [
        'init' => ['Starting', 'secondary'],
        'connecting' => ['Connecting to Wi-Fi', 'info'],
        'connected' => ['Connected to Wi-Fi', 'success'],
        'lost' => ['Wi-Fi lost, waiting for it to return', 'warning'],
        'ap' => ['Hotspot open', 'primary'],
        'handoff' => ['Closing the hotspot to try Wi-Fi', 'info'],
        'ap_failed' => ['Hotspot failed to open', 'danger'],
        'held' => ['Hotspot stopped', 'secondary'],
    ];

    private const MODES = ['client' => 'Wi-Fi client', 'ap' => 'Hotspot', 'none' => 'Off'];

    private const OVERRIDES = [
        'auto' => 'Automatic',
        'ap' => 'Hotspot kept open',
        'off' => 'Hotspot stopped by the user',
    ];

    private const OPERATIONS = [
        'open_ap' => 'Opening the hotspot',
        'close_ap' => 'Closing the hotspot',
        'client_attempt' => 'Joining a Wi-Fi network',
    ];

    private const TIMERS = [
        'attempt_ends_at' => 'Wi-Fi attempt gives up',
        'grace_ends_at' => 'Hotspot opens if Wi-Fi is still down',
        'handoff_at' => 'Hotspot closes to try Wi-Fi',
        'retry_at' => 'Next Wi-Fi retry (no devices on the hotspot)',
        'busy_retry_at' => 'Next Wi-Fi retry (devices on the hotspot)',
        'next_open_at' => 'Next hotspot attempt',
    ];

    private const SETTINGS = [
        'boot_timeout_s' => 'First Wi-Fi attempt after boot',
        'connect_timeout_s' => 'Later Wi-Fi attempts',
        'disconnect_grace_s' => 'Wait before opening the hotspot after Wi-Fi drops',
        'retry_interval_s' => 'Retry Wi-Fi from the hotspot (no devices on it)',
        'retry_interval_busy_s' => 'Retry Wi-Fi from the hotspot (devices on it)',
        'handoff_delay_s' => 'Delay before a hand-off',
        'off_hold_s' => 'Hotspot stays off after "Stop hotspot"',
    ];

    private const NOTICES = [
        'dnsmasq_stopped_outside_airhatch' =>
            'dnsmasq was stopped outside airhatch; devices on the hotspot get no address until it runs again.',
    ];

    private string $pluginPath;
    private DaemonClient $daemon;

    /**
     * Called by PluginManager with ($pluginPath, $pluginName). The name is not
     * kept: on page actions it is the class name, so getName() is used instead.
     * Constructing a DaemonClient opens nothing; the socket is used per request.
     */
    public function __construct(string $pluginPath, string $pluginName, DaemonClient $daemon = new DaemonClient())
    {
        $this->pluginPath = $pluginPath;
        $this->daemon = $daemon;
    }

    public static function getName(): string
    {
        return 'Airhatch';
    }

    public function initialize(Sidebar $sidebar): void
    {
        $sidebar->addItem(_('Wi-Fi fallback'), 'fas fa-life-ring', self::route(), 78);
    }

    public function handlePageAction(string $page): bool
    {
        if (!str_starts_with($page, '/' . self::route())) {
            return false;
        }

        $messages = new StatusMessage();
        if (!self::loggedIn()) {
            echo $this->renderTemplate('main', [
                'action' => self::route(),
                'messages' => $messages,
                'view' => null,
                'unavailable' => _('Log in to RaspAP to use this page.'),
            ]);
            return true;
        }

        $op = RASPI_MONITOR_ENABLED ? null : self::requestedOp($_POST);
        if ($op !== null) {
            self::report($this->daemon->request(['op' => $op]), $messages);
        }

        $reply = $this->daemon->request(['op' => 'status']);
        $view = $reply['ok'] && is_array($reply['status'] ?? null) ? self::describe($reply['status']) : null;

        echo $this->renderTemplate('main', [
            'action' => self::route(),
            'messages' => $messages,
            'view' => $view,
            'unavailable' => $view === null ? self::errorMessage($reply) : null,
        ]);
        return true;
    }

    public function renderTemplate(string $templateName, array $__data = []): string
    {
        $templateFile = "{$this->pluginPath}/" . self::getName() . "/templates/{$templateName}.php";
        if (!file_exists($templateFile)) {
            return "Template file {$templateFile} not found.";
        }
        extract($__data);
        ob_start();
        include $templateFile;
        return ob_get_clean();
    }

    /**
     * Formats seconds like Go's time.Duration without zero units: 45s, 15m, 1h30m.
     */
    public static function duration(int $seconds): string
    {
        $parts = [
            'h' => intdiv($seconds, 3600),
            'm' => intdiv($seconds % 3600, 60),
            's' => $seconds % 60,
        ];
        $text = '';
        foreach ($parts as $unit => $value) {
            if ($value > 0) {
                $text .= $value . $unit;
            }
        }
        return $text === '' ? '0s' : $text;
    }

    /**
     * RaspAP skips its own login check for any URI that contains "/login"
     * (HTTPAuth::authenticate), so the plugin checks the session itself
     * before it talks to the daemon.
     */
    private static function loggedIn(): bool
    {
        return !RASPI_AUTH_ENABLED || (new HTTPAuth())->isLogged();
    }

    private static function route(): string
    {
        return 'plugin__' . self::getName();
    }

    private static function requestedOp(array $post): ?string
    {
        foreach (self::ACTIONS as $button => $op) {
            if (isset($post[$button])) {
                return $op;
            }
        }
        return null;
    }

    private static function report(array $reply, StatusMessage $messages): void
    {
        if (!$reply['ok']) {
            $messages->addMessage(htmlspecialchars(self::errorMessage($reply)), 'danger');
        } elseif (($reply['message'] ?? '') !== '') {
            $messages->addMessage(htmlspecialchars($reply['message']), 'success');
        }
    }

    private static function errorMessage(array $reply): string
    {
        // A successful reply without a status object is unexpected too.
        return $reply['error']['message'] ?? DaemonClient::badReply()['error']['message'];
    }

    /**
     * Turns the daemon's status object into display values for the template.
     */
    private static function describe(array $status): array
    {
        [$stateLabel, $stateClass] = self::STATES[$status['state']] ?? [$status['state'], 'secondary'];
        $operation = $status['operation'];
        $lastError = $status['last_error'];

        return [
            'state' => _($stateLabel),
            'stateClass' => $stateClass,
            'mode' => _(self::MODES[$status['mode']] ?? $status['mode']),
            'override' => _(self::OVERRIDES[$status['override']] ?? $status['override']),
            'holdUntil' => self::localTime($status['hold_until']),
            'operation' => $operation === null ? null : _(self::OPERATIONS[$operation['name']] ?? $operation['name']),
            'savedNetworks' => $status['saved_networks'],
            'details' => self::details($status),
            'lastError' => $lastError === null ? null : [
                'message' => $lastError['message'],
                'at' => self::localTime($lastError['at']),
            ],
            'timers' => self::timers($status['timers']),
            'notices' => array_map(fn (string $code): string => _(self::NOTICES[$code] ?? $code), $status['notices']),
            'credentialsFile' => $status['credentials_file'],
            'settings' => self::settings($status['config']),
        ];
    }

    /** @return list<array{0: string, 1: string}> label/value rows of the active mode */
    private static function details(array $status): array
    {
        $rows = [];
        if ($status['client'] !== null) {
            $rows[] = [_('Wi-Fi network'), $status['client']['ssid']];
            $rows[] = [_('Address'), $status['client']['ipv4']];
        }
        if ($status['ap'] !== null) {
            $ap = $status['ap'];
            $rows[] = [_('Hotspot name'), $ap['ssid']];
            $rows[] = [_('Hotspot address'), $ap['ipv4']];
            $rows[] = [_('Connected devices'), (string) $ap['stations']];
            $rows[] = [_('Captive portal'), $ap['captive'] ? _('on') : _('off')];
        }
        return $rows;
    }

    /** @return list<array{label: string, at: string, seconds: int}> */
    private static function timers(array $timers): array
    {
        $rows = [];
        foreach (self::TIMERS as $key => $label) {
            $at = $timers[$key] === null ? false : strtotime($timers[$key]);
            if ($at !== false) {
                $rows[] = [
                    'label' => _($label),
                    'at' => date('Y-m-d H:i:s', $at),
                    'seconds' => max(0, $at - time()),
                ];
            }
        }
        return $rows;
    }

    /** @return list<array{0: string, 1: string}> */
    private static function settings(array $config): array
    {
        $rows = [];
        foreach (self::SETTINGS as $key => $label) {
            $rows[] = [_($label), self::duration($config[$key])];
        }
        return $rows;
    }

    private static function localTime(?string $rfc3339): ?string
    {
        $at = $rfc3339 === null ? false : strtotime($rfc3339);
        return $at === false ? $rfc3339 : date('Y-m-d H:i:s', $at);
    }
}
