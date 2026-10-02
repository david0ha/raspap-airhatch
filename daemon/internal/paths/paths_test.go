package paths

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultMatchesSpec(t *testing.T) {
	l := Default()
	want := map[string]string{
		l.APLease:     "/run/airhatch/ap.lease",
		l.ModeFile:    "/run/airhatch/mode",
		l.DnsmasqConf: "/run/airhatch/dnsmasq.conf",
		l.DhcpcdConf:  "/run/airhatch/dhcpcd.conf",
		l.APISock:     "/run/airhatch/api.sock",
		l.CtrlStandIn: "/var/run/wpa_supplicant/wlan0",
		l.HostapdCtrl: "/var/run/hostapd/wlan0",
		l.Notice:      "/boot/raspap-airhatch.txt",
	}
	for got, expected := range want {
		if got != expected {
			t.Errorf("path %q, want %q", got, expected)
		}
	}
}

func TestRootedMovesEveryField(t *testing.T) {
	root := t.TempDir()
	rooted := Rooted(root)
	if n := reflect.TypeOf(rooted).NumField(); n != len(rooted.fields()) {
		t.Fatalf("fields() lists %d of %d fields", len(rooted.fields()), n)
	}
	def := Default()
	defFields := def.fields()
	for i, p := range rooted.fields() {
		if !strings.HasPrefix(*p, root+"/") || *p != filepath.Join(root, *defFields[i]) {
			t.Errorf("field %d = %q", i, *p)
		}
	}
}
