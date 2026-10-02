#!/bin/bash
# Smoke test inside the airhatch image under Docker (docs/DESIGN.md §11.3); run.sh
# drives it.
#   inner.sh boot         PID 1 of the container: prepares the veth radio and
#                         the test-only units, then executes /sbin/init
#   inner.sh first-boot   scenarios on a fresh image (no saved network)
#   inner.sh second-boot  scenarios after a reboot with a reachable network
#
# wlan0 is a veth whose peer, peer0, lives in the network namespace "air".
# There, 10.3.141.9 is a phone on the hotspot and 192.168.77.1 the home
# router, whose DHCP server is smoke-router.service. Neither answers a DHCP
# client inside "air" itself, so any lease a client there gets comes from
# the device.
set -euo pipefail

# shellcheck source=SCRIPTDIR/../lib.sh
. "${BASH_SOURCE[0]%/*}/../lib.sh"

TESTS=$(cd "${BASH_SOURCE[0]%/*}/.." && pwd)
SMOKE_RUN=/run/airhatch-smoke
UNITS=/run/systemd/system
HARNESS=$TESTS/static/wifimanager-harness.php
SHIM_LOG=$SMOKE_RUN/sudo-shim.log
STATIONS_FILE=$SMOKE_RUN/stations
CREDENTIALS=/boot/raspap-airhatch.txt
SUPPLICANT_SOCKET=/var/run/wpa_supplicant/wlan0

AP_IP=10.3.141.1
PHONE_IP=10.3.141.9
ROUTER_IP=192.168.77.1
ROUTER_PREFIX=192.168.77.
HOME_SSID=Home
LANDING="http://$AP_IP/login?action=%2Fwpa_conf"
LANDING_AFTER_FAILURE="http://$AP_IP/login?action=%2Fplugin__Airhatch"

# --- boot (PID 1) -----------------------------------------------------------

create_radio() {
    ip link add wlan0 type veth peer name peer0
    ip netns add air
    ip link set peer0 netns air
    ip -n air link set lo up
    ip -n air addr add "$PHONE_IP/24" dev peer0
    ip -n air addr add "$ROUTER_IP/24" dev peer0
    ip -n air link set peer0 up
}

# drop_in <unit>: writes the test-only drop-in read from stdin.
drop_in() {
    mkdir -p "$UNITS/$1.d"
    cat >"$UNITS/$1.d/90-smoke.conf"
}

install_test_units() {
    mkdir -p "$SMOKE_RUN"
    # The wired driver associates without scanning.
    printf 'ap_scan=0\n' >"$SMOKE_RUN/wired.conf"
    # Coldplug only the container's network devices (the others are the
    # host's). systemd in Docker learns about devices only when it
    # enumerates them, so reload once udev has seen wlan0: the shipped
    # airhatch-wpa-supplicant.service binds to wlan0's device unit.
    drop_in systemd-udev-trigger.service <<'EOF'
[Service]
ExecStart=
ExecStart=-/usr/bin/udevadm trigger --subsystem-match=net --action=add --settle
ExecStartPost=/bin/systemctl --no-block daemon-reload
EOF
    drop_in hostapd.service <<EOF
[Service]
Type=simple
PIDFile=
ExecStart=
ExecStart=/smoke-bin/hostapd -stations $STATIONS_FILE /etc/hostapd/hostapd.conf
EOF
    drop_in airhatch-wpa-supplicant.service <<EOF
[Service]
ExecStart=
ExecStart=/usr/sbin/wpa_supplicant -i wlan0 -D wired -c /etc/wpa_supplicant/wpa_supplicant.conf -I $SMOKE_RUN/wired.conf
EOF
    cat >"$UNITS/smoke-router.service" <<EOF
[Unit]
Description=airhatch smoke test: the home router's DHCP server
Before=airhatchd.service

[Service]
ExecStart=/usr/sbin/ip netns exec air /usr/bin/dnsmasq --keep-in-foreground --conf-file=/dev/null --port=0 --interface=peer0 --bind-interfaces --dhcp-authoritative --dhcp-range=${ROUTER_PREFIX}50,${ROUTER_PREFIX}99,1h --dhcp-leasefile=$SMOKE_RUN/router.leases --pid-file=$SMOKE_RUN/router.pid
EOF
    mkdir -p "$UNITS/multi-user.target.wants"
    ln -s ../smoke-router.service "$UNITS/multi-user.target.wants/smoke-router.service"
    # The serial console of the CM5 does not exist here.
    ln -s /dev/null "$UNITS/serial-getty@ttyAMA0.service"
}

boot() {
    # The SD card partitions of the image's fstab do not exist here.
    sed -i '/^\/dev\/mmcblk/d' /etc/fstab
    install -D -m 0644 "$TESTS/smoke/airhatchd-test.conf" /etc/airhatch/airhatchd.conf
    create_radio
    install_test_units
    exec /sbin/init
}

# --- observations -------------------------------------------------------------

# status_field <key[.key]>: one field of "airhatchd status", "null" for null.
status_field() {
    # shellcheck disable=SC2016 # PHP code
    airhatchd status 2>/dev/null | php -r '
        $value = json_decode(stream_get_contents(STDIN), true);
        foreach (explode(".", $argv[1]) as $key) {
            $value = is_array($value) ? ($value[$key] ?? null) : null;
        }
        echo match (true) {
            $value === null => "null",
            is_bool($value) => $value ? "true" : "false",
            is_array($value) => json_encode($value),
            default => $value,
        };' "$1"
}

state_is() {
    [[ $(status_field state) == "$1" ]]
}

# expect_state <seconds> <state>
expect_state() {
    if wait_until "$1" state_is "$2"; then
        pass "state $2 within $1 s"
    else
        fail "state $2 within $1 s (state $(status_field state), last error $(status_field last_error.code))"
    fi
}

# test_conf_seconds <key>: a duration of airhatchd-test.conf in seconds.
test_conf_seconds() {
    local value
    value=$(sed -n "s/^$1=//p" "$TESTS/smoke/airhatchd-test.conf")
    case $value in
        *m) echo $((${value%m} * 60)) ;;
        *s) echo "${value%s}" ;;
        *) die "airhatchd-test.conf: $1 is not in s or m" ;;
    esac
}

has_nft_table() {
    nft list table inet airhatch >/dev/null 2>&1
}

ap_is_up() {
    [[ -e /run/airhatch/ap.lease ]] && systemctl -q is-active hostapd.service && has_nft_table
}

ap_is_closed() {
    [[ ! -e /run/airhatch/ap.lease ]] && ! systemctl -q is-active hostapd.service && ! has_nft_table
}

hostapd_never_started() {
    [[ $(systemctl show -P ExecMainStartTimestampMonotonic hostapd.service) == 0 ]]
}

# The stand-in reports DISCONNECTED; the wired supplicant would be COMPLETED.
standin_answers() {
    wpa_cli -i wlan0 status 2>/dev/null | grep -qx 'wpa_state=DISCONNECTED'
}

dnsmasq_has_client_content() {
    grep -qx 'except-interface=wlan0' /run/airhatch/dnsmasq.conf &&
        ! grep -q '^address=' /run/airhatch/dnsmasq.conf
}

# Nothing listens on port 8099 (1FA3); 0A is the state LISTEN.
portal_closed() {
    awk 'NR > 1 && $4 == "0A" && $2 ~ /:1FA3$/ { found = 1 } END { exit found }' /proc/net/tcp
}

no_nft_table() {
    ! has_nft_table
}

wlan0_ipv4() {
    ip -4 -o addr show dev wlan0 | awk '{ sub(/\/.*/, "", $4); print $4 }'
}

wlan0_has_router_lease() {
    wlan0_ipv4 | grep -q "^${ROUTER_PREFIX//./\\.}"
}

wlan0_lacks_ap_address() {
    ! wlan0_ipv4 | grep -qxF "$AP_IP"
}

# --- the phone and the router in "air" -----------------------------------------

in_air() {
    ip netns exec air "$@"
}

phone_dhcp_lease() {
    # -B asks for broadcast replies: a unicast offer to an address nobody
    # holds yet would not reach the probe.
    in_air udhcpc -B -i peer0 -n -q -t 4 -T 1 -s /bin/true >/dev/null 2>&1
}

no_phone_dhcp_lease() {
    ! phone_dhcp_lease
}

# phone_dns <server>: the A records the server returns for example.com.
# dig +short reports a timeout on stdout as ";;" lines; they are no answer.
phone_dns() {
    { in_air dig +short +time=2 +tries=1 "@$1" example.com 2>/dev/null || true; } | grep -v '^;;' || true
}

# phone_http <curl args...>: prints the status code; keeps the body and the
# CR-stripped headers in $SMOKE_RUN and the cookies of one browser session.
phone_http() {
    : >"$SMOKE_RUN/headers.raw"
    : >"$SMOKE_RUN/body"
    in_air curl --silent --max-time 15 \
        --cookie "$SMOKE_RUN/cookies" --cookie-jar "$SMOKE_RUN/cookies" \
        --dump-header "$SMOKE_RUN/headers.raw" --output "$SMOKE_RUN/body" \
        --write-out '%{http_code}' "$@" || true
    tr -d '\r' <"$SMOKE_RUN/headers.raw" >"$SMOKE_RUN/headers"
}

# header <name>: the value of the last such response header.
header() {
    awk -v name="${1,,}" 'index(tolower($0), name ": ") == 1 { value = substr($0, length(name) + 3) }
        END { print value }' "$SMOKE_RUN/headers"
}

body_has() {
    grep -qF -- "$1" "$SMOKE_RUN/body"
}

# page_is <status> <wanted status> <text in the body>
page_is() {
    [[ $1 == "$2" ]] && body_has "$3"
}

csrf_token() {
    sed -nE 's/.*name="csrf_token" (value|content)="([0-9a-f]+)".*/\2/p' "$SMOKE_RUN/body" | head -n 1
}

router_down() {
    ip -n air link set peer0 down
}

router_up() {
    ip -n air link set peer0 up
}

# --- RaspAP as the web UI and the WiFi page run it ------------------------------

# raspap_sudo <command...>: the command as RaspAP's PHP runs it.
raspap_sudo() {
    sudo -u www-data sudo -n "$@"
}

# harness <args...>: wifimanager-harness.php with sudo-shim.sh as sudo.
# RaspAP's web save leaves its scratch /tmp/wifidata owned by www-data;
# fs.protected_regular keeps root from reopening it in sticky /tmp, so the
# harness, which runs as root, starts without it as RaspAP's www-data would.
harness() {
    rm -f /tmp/wifidata
    mkdir -p "$SMOKE_RUN/shim"
    ln -sf "$TESTS/static/sudo-shim.sh" "$SMOKE_RUN/shim/sudo"
    PATH=$SMOKE_RUN/shim:$PATH SUDO_SHIM_LOG=$SHIM_LOG php "$HARNESS" "$@"
}

shim_log_lacks() {
    ! grep -qE -- "$1" "$SHIM_LOG"
}

# admin_password: the password first boot wrote to the credentials notice.
admin_password() {
    sed -n 's/^RaspAP admin password: *//p' "$CREDENTIALS"
}

login_through_portal() {
    local password status token
    password=$(admin_password)
    [[ -n $password ]] || return 1
    rm -f "$SMOKE_RUN/cookies"
    # The first request of a session seeds RaspAP's CSRF token.
    phone_http "http://$AP_IP/login" >/dev/null
    status=$(phone_http "http://$AP_IP/login")
    token=$(csrf_token)
    [[ $status == 200 && -n $token ]] || return 1
    status=$(phone_http --data-urlencode login-auth= --data-urlencode username=admin \
        --data-urlencode "password=$password" --data-urlencode redirect-url=/ \
        --data-urlencode "csrf_token=$token" "http://$AP_IP/login")
    [[ $status == 302 ]]
}

# sudo_log_since <byte offset>: sudo's syslog lines written after the offset.
sudo_log_since() {
    tail -c "+$(($1 + 1))" /var/log/messages | grep 'sudo:.*COMMAND=' || true
}

# --- scenarios: first boot ------------------------------------------------------

boot_finished() {
    systemctl -q is-active multi-user.target 2>/dev/null
}

scenario_boot_without_networks() {
    local status
    check "boot reached multi-user.target" wait_until 120 boot_finished
    check "airhatchd active" systemctl -q is-active airhatchd.service
    expect_state 30 ap
    check "AP up: lease, hostapd active, nft table inet airhatch" ap_is_up
    check "dnsmasq active" systemctl -q is-active dnsmasq.service
    check "wlan0 has $AP_IP" test "$(wlan0_ipv4)" = "$AP_IP"
    check "phone gets a DHCP lease from the hotspot" phone_dhcp_lease
    check "phone DNS: example.com -> $AP_IP" test "$(phone_dns "$AP_IP")" = "$AP_IP"

    status=$(phone_http -H 'Host: connectivitycheck.gstatic.com' "http://$AP_IP/generate_204")
    check "probe redirect: $status to $(header location)" \
        test "$status $(header location)" = "302 $LANDING"
    check "probe redirect: Cache-Control no-store" test "$(header cache-control)" = no-store

    status=$(phone_http "http://$AP_IP/captive-portal/api")
    check "RFC 8908 API: $status, application/captive+json" \
        test "$status $(header content-type)" = "200 application/captive+json"
    check "RFC 8908 API: Cache-Control no-store" test "$(header cache-control)" = no-store
    # shellcheck disable=SC2016 # PHP code
    check "RFC 8908 API: captive true, user-portal-url $LANDING" php -r '
        $api = json_decode(file_get_contents($argv[1]), true);
        exit($api === ["captive" => true, "user-portal-url" => $argv[2]] ? 0 : 1);' \
        "$SMOKE_RUN/body" "$LANDING"
}

scenario_first_boot_credentials() {
    check "credentials notice $CREDENTIALS" test -s "$CREDENTIALS"
    check "hostapd.conf: ssid=RaspAP-XXXX" grep -qxE 'ssid=RaspAP-[0-9A-F]{4}' /etc/hostapd/hostapd.conf
    check "hostapd.conf: passphrase is not ChangeMe" \
        test -z "$(grep -x 'wpa_passphrase=ChangeMe' /etc/hostapd/hostapd.conf || true)"
    check "raspap.auth: www-data 0600" test "$(stat -c '%U %a' /etc/raspap/raspap.auth)" = 'www-data 600'
    check "marker /etc/airhatch/firstboot.done" test -f /etc/airhatch/firstboot.done
    check "status names the credentials file" test "$(status_field credentials_file)" = "$CREDENTIALS"
}

scenario_wifi_page_on_ap() {
    local offset status sudo_log id
    # RaspAP skips its login check for URIs containing "/login"; the plugin
    # checks the session itself.
    # A new session gets RaspAP's CSRF token through a redirect to the same
    # URI, so follow it.
    rm -f "$SMOKE_RUN/cookies"
    status=$(phone_http --location "http://$AP_IP/plugin__Airhatch?x=/login")
    check "logged out, GET /plugin__Airhatch?x=/login: $status, login hint" \
        page_is "$status" 200 'Log in to RaspAP to use this page.'
    check "logged out: no hotspot controls" body_lacks 'airhatch_open_ap'
    check "logged out: no daemon status" body_lacks 'Hotspot open'
    check "login through the portal with the first-boot admin password" login_through_portal
    status=$(phone_http "http://$AP_IP/plugin__Airhatch")
    check "logged in, GET /plugin__Airhatch: $status, daemon status" page_is "$status" 200 'Hotspot open'
    offset=$(stat -c %s /var/log/messages)
    status=$(phone_http "http://$AP_IP/wpa_conf")
    check "GET /wpa_conf through the portal: $status, WiFi client page" \
        page_is "$status" 200 'name="wpa_conf_form"'
    sudo_log=$(sudo_log_since "$offset")
    check "the page ran wpa_cli list_networks through sudo" \
        grep -qE 'COMMAND=[^ ]*/wpa_cli -i wlan0 list_networks' <<<"$sudo_log"
    check "the page started no wpa_supplicant" \
        test -z "$(grep -E 'COMMAND=[^ ]*/wpa_supplicant ' <<<"$sudo_log" || true)"
    check "the page's new wpa_supplicant.conf (no network) keeps the AP" state_is ap

    check "wpa_cli list_networks: header only" \
        test "$(wpa_cli -i wlan0 list_networks)" = 'network id / ssid / bssid / flags'
    id=$(wpa_cli -i wlan0 add_network || true)
    check "wpa_cli add_network: '$id'" grep -qxE '[0-9]+' <<<"$id"
    check "wpa_cli set_network $id ssid: OK" test "$(wpa_cli -i wlan0 set_network "$id" ssid '"x"')" = OK
    check "wpa_cli reconfigure: OK" test "$(wpa_cli -i wlan0 reconfigure)" = OK
    check "wpa_cli select_network 0: OK" test "$(wpa_cli -i wlan0 select_network 0)" = OK
    check "wpa_cli status: DISCONNECTED" standin_answers
    check "www-data's sudo wpa_cli status: DISCONNECTED" \
        grep -qx 'wpa_state=DISCONNECTED' <(raspap_sudo /sbin/wpa_cli -i wlan0 status)
    sleep 2
    check "no hand-off without a saved network" state_is ap

    : >"$SHIM_LOG"
    check "WiFiManager setKnownStationsWPA on the stand-in" harness known
    check "WiFiManager connectedWifiStations: none connected" test -z "$(harness connected)"
    # setKnownStationsWPA reads /etc/wpa_supplicant/wpa_supplicant.conf
    # through sudo; only running the binary counts.
    check "WiFiManager started no wpa_supplicant" shim_log_lacks '(^|/)wpa_supplicant '
    check "scan cache /tmp/raspap: www-data" test "$(stat -c %U /tmp/raspap)" = www-data
    check "scan cache key for wpa_supplicant.conf's mtime" \
        test -f "/tmp/raspap/nearby_wifi_stations_$(stat -c %Y /etc/wpa_supplicant/wpa_supplicant.conf)"
    check "WiFiManager nearbyWifiStations" harness nearby
    check "nearbyWifiStations read the seed, no live scan" shim_log_lacks 'iw dev'
}

hostapd_pid() {
    systemctl show -P MainPID hostapd.service
}

# hostapd_restarted <old pid>: a new hostapd, and the reapply has finished.
hostapd_restarted() {
    local pid
    pid=$(hostapd_pid)
    [[ $pid != 0 && $pid != "$1" ]] && state_is ap && [[ $(status_field operation) == null ]]
}

scenario_hotspot_buttons_on_ap() {
    local output pid off_hold
    off_hold=$(test_conf_seconds off_hold)
    pid=$(hostapd_pid)
    output=$(raspap_sudo /etc/raspap/hostapd/servicestart.sh --interface br0 --seconds 1 || true)
    check "Start hotspot, bridged: '$output'" test "$output" = \
        'airhatch: RaspAP bridged and AP+client modes are not supported by raspap-airhatch; nothing changed.'
    check "bridged start changed nothing" test "$(hostapd_pid)" = "$pid"

    output=$(raspap_sudo /etc/raspap/hostapd/servicestart.sh --interface wlan0 --seconds 1 || true)
    check "Restart hotspot on the AP: '$output'" \
        test "$output" = 'airhatch: restarting the hotspot with the current settings.'
    check "hotspot reapplied (new hostapd)" wait_until 30 hostapd_restarted "$pid"

    check "Stop hotspot: sudo systemctl stop hostapd.service" raspap_sudo /bin/systemctl stop hostapd.service
    expect_state 10 held
    check "override off" test "$(status_field override)" = off
    check "AP closed while held" ap_is_closed
    expect_state $((off_hold + 15)) ap
    check "AP back after off_hold" ap_is_up
}

stray_supplicant_running() {
    pidof wpa_supplicant >/dev/null
}

# start_stray: RaspAP's Reinitialize (rm -f of the control socket, then
# wpa_supplicant -i wlan0 … -B), with the wired driver so it runs on veth.
start_stray() {
    rm -f "$SUPPLICANT_SOCKET"
    wpa_supplicant -i wlan0 -D wired -c /etc/wpa_supplicant/wpa_supplicant.conf \
        -I "$SMOKE_RUN/wired.conf" -B >/dev/null && stray_supplicant_running
}

no_stray_supplicant() {
    ! stray_supplicant_running
}

# sudo_refused <command...>: www-data's sudo refuses the command by policy.
sudo_refused() {
    local output
    output=$(raspap_sudo "$@" 2>&1 || true)
    grep -q 'is not allowed to execute' <<<"$output"
}

supplicant_start_stamp() {
    systemctl show -P ExecMainStartTimestampMonotonic airhatch-wpa-supplicant.service
}

supplicant_inactive() {
    ! systemctl -q is-active airhatch-wpa-supplicant.service
}

not_connected() {
    ! state_is connected
}

wlan0_lacks_router_lease() {
    ! wlan0_has_router_lease
}

body_lacks() {
    ! body_has "$1"
}

scenario_reinitialize_on_ap() {
    check "Reinitialize: stray wpa_supplicant -i wlan0 started" start_stray
    check "stray reaped within 2 ticks" wait_until 3 no_stray_supplicant
    check "stand-in answers on $SUPPLICANT_SOCKET again" wait_until 3 standin_answers
    check "still AP" state_is ap
}

# RaspAP starts wpa_supplicant through sudo in two forms (raspap.sudoers
# lines 7-8, WiFiManager.php:331,410); 095_airhatch refuses both, so only
# airhatchd starts one.
scenario_raspap_supplicant_refused() {
    check "sudo wpa_supplicant -i wlan0 -c ... -B refused" \
        sudo_refused /sbin/wpa_supplicant -i wlan0 -c /etc/wpa_supplicant/wpa_supplicant.conf -B
    check "sudo wpa_supplicant -B -Dnl80211 ... -iwlan0 refused" \
        sudo_refused /sbin/wpa_supplicant -B -Dnl80211 -c/etc/wpa_supplicant/wpa_supplicant.conf -iwlan0
    check "no stray wpa_supplicant" no_stray_supplicant
    check "www-data's other sudo rules still apply (wpa_cli status)" raspap_sudo /sbin/wpa_cli -i wlan0 status
    check "still AP" state_is ap
}

# airhatchd killed in AP mode: its ExecStopPost closes the AP, so nothing
# serves wlan0 while it is down.
scenario_crash_on_ap() {
    drop_in airhatchd.service <<'EOF'
[Service]
Restart=no
EOF
    systemctl daemon-reload
    # A crash kills only the main process; the default --kill-whom=all would
    # also kill the ExecStopPost process this scenario is about.
    check "airhatchd main process killed (SIGKILL)" \
        systemctl kill --kill-whom=main -s KILL airhatchd.service
    check "airhatchd killed" wait_until 5 airhatchd_inactive
    check "AP closed by ExecStopPost" wait_until 30 ap_is_closed
    check "dnsmasq back to except-interface=wlan0" dnsmasq_has_client_content
    check "wlan0 lost $AP_IP" wlan0_lacks_ap_address
    check "no DHCP offer from the device" no_phone_dhcp_lease

    rm -r "$UNITS/airhatchd.service.d"
    systemctl daemon-reload
    check "airhatchd started again" systemctl start airhatchd.service
    expect_state 30 ap
    check "AP up again" ap_is_up
}

expect_client_mode() {
    check "AP closed" ap_is_closed
    check "dnsmasq back to except-interface=wlan0" dnsmasq_has_client_content
    check "no portal listener on 8099" portal_closed
    check "wlan0 has a router lease: $(wlan0_ipv4)" wlan0_has_router_lease
}

scenario_save_on_ap() {
    local status token budget
    budget=$(($(test_conf_seconds handoff_delay) + $(test_conf_seconds connect_timeout) + 10))
    status=$(phone_http "http://$AP_IP/wpa_conf")
    token=$(csrf_token)
    check "WiFi page for the save: $status, CSRF token" test "$status" = 200 -a -n "$token"
    status=$(phone_http --data-urlencode "csrf_token=$token" --data-urlencode client_settings= \
        --data-urlencode update0= --data-urlencode "ssid0=$HOME_SSID" --data-urlencode protocol0=OPEN \
        --data-urlencode passphrase0= "http://$AP_IP/wpa_conf")
    check "POST /wpa_conf client_settings ($HOME_SSID, open): $status, saved" \
        page_is "$status" 200 'Wifi settings updated successfully'
    check "RaspAP wrote $HOME_SSID to wpa_supplicant.conf" \
        grep -qx "[[:space:]]*ssid=\"$HOME_SSID\"" /etc/wpa_supplicant/wpa_supplicant.conf
    expect_state "$budget" connected
    check "client SSID $HOME_SSID" test "$(status_field client.ssid)" = "$HOME_SSID"
    expect_client_mode
}

scenario_captive_only_in_ap_mode() {
    local client_ip status
    client_ip=$(wlan0_ipv4)
    check "AP address $AP_IP gone from wlan0" wlan0_lacks_ap_address
    check "no DNS answer on wlan0 ($client_ip)" test -z "$(phone_dns "$client_ip")"
    status=$(phone_http -H 'Host: connectivitycheck.gstatic.com' "http://$client_ip/generate_204")
    # lighttpd must answer (status not 000), and not with the portal redirect.
    check "no portal redirect on wlan0: $status $(header location)" \
        test "$status" != 000 -a "$(header location)" != "$LANDING"
}

scenario_drop() {
    router_down
    expect_state $(($(test_conf_seconds disconnect_grace) + 15)) ap
    check "AP up after the drop" ap_is_up
}

scenario_wrong_network() {
    local budget status
    budget=$(($(test_conf_seconds handoff_delay) + $(test_conf_seconds connect_timeout) + 20))
    check "WiFiManager writeWpaSupplicant: an unreachable network" harness write Nowhere
    check "hand-off to the client attempt" wait_until 10 state_is connecting
    expect_state "$budget" ap
    # With the wired driver any SSID associates, so the attempt ends without
    # a DHCP lease rather than with network_not_found (hardware only).
    check "last error: $(status_field last_error.code)" test "$(status_field last_error.code)" = no_dhcp_lease
    router_up
    status=$(phone_http -H 'Host: connectivitycheck.gstatic.com' "http://$AP_IP/generate_204")
    check "after a failed hand-off the probe lands on the plugin: $status $(header location)" \
        test "$status $(header location)" = "302 $LANDING_AFTER_FAILURE"
}

# close_failed_on_dnsmasq: the hand-off's close stopped hostapd and wrote
# the client fence, but dnsmasq did not start again.
close_failed_on_dnsmasq() {
    ! systemctl -q is-active hostapd.service && dnsmasq_has_client_content &&
        ! systemctl -q is-active dnsmasq.service
}

# A hand-off whose close cannot restart dnsmasq keeps the device out of
# client mode until a close succeeds, and RaspAP's Reinitialize cannot start
# a supplicant meanwhile.
scenario_close_fails_on_dnsmasq() {
    local stamp budget
    budget=$(($(test_conf_seconds handoff_delay) + $(test_conf_seconds connect_timeout) + 10))
    stamp=$(supplicant_start_stamp)
    drop_in dnsmasq.service <<'UNIT'
[Service]
ExecStartPre=/bin/false
UNIT
    systemctl daemon-reload
    check "WiFiManager writeWpaSupplicant: $HOME_SSID" harness write "$HOME_SSID"
    check "hand-off close: hostapd stopped, client fence written, dnsmasq failed" \
        wait_until 15 close_failed_on_dnsmasq
    # The close is retried every tick and keeps failing.
    sleep 3
    check "no client started while the close fails" test "$(supplicant_start_stamp)" = "$stamp"
    check "airhatch-wpa-supplicant inactive" supplicant_inactive
    check "wlan0 has no router lease" wlan0_lacks_router_lease
    check "state is not connected: $(status_field state)" not_connected
    check "Reinitialize during the close: sudo rm -f $SUPPLICANT_SOCKET" \
        raspap_sudo /bin/rm -f "$SUPPLICANT_SOCKET"
    check "Reinitialize during the close: sudo wpa_supplicant refused" \
        sudo_refused /sbin/wpa_supplicant -i wlan0 -c /etc/wpa_supplicant/wpa_supplicant.conf -B
    check "no stray wpa_supplicant" no_stray_supplicant
    check "still no client" supplicant_inactive

    # Once a close succeeds, the failed hand-off reopens the hotspot; a retry
    # then joins the network.
    rm -r "$UNITS/dnsmasq.service.d"
    systemctl daemon-reload
    expect_state 20 ap
    check "dnsmasq active again" systemctl -q is-active dnsmasq.service
    check "AP up again" ap_is_up
    check "airhatchd retry" airhatchd retry
    expect_state "$budget" connected
    expect_client_mode
}

scenario_save_reachable_network() {
    local budget
    budget=$(($(test_conf_seconds handoff_delay) + $(test_conf_seconds connect_timeout) + 10))
    check "WiFiManager writeWpaSupplicant: $HOME_SSID" harness write "$HOME_SSID"
    expect_state "$budget" connected
    expect_client_mode
}

first_boot() {
    # The phone stays associated with the hotspot.
    echo 1 >"$STATIONS_FILE"
    scenario_boot_without_networks
    scenario_first_boot_credentials
    scenario_wifi_page_on_ap
    scenario_hotspot_buttons_on_ap
    scenario_reinitialize_on_ap
    scenario_raspap_supplicant_refused
    scenario_crash_on_ap
    scenario_save_on_ap
    scenario_captive_only_in_ap_mode
    scenario_drop
    scenario_wrong_network
    scenario_close_fails_on_dnsmasq
    scenario_save_reachable_network
}

# --- scenarios: second boot -----------------------------------------------------

airhatchd_inactive() {
    ! systemctl -q is-active airhatchd.service
}

client_mode_is_fenced() {
    check "$1: sudo systemctl start hostapd.service" raspap_sudo /bin/systemctl start hostapd.service
    check "$1: hostapd skipped by its lease condition" hostapd_never_started
    check "$1: sudo systemctl restart dnsmasq.service" raspap_sudo /bin/systemctl restart dnsmasq.service
    check "$1: dnsmasq active" systemctl -q is-active dnsmasq.service
    check "$1: no DHCP offer from the device" no_phone_dhcp_lease
    check "$1: wlan0 never has $AP_IP" wlan0_lacks_ap_address
}

scenario_boot_with_reachable_network() {
    check "boot reached multi-user.target" wait_until 120 boot_finished
    check "airhatchd active" systemctl -q is-active airhatchd.service
    expect_state $(($(test_conf_seconds boot_timeout) + 10)) connected
    check "wlan0 has a router lease: $(wlan0_ipv4)" wlan0_has_router_lease
    check "hostapd never started" hostapd_never_started
    check "no nft table" no_nft_table
    check "no DHCP offer from the device" no_phone_dhcp_lease
}

scenario_hard_constraint() {
    local output
    client_mode_is_fenced "airhatchd running"

    drop_in airhatchd.service <<'EOF'
[Service]
Restart=no
EOF
    systemctl daemon-reload
    check "systemctl kill -s KILL airhatchd.service" systemctl kill -s KILL airhatchd.service
    check "airhatchd killed" wait_until 5 airhatchd_inactive
    client_mode_is_fenced "airhatchd killed"
    output=$(raspap_sudo /etc/raspap/hostapd/servicestart.sh --interface wlan0 --seconds 1 || true)
    check "Start hotspot without airhatchd: '$output'" test "$output" = 'airhatch: airhatchd is not running.'
    check "hostapd still never started" hostapd_never_started

    rm -r "$UNITS/airhatchd.service.d"
    systemctl daemon-reload
    check "airhatchd started again" systemctl start airhatchd.service
    expect_state 15 connected
}

scenario_hotspot_buttons_in_client_mode() {
    local output
    output=$(raspap_sudo /etc/raspap/hostapd/servicestart.sh --interface wlan0 --seconds 1 || true)
    check "Start hotspot in client mode: '$output'" test "$output" = \
        'airhatch: Wi-Fi client is active; the hotspot opens automatically when Wi-Fi is unavailable.'
    sleep 3
    check "still connected" state_is connected
    check "no AP" ap_is_closed
}

scenario_daemon_restart_while_connected() {
    local pid
    pid=$(systemctl show -P MainPID airhatch-wpa-supplicant.service)
    check "supplicant running: pid $pid" test "$pid" != 0
    check "systemctl restart airhatchd.service" systemctl restart airhatchd.service
    expect_state 15 connected
    check "supplicant kept (same MainPID)" \
        test "$(systemctl show -P MainPID airhatch-wpa-supplicant.service)" = "$pid"
}

# overlap_sample: the supplicant runs while the captive table exists. The
# supplicant is read first, so a hit means the table outlived its start.
overlap_sample() {
    systemctl -q is-active airhatch-wpa-supplicant.service && has_nft_table
}

# A captive table left behind while airhatchd was down is removed before
# airhatchd starts a client.
scenario_leftover_table_at_start() {
    local sampler
    drop_in airhatchd.service <<'UNIT'
[Service]
Restart=no
UNIT
    systemctl daemon-reload
    check "systemctl stop airhatchd.service" systemctl stop airhatchd.service
    check "systemctl stop airhatch-wpa-supplicant.service" systemctl stop airhatch-wpa-supplicant.service
    check "leftover nft table inet airhatch added" nft -f - <<'NFT'
table inet airhatch {
  chain forward {
    type filter hook forward priority filter; policy accept;
    oifname "wlan0" drop
  }
}
NFT
    check "the leftover table exists" has_nft_table
    rm -r "$UNITS/airhatchd.service.d"
    systemctl daemon-reload
    : >"$SMOKE_RUN/overlap"
    (
        while :; do
            if overlap_sample; then
                echo overlap >>"$SMOKE_RUN/overlap"
            fi
            sleep 0.05
        done
    ) &
    sampler=$!
    check "airhatchd started" systemctl start airhatchd.service
    expect_state $(($(test_conf_seconds boot_timeout) + 10)) connected
    kill "$sampler"
    wait "$sampler" 2>/dev/null || true
    check "nft table removed" no_nft_table
    check "never a running supplicant while the table existed" test ! -s "$SMOKE_RUN/overlap"
    check "wlan0 has a router lease: $(wlan0_ipv4)" wlan0_has_router_lease
}

second_boot() {
    scenario_boot_with_reachable_network
    scenario_hard_constraint
    scenario_hotspot_buttons_in_client_mode
    scenario_daemon_restart_while_connected
    scenario_leftover_table_at_start
    check "hostapd never started during this boot" hostapd_never_started
}

# --- main -----------------------------------------------------------------------

show_diagnostics() {
    echo "--- airhatchd status"
    airhatchd status || true
    echo "--- journal: airhatchd, airhatch-firstboot, airhatch-wpa-supplicant, hostapd"
    journalctl -b --no-pager -n 120 -u airhatchd -u airhatch-firstboot \
        -u airhatch-wpa-supplicant -u hostapd || true
}

main() {
    case ${1:-} in
        boot) boot ;;
        first-boot) first_boot ;;
        second-boot) second_boot ;;
        *) die "usage: ${0##*/} boot|first-boot|second-boot" ;;
    esac
    if ! report; then
        show_diagnostics
        return 1
    fi
}

main "$@"
