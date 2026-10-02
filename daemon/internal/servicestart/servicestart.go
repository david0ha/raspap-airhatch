// Package servicestart understands the arguments RaspAP passes to
// /etc/raspap/hostapd/servicestart.sh and words the replies the shim prints
// (docs/API.md §5).
package servicestart

import (
	"strings"
	"time"
)

// Request is one servicestart.sh invocation.
type Request struct {
	Interface string
	Stop      bool
}

// Parse follows upstream's option loop (U:installers/servicestart.sh:13-35):
// -i/--interface, -s/--seconds and -a/--action take a value; anything else
// is ignored. Without --interface airhatchd's only interface, wlan0, applies.
func Parse(args []string) Request {
	r := Request{Interface: "wlan0"}
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			break
		}
		switch args[i] {
		case "-i", "--interface":
			r.Interface = args[i+1]
		case "-a", "--action":
			r.Stop = args[i+1] == "stop"
		case "-s", "--seconds":
		default:
			continue
		}
		i++
	}
	return r
}

// Supported reports whether the request targets wlan0 (not bridged br0 or
// the AP+client uap0).
func (r Request) Supported() bool { return r.Interface == "wlan0" }

// Reply lines, asserted verbatim by the integration tests.
const (
	Unsupported  = "airhatch: RaspAP bridged and AP+client modes are not supported by raspap-airhatch; nothing changed."
	ClientActive = "airhatch: Wi-Fi client is active; the hotspot opens automatically when Wi-Fi is unavailable."
	Restarting   = "airhatch: restarting the hotspot with the current settings."
	HandoffBusy  = "airhatch: a Wi-Fi hand-off is in progress; try again in a minute."
	Opening      = "airhatch: opening the hotspot."
	Unreachable  = "airhatch: airhatchd is not running."
)

// Stopped is the reply to --action stop.
func Stopped(hold time.Duration) string {
	return "airhatch: hotspot off; it stays off for " + FormatDuration(hold) + " unless you start it again."
}

// FormatDuration is time.Duration.String without trailing zero units.
func FormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
