#!/bin/bash
# raspap-airhatch: RaspAP runs this script through sudo to start, restart or
# stop the hotspot. airhatchd owns wlan0's mode, so the request goes to it.
exec /usr/sbin/airhatchd servicestart "$@"
