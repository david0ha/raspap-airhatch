<?php

/**
 * airhatchd socket API client (docs/API.md §3): one JSON request line,
 * one JSON reply line per connection.
 *
 * @license GPL-3.0-only
 */

declare(strict_types=1);

namespace RaspAP\Plugins\Airhatch;

final class DaemonClient
{
    public function __construct(
        private string $socket = 'unix:///run/airhatch/api.sock',
        private float $timeout = 2.0
    ) {
    }

    /**
     * Sends one request and returns the daemon's decoded reply, an
     * `unreachable` error reply when the daemon cannot be asked, or a
     * `bad_reply` error reply when it answers with something that is not a
     * reply.
     */
    public function request(array $req): array
    {
        try {
            $line = $this->exchange(json_encode($req, JSON_THROW_ON_ERROR) . "\n");
        } catch (\JsonException) {
            $line = null;
        }
        if ($line === null) {
            return self::unreachable();
        }
        $reply = json_decode($line, true);
        return self::isReply($reply) ? $reply : self::badReply();
    }

    /**
     * The error reply for an answer that is not a reply; its message is the
     * plugin's text for any unexpected reply.
     */
    public static function badReply(): array
    {
        return [
            'ok' => false,
            'error' => ['code' => 'bad_reply', 'message' => _('airhatchd sent an unexpected reply.')],
        ];
    }

    /** The raw reply line, or null when the daemon could not be asked. */
    private function exchange(string $line): ?string
    {
        // Warnings would land in the rendered page; failures are reported
        // through the return value instead.
        $conn = @stream_socket_client($this->socket, $errno, $errstr, $this->timeout);
        if ($conn === false) {
            return null;
        }
        try {
            $seconds = (int) $this->timeout;
            stream_set_timeout($conn, $seconds, (int) (($this->timeout - $seconds) * 1e6));
            if (@fwrite($conn, $line) !== strlen($line)) {
                return null;
            }
            $reply = fgets($conn);
            return $reply === false ? null : $reply;
        } finally {
            fclose($conn);
        }
    }

    private static function isReply(mixed $reply): bool
    {
        if (!is_array($reply) || !is_bool($reply['ok'] ?? null)) {
            return false;
        }
        if ($reply['ok']) {
            return true;
        }
        $error = $reply['error'] ?? null;
        return is_array($error)
            && is_string($error['code'] ?? null)
            && is_string($error['message'] ?? null);
    }

    private static function unreachable(): array
    {
        return [
            'ok' => false,
            'error' => ['code' => 'unreachable', 'message' => _('airhatchd is not running.')],
        ];
    }
}
