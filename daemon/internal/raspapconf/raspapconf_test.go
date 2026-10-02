package raspapconf

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

const upstream = "../../testdata/upstream/"

func TestReadHostapdUpstream(t *testing.T) {
	h, err := ReadHostapd(upstream + "hostapd.conf")
	if err != nil {
		t.Fatal(err)
	}
	want := Hostapd{Interface: "wlan0", SSID: "RaspAP", Passphrase: "ChangeMe", WPA: "2"}
	if h != want {
		t.Fatalf("%+v", h)
	}
}

func TestReadHostapdIniForms(t *testing.T) {
	dir := t.TempDir()
	missing, err := ReadHostapdIni(filepath.Join(dir, "absent.ini"))
	if err != nil || missing != (HostapdIni{}) {
		t.Fatalf("missing: %+v %v", missing, err)
	}
	path := filepath.Join(dir, "hostapd.ini")
	for text, want := range map[string]HostapdIni{
		"LogEnable = 0\nWifiAPEnable = 1\nBridgedEnable = 0\nWifiManaged = wlan0\n": {WifiAPEnable: true},
		"BridgedEnable=1\nWifiAPEnable=0\n":                                         {BridgedEnable: true},
		"WifiAPEnable = 0\nBridgedEnable = 0\n":                                     {},
	} {
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadHostapdIni(path)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", text, got, err)
		}
	}
}

func TestReadWpaFixtures(t *testing.T) {
	cases := map[string][]string{
		"empty":      nil,
		"one-wpa":    {"Home"},
		"wpa-open":   {"Home", "Cafe Free"},
		"hex-ssid":   {"Café", "Grün"},
		"passkey":    {"Office"},
		"priorities": {"Home", "Office"},
	}
	for name, ssids := range cases {
		w, err := ReadWpa(upstream + "wpa_supplicant-" + name + ".conf")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w.ControlDir() != "/var/run/wpa_supplicant" {
			t.Errorf("%s: control dir %q", name, w.ControlDir())
		}
		if len(w.Networks) != len(ssids) || Saved(w.Networks) != len(ssids) {
			t.Fatalf("%s: networks %+v", name, w.Networks)
		}
		for i, ssid := range ssids {
			if w.Networks[i].SSID != ssid || w.Networks[i].Disabled {
				t.Errorf("%s[%d] = %+v", name, i, w.Networks[i])
			}
		}
	}
}

func TestReadWpaMissingFileIsEmpty(t *testing.T) {
	w, err := ReadWpa(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(w.Networks) != 0 || w.ControlDir() != "" {
		t.Fatalf("%+v %v", w, err)
	}
}

func TestDisabledAndSSIDlessBlocksAreNotSaved(t *testing.T) {
	text := "ctrl_interface=/var/run/wpa_supplicant\nnetwork={\n\tssid=\"A\"\n\tdisabled=1\n}\nnetwork={\n\tkey_mgmt=NONE\n}\nnetwork={\n\tssid=4142\n}\n"
	w := parseWpa([]byte(text))
	if len(w.Networks) != 3 || Saved(w.Networks) != 1 || !w.Networks[0].Disabled || w.Networks[2].SSID != "AB" {
		t.Fatalf("%+v", w.Networks)
	}
	if w.ControlDir() != "/var/run/wpa_supplicant" {
		t.Fatal(w.ControlDir())
	}
}

func TestDigestIgnoresCommentsAndWhitespace(t *testing.T) {
	a := parseWpa([]byte("network={\n\tssid=\"Home\"\n\t#psk=\"one\"\n\tpsk=abc\n}\n"))
	b := parseWpa([]byte("network={\n  ssid=\"Home\"   \n\n  psk=abc\n}\n"))
	c := parseWpa([]byte("network={\n\tssid=\"Home\"\n\tpsk=abd\n}\n"))
	if a.Networks[0].Digest != b.Networks[0].Digest {
		t.Fatal("comments or whitespace changed the digest")
	}
	if a.Networks[0].Digest == c.Networks[0].Digest {
		t.Fatal("a body change kept the digest")
	}
}

func TestChanged(t *testing.T) {
	read := func(name string) []Network {
		w, err := ReadWpa(upstream + "wpa_supplicant-" + name + ".conf")
		if err != nil {
			t.Fatal(err)
		}
		return w.Networks
	}
	empty, one, two, prio := read("empty"), read("one-wpa"), read("wpa-open"), read("priorities")
	cases := []struct {
		name      string
		prev, cur []Network
		changed   bool
		preferred string
	}{
		{"new network", one, two, true, "Cafe Free"},
		{"first network", empty, one, true, "Home"},
		{"changed body", one, prio, true, "Home"},
		{"delete only", two, one, false, ""},
		{"all deleted", one, empty, false, ""},
		{"ensureWpaSupplicant file", nil, empty, false, ""},
		{"unchanged", two, two, false, ""},
	}
	for _, c := range cases {
		changed, preferred := Changed(c.prev, c.cur)
		if changed != c.changed || preferred != c.preferred {
			t.Errorf("%s: %v %q", c.name, changed, preferred)
		}
	}
	disabled := parseWpa([]byte("network={\n\tssid=\"New\"\n\tdisabled=1\n}\n"))
	if changed, _ := Changed(one, append(append([]Network{}, one...), disabled.Networks...)); changed {
		t.Error("a disabled network triggered a hand-off")
	}
}

func TestSSIDCodecs(t *testing.T) {
	for raw, want := range map[string]string{
		`"Home"`:              "Home",
		`"with \x41 literal"`: `with \x41 literal`,
		`P"Caf\xc3\xa9"`:      "Café",
		`P"tab\there\"q\\"`:   "tab\there\"q\\",
		`P"oct\101"`:          "octA",
		`486f6d65`:            "Home",
	} {
		got, ok := DecodeSSID(raw)
		if !ok || got != want {
			t.Errorf("DecodeSSID(%s) = %q %v", raw, got, ok)
		}
	}
	for _, bad := range []string{`"open`, "zz", "abc", ""} {
		if _, ok := DecodeSSID(bad); ok {
			t.Errorf("DecodeSSID(%q) accepted", bad)
		}
	}
	for ssid, want := range map[string]string{
		"Home":       "Home",
		"Café":       `Caf\xc3\xa9`,
		"a\"b\\c":    `a\"b\\c`,
		"t\tn\nr\re": `t\tn\nr\re`,
		"\x1b\x7f":   `\e\x7f`,
	} {
		if got := EncodeText(ssid); got != want {
			t.Errorf("EncodeText(%q) = %q", ssid, got)
		}
		if back := DecodeText(EncodeText(ssid)); back != ssid {
			t.Errorf("round trip %q -> %q", ssid, back)
		}
	}
}

func TestDhcpcdAPAddress(t *testing.T) {
	up, _ := os.ReadFile(upstream + "dhcpcd.conf")
	if p, ok := APAddress(up); !ok || p != netip.MustParsePrefix("10.3.141.1/24") {
		t.Fatalf("upstream: %v %v", p, ok)
	}
	variant, _ := os.ReadFile("../../testdata/dhcpcd/variant.conf")
	if p, ok := APAddress(variant); !ok || p != netip.MustParsePrefix("10.3.142.1/24") {
		t.Fatalf("variant (last wlan0 block wins): %v %v", p, ok)
	}
	for _, text := range []string{"", "interface eth0\nstatic ip_address=192.168.1.2/24\n",
		"interface wlan0\nstatic ip_address=10.3.141.1\n", "interface wlan0\nstatic ip_address=fd00::1/64\n"} {
		if p, ok := APAddress([]byte(text)); ok {
			t.Errorf("%q: %v", text, p)
		}
	}
}

func TestSplitDhcpcd(t *testing.T) {
	variant, _ := os.ReadFile("../../testdata/dhcpcd/variant.conf")
	global, blocks := SplitDhcpcd(variant)
	if len(global) != 16 || len(blocks) != 4 {
		t.Fatalf("global %d lines, %d blocks", len(global), len(blocks))
	}
	wlan := 0
	for _, b := range blocks {
		if IsWlan0(b) {
			wlan++
		}
	}
	if wlan != 2 || blocks[2][0] != "profile static_wlan0" {
		t.Fatalf("blocks %q", blocks)
	}
}

func TestLighttpdPort(t *testing.T) {
	port, err := LighttpdPort("../../testdata/image/lighttpd.conf")
	if err != nil || port != 80 {
		t.Fatalf("%d %v", port, err)
	}
	dir := t.TempDir()
	if port, err := LighttpdPort(filepath.Join(dir, "absent")); err != nil || port != 80 {
		t.Fatalf("missing: %d %v", port, err)
	}
	path := filepath.Join(dir, "l.conf")
	_ = os.WriteFile(path, []byte("#server.port = 1\nserver.port=8080\n"), 0644)
	if port, err := LighttpdPort(path); err != nil || port != 8080 {
		t.Fatalf("custom: %d %v", port, err)
	}
	_ = os.WriteFile(path, []byte("server.port = http\n"), 0644)
	if _, err := LighttpdPort(path); err == nil {
		t.Fatal("bad port accepted")
	}
}
