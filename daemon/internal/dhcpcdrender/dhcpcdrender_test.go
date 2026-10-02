package dhcpcdrender

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testdata = "../../testdata/"

func TestGoldenRenders(t *testing.T) {
	cases := []struct {
		source, golden string
		mode           Mode
	}{
		{"upstream/dhcpcd.conf", "dhcpcd/upstream-client.golden", Client},
		{"upstream/dhcpcd.conf", "dhcpcd/upstream-ap.golden", AP},
		{"dhcpcd/variant.conf", "dhcpcd/variant-client.golden", Client},
		{"dhcpcd/variant.conf", "dhcpcd/variant-ap.golden", AP},
	}
	for _, c := range cases {
		conf, err := os.ReadFile(testdata + c.source)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(testdata + c.golden)
		if err != nil {
			t.Fatal(err)
		}
		if got := Render(conf, c.mode); string(got) != string(want) {
			t.Errorf("%s (%s):\n%s\nwant:\n%s", c.source, c.mode, got, want)
		}
	}
}

func TestMissingConfRendersModeLinesOnly(t *testing.T) {
	for mode, golden := range map[Mode]string{Client: "missing-client.golden", AP: "missing-ap.golden"} {
		want, _ := os.ReadFile(testdata + "dhcpcd/" + golden)
		if got := Render(nil, mode); string(got) != string(want) {
			t.Errorf("%s: %q", mode, got)
		}
	}
}

func TestUnterminatedLastLine(t *testing.T) {
	got := Render([]byte("hostname\ninterface eth0\nmetric 5"), Client)
	want := "# Rendered by airhatchd render-dhcpcd from /etc/dhcpcd.conf (mode client). Do not edit.\n" +
		"hostname\ninterface eth0\nmetric 5\ninterface wlan0\nnohook wpa_supplicant\n"
	if string(got) != want {
		t.Fatalf("%q", got)
	}
}

func TestReadMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode")
	if ReadMode(path) != Client {
		t.Fatal("absent mode file must be client")
	}
	for content, want := range map[string]Mode{"ap\n": AP, "client\n": Client, "garbage": Client, "ap": AP} {
		_ = os.WriteFile(path, []byte(content), 0644)
		if got := ReadMode(path); got != want {
			t.Errorf("%q: %s", content, got)
		}
	}
}

func TestWriteRendersAtomically(t *testing.T) {
	dir := t.TempDir()
	conf, mode, out := filepath.Join(dir, "dhcpcd.conf"), filepath.Join(dir, "mode"), filepath.Join(dir, "out.conf")
	up, _ := os.ReadFile(testdata + "upstream/dhcpcd.conf")
	_ = os.WriteFile(conf, up, 0644)
	_ = os.WriteFile(mode, []byte("ap\n"), 0644)
	if err := Write(conf, mode, out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	want, _ := os.ReadFile(testdata + "dhcpcd/upstream-ap.golden")
	info, _ := os.Stat(out)
	if string(got) != string(want) || info.Mode().Perm() != 0644 {
		t.Fatalf("%v\n%s", info.Mode(), got)
	}
	if err := Write(conf, mode, filepath.Join(dir, "missing", "out")); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}

func TestDeniesWlan0(t *testing.T) {
	for _, golden := range []string{"upstream-client", "upstream-ap", "variant-client", "variant-ap", "missing-client", "missing-ap"} {
		rendered, err := os.ReadFile(testdata + "dhcpcd/" + golden + ".golden")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := DeniesWlan0(rendered), strings.HasSuffix(golden, "-ap"); got != want {
			t.Errorf("%s: DeniesWlan0 %v, want %v", golden, got, want)
		}
	}
}
