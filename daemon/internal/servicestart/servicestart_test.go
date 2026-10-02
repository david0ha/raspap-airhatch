package servicestart

import (
	"testing"
	"time"
)

func TestParseHostapdPhpArgv(t *testing.T) {
	cases := []struct {
		args []string
		want Request
	}{
		{[]string{"--interface", "wlan0", "--seconds", "1"}, Request{Interface: "wlan0"}},
		{[]string{"--interface", "br0", "--seconds", "1"}, Request{Interface: "br0"}},
		{[]string{"--interface", "uap0", "--seconds", "1"}, Request{Interface: "uap0"}},
		{[]string{"--action", "stop"}, Request{Interface: "wlan0", Stop: true}},
		{[]string{"-i", "wlan0", "-a", "stop", "-s", "2"}, Request{Interface: "wlan0", Stop: true}},
		{[]string{"--seconds", "1"}, Request{Interface: "wlan0"}},
		{[]string{"--bogus", "--interface", "uap0", "stray"}, Request{Interface: "uap0"}},
		{[]string{"--action", "start"}, Request{Interface: "wlan0"}},
		{[]string{"--interface"}, Request{Interface: "wlan0"}},
		{nil, Request{Interface: "wlan0"}},
	}
	for _, c := range cases {
		if got := Parse(c.args); got != c.want {
			t.Errorf("%q: %+v", c.args, got)
		}
	}
}

func TestSupported(t *testing.T) {
	if !(Request{Interface: "wlan0"}).Supported() || (Request{Interface: "br0"}).Supported() {
		t.Fatal("Supported")
	}
}

func TestStoppedLineFormatsHold(t *testing.T) {
	cases := map[time.Duration]string{
		15 * time.Minute:            "15m",
		time.Hour:                   "1h",
		time.Hour + 30*time.Minute:  "1h30m",
		30 * time.Second:            "30s",
		10 * time.Second:            "10s",
		90 * time.Second:            "1m30s",
		0:                           "0s",
		2*time.Hour + 5*time.Second: "2h0m5s",
	}
	for d, want := range cases {
		if got := Stopped(d); got != "airhatch: hotspot off; it stays off for "+want+" unless you start it again." {
			t.Errorf("%v: %q", d, got)
		}
	}
}
