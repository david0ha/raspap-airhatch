package standin

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
	"github.com/david0ha/raspap-airhatch/daemon/internal/wpactrl"
)

type fixture struct {
	server   *Server
	path     string
	local    string
	triggers chan string
}

func start(t *testing.T, conf string) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{path: filepath.Join(dir, "wpa_supplicant", "wlan0"), local: t.TempDir(), triggers: make(chan string, 4)}
	server, err := Start(Options{
		Path: f.path, GID: os.Getgid(), MAC: "dc:a6:32:01:1a:2b",
		Networks: func() []raspapconf.Network {
			w, err := raspapconf.ReadWpa(conf)
			if err != nil {
				t.Error(err)
			}
			return w.Networks
		},
		Triggers: f.triggers,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.server = server
	t.Cleanup(func() { server.Close() })
	return f
}

func (f *fixture) ask(t *testing.T, cmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reply, err := wpactrl.Command(ctx, f.path, f.local, cmd)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return reply
}

const fixtures = "../../testdata/upstream/"

func TestRoundTripOfEveryCommand(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-wpa-open.conf")
	cases := map[string]string{
		"PING":                 "PONG\n",
		"STATUS":               "wpa_state=DISCONNECTED\naddress=dc:a6:32:01:1a:2b\n",
		"LIST_NETWORKS":        "network id / ssid / bssid / flags\n0\tHome\tany\t\n1\tCafe Free\tany\t\n",
		"ENABLE_NETWORK 0":     "OK\n",
		"DISABLE_NETWORK 0":    "OK\n",
		"REMOVE_NETWORK 1":     "OK\n",
		"DISCONNECT":           "OK\n",
		"REASSOCIATE":          "OK\n",
		"SCAN":                 "OK\n",
		"RECONFIGURE":          "OK\n",
		"SCAN_RESULTS":         "bssid / frequency / signal level / flags / ssid\n",
		"TERMINATE":            "OK\n",
		"SAVE_CONFIG":          "UNKNOWN COMMAND\n",
		"SELECT_NETWORK 7":     "FAIL\n",
		"SET_NETWORK 9 ssid x": "FAIL\n",
	}
	for cmd, want := range cases {
		if got := f.ask(t, cmd); got != want {
			t.Errorf("%s: %q, want %q", cmd, got, want)
		}
	}
	if got := f.ask(t, "PING"); got != "PONG\n" {
		t.Fatalf("stand-in stopped after TERMINATE: %q", got)
	}
}

func TestListNetworksEncodingAndFlags(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "wpa.conf")
	text := "ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nnetwork={\n\tssid=P\"Caf\\xc3\\xa9\"\n\tkey_mgmt=NONE\n}\nnetwork={\n\tssid=\"Off\"\n\tdisabled=1\n}\n"
	if err := os.WriteFile(conf, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	f := start(t, conf)
	want := "network id / ssid / bssid / flags\n0\tCaf\\xc3\\xa9\tany\t\n1\tOff\tany\t[DISABLED]\n"
	if got := f.ask(t, "LIST_NETWORKS"); got != want {
		t.Fatalf("%q", got)
	}
	// The list is read from the file on every request.
	if err := os.WriteFile(conf, []byte("network={\n\tssid=\"New\"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := f.ask(t, "LIST_NETWORKS"); got != "network id / ssid / bssid / flags\n0\tNew\tany\t\n" {
		t.Fatalf("not re-read: %q", got)
	}
}

func TestSelectNetworkTriggersHandoff(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-wpa-open.conf")
	if got := f.ask(t, "SELECT_NETWORK 1"); got != "OK\n" {
		t.Fatal(got)
	}
	select {
	case ssid := <-f.triggers:
		if ssid != "Cafe Free" {
			t.Fatalf("trigger %q", ssid)
		}
	case <-time.After(time.Second):
		t.Fatal("no trigger")
	}
}

func TestAddedNetworkKeepsOnlyItsSSID(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-one-wpa.conf")
	if got := f.ask(t, "ADD_NETWORK"); got != "1\n" {
		t.Fatalf("first scratch id %q", got)
	}
	if got := f.ask(t, "ADD_NETWORK"); got != "2\n" {
		t.Fatalf("second scratch id %q", got)
	}
	for _, cmd := range []string{`SET_NETWORK 1 ssid "Guest"`, `SET_NETWORK 1 psk "supersecret99"`,
		"SET_NETWORK 2 ssid 4f6666696365", "SET_NETWORK 2 key_mgmt NONE"} {
		if got := f.ask(t, cmd); got != "OK\n" {
			t.Fatalf("%s: %q", cmd, got)
		}
	}
	state := fmt.Sprintf("%#v", f.server.scratchSnapshot())
	if strings.Contains(state, "supersecret99") || !strings.Contains(state, "Guest") || !strings.Contains(state, "Office") {
		t.Fatalf("scratch state %s", state)
	}
	f.ask(t, "SELECT_NETWORK 2")
	if ssid := <-f.triggers; ssid != "Office" {
		t.Fatalf("trigger %q", ssid)
	}
	if got := f.ask(t, "REMOVE_NETWORK 1"); got != "OK\n" || len(f.server.scratchSnapshot()) != 1 {
		t.Fatalf("remove: %q %v", got, f.server.scratchSnapshot())
	}
}

func TestSocketModeAndGroup(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-empty.conf")
	for _, p := range []string{f.path, filepath.Dir(f.path)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0770 || int(info.Sys().(*syscall.Stat_t).Gid) != os.Getgid() {
			t.Errorf("%s: %v gid %d", p, info.Mode(), info.Sys().(*syscall.Stat_t).Gid)
		}
	}
	if info, _ := os.Stat(f.path); info.Mode()&os.ModeSocket == 0 {
		t.Fatal("not a socket")
	}
}

func TestGoneAfterUnlinkAndRebind(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-empty.conf")
	if f.server.Gone() {
		t.Fatal("fresh stand-in reported gone")
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	if !f.server.Gone() {
		t.Fatal("unlinked stand-in not reported gone")
	}
	f.server.Close()
	again, err := Start(f.server.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := f.ask(t, "PING"); got != "PONG\n" {
		t.Fatalf("after rebind: %q", got)
	}
}

func TestCloseUnlinksOnlyItsOwnSocket(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-empty.conf")
	if err := f.server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f.path); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
	g := start(t, fixtures+"wpa_supplicant-empty.conf")
	_ = os.Remove(g.path)
	if err := os.WriteFile(g.path, []byte("someone else"), 0644); err != nil {
		t.Fatal(err)
	}
	g.server.Close()
	if data, _ := os.ReadFile(g.path); string(data) != "someone else" {
		t.Fatal("Close removed a file it did not create")
	}
}

func TestStartRefusesLiveOrForeignPath(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-empty.conf")
	if _, err := Start(f.server.opts); err == nil {
		t.Fatal("bound over a live socket")
	}
	path := filepath.Join(t.TempDir(), "wlan0")
	_ = os.WriteFile(path, nil, 0644)
	opts := f.server.opts
	opts.Path = path
	if _, err := Start(opts); err == nil {
		t.Fatal("replaced a regular file")
	}
}

func TestStartReplacesStaleSocket(t *testing.T) {
	f := start(t, fixtures+"wpa_supplicant-empty.conf")
	opts := f.server.opts
	f.server.conn.Close() // a dead socket file stays behind
	again, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := f.ask(t, "PING"); got != "PONG\n" {
		t.Fatal(got)
	}
}

func (s *Server) scratchSnapshot() map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.scratch)
}
