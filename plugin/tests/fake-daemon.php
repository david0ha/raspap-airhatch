<?php

/**
 * A stand-in for airhatchd's socket API (docs/API.md §3) that answers
 * from a status fixture and logs every raw request line.
 *
 * Usage: php fake-daemon.php <socket> <fixture> <request-log>
 * A fixture that is not JSON is sent back verbatim as a malformed reply.
 * Prints "ready" once listening; exits when idle for 30 s.
 */

declare(strict_types=1);

[, $socketPath, $fixturePath, $logPath] = $argv;

$fixture = file_get_contents($fixturePath);
$status = json_decode($fixture, true);

$server = stream_socket_server('unix://' . $socketPath, $errno, $errstr);
if ($server === false) {
    fwrite(STDERR, "fake-daemon: $errstr\n");
    exit(1);
}
echo "ready\n";

while (($conn = @stream_socket_accept($server, 30)) !== false) {
    $line = (string) fgets($conn);
    file_put_contents($logPath, $line, FILE_APPEND);
    fwrite($conn, $status === null ? $fixture : json_encode(reply($line, $status)) . "\n");
    fclose($conn);
}

function reply(string $line, array $status): array
{
    $op = json_decode($line, true)['op'] ?? null;
    return match (true) {
        $op === 'status' => ['ok' => true, 'message' => '', 'status' => $status],
        $op === 'retry_client' && $status['saved_networks'] === 0 => failure('no_saved_network', 'No Wi-Fi network is saved <yet>.'),
        in_array($op, ['open_ap', 'retry_client', 'auto'], true) => ['ok' => true, 'message' => "$op accepted <&>", 'status' => $status],
        default => failure('unknown_op', 'unknown op'),
    };
}

function failure(string $code, string $message): array
{
    return ['ok' => false, 'error' => ['code' => $code, 'message' => $message]];
}
