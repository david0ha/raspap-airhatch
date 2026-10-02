package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/david0ha/raspap-airhatch/daemon/internal/api"
	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
	"github.com/david0ha/raspap-airhatch/daemon/internal/servicestart"
)

type cli struct {
	app    app
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	root, err := os.MkdirTemp("", "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	c := &cli{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	c.app = app{layout: paths.Rooted(root), stdout: c.stdout, stderr: c.stderr, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cmds: runner.NewFake()}
	return c
}

func (c *cli) exec(args ...string) int {
	c.stdout.Reset()
	c.stderr.Reset()
	return c.app.main(context.Background(), args)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestUsage(t *testing.T) {
	c := newCLI(t)
	if c.exec() != 2 || c.exec("reboot") != 2 || !strings.HasPrefix(c.stderr.String(), "usage:") {
		t.Fatal(c.stderr.String())
	}
}

func TestRunConfigErrorExits2NamingTheLine(t *testing.T) {
	c := newCLI(t)
	write(t, c.app.layout.DaemonConf, "tick=1s\nretry_interval=1s\n")
	if code := c.exec("run"); code != 2 || !strings.Contains(c.stderr.String(), "line 2") {
		t.Fatalf("%d %q", code, c.stderr.String())
	}
}

func TestRenderDhcpcd(t *testing.T) {
	c := newCLI(t)
	upstream, err := os.ReadFile("../../testdata/upstream/dhcpcd.conf")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../../testdata/dhcpcd/upstream-ap.golden")
	if err != nil {
		t.Fatal(err)
	}
	write(t, c.app.layout.RaspAPDhcpcd, string(upstream))
	write(t, c.app.layout.ModeFile, "ap\n")
	if code := c.exec("render-dhcpcd"); code != 0 {
		t.Fatal(c.stderr.String())
	}
	if got, _ := os.ReadFile(c.app.layout.DhcpcdConf); !bytes.Equal(got, golden) {
		t.Fatalf("%s", got)
	}
	if err := os.RemoveAll(c.app.layout.RunDir); err != nil {
		t.Fatal(err)
	}
	if code := c.exec("render-dhcpcd"); code != 1 {
		t.Fatal("a write failure must exit 1")
	}
}

func step(cmdline string) runner.Step {
	fields := strings.Fields(cmdline)
	return runner.Step{Name: fields[0], Args: fields[1:]}
}

// mkdirProc gives the reaper an empty /proc.
func mkdirProc(t *testing.T, c *cli) {
	t.Helper()
	if err := os.MkdirAll(c.app.layout.Proc, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupExits1WhenReapingFails(t *testing.T) {
	c := newCLI(t)
	if code := c.exec("cleanup"); code != 1 {
		t.Fatal(code)
	}
	if n := len(c.app.cmds.(*runner.Fake).Calls()); n != 0 {
		t.Fatalf("cleanup ran %d commands after a failed reap", n)
	}
}

// cleanup is the unit's ExecStopPost: after airhatchd exits, crash or not,
// an AP it left behind is closed and the captive table deleted.
func TestCleanupClosesALeftAP(t *testing.T) {
	c := newCLI(t)
	nft := step("nft -f -")
	nft.Input = []byte(captive.CloseBatch)
	fake := runner.NewFake(
		step("systemctl stop hostapd.service hostapd@*.service"),
		step("systemctl list-units --plain --no-legend --state=active,activating,deactivating,reloading hostapd.service hostapd@*.service"),
		step("systemctl restart dnsmasq.service"),
		step("systemctl is-active dnsmasq.service"),
		nft,
		step("ip -4 addr flush dev wlan0"),
		step("systemctl restart dhcpcd.service"),
	)
	c.app.cmds = fake
	mkdirProc(t, c)
	write(t, c.app.layout.APLease, "")
	write(t, c.app.layout.ModeFile, "ap\n")
	write(t, c.app.layout.DnsmasqConf, "# airhatch: ap\n")
	if code := c.exec("cleanup"); code != 0 {
		t.Fatalf("%d %q", code, c.stderr.String())
	}
	if n := fake.Remaining(); n != 0 {
		t.Fatalf("%d commands did not run: %v", n, fake.Calls())
	}
	if mode, _ := os.ReadFile(c.app.layout.ModeFile); string(mode) != "client\n" {
		t.Fatalf("mode %q", mode)
	}
	if _, err := os.Stat(c.app.layout.APLease); !os.IsNotExist(err) {
		t.Fatal("lease kept")
	}
}

func TestCleanupExits1WhenHostapdDoesNotStop(t *testing.T) {
	c := newCLI(t)
	stop := step("systemctl stop hostapd.service hostapd@*.service")
	stop.Err = os.ErrDeadlineExceeded
	c.app.cmds = runner.NewFake(stop)
	mkdirProc(t, c)
	write(t, c.app.layout.APLease, "")
	if code := c.exec("cleanup"); code != 1 {
		t.Fatal(code)
	}
}

func TestFirstbootErrorExits1(t *testing.T) {
	c := newCLI(t)
	if code := c.exec("firstboot"); code != 1 {
		t.Fatal(code)
	}
}

type backendStub struct{}

func (backendStub) Status() any { return map[string]any{"version": 1, "state": "ap"} }
func (backendStub) OpenAP(context.Context) (string, error) {
	return "Opening the hotspot.", nil
}
func (backendStub) RetryClient(context.Context) (string, error) { return "Trying again.", nil }
func (backendStub) Auto(context.Context) (string, error)        { return "Back to automatic.", nil }
func (backendStub) ServiceStart(_ context.Context, args []string) ([]string, error) {
	return []string{servicestart.Parse(args).Interface}, nil
}

func serveStub(t *testing.T, c *cli, role api.Role) {
	t.Helper()
	if err := os.MkdirAll(c.app.layout.RunDir, 0755); err != nil {
		t.Fatal(err)
	}
	srv, err := api.Listen(api.Options{Path: c.app.layout.APISock, GID: os.Getgid(),
		Roles: map[uint32]api.Role{uint32(os.Getuid()): role}, Backend: backendStub{}, Log: c.app.log})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestClientCommands(t *testing.T) {
	c := newCLI(t)
	serveStub(t, c, api.RoleRoot)
	if c.exec("status") != 0 || c.stdout.String() != "{\n  \"state\": \"ap\",\n  \"version\": 1\n}\n" {
		t.Fatalf("%q", c.stdout.String())
	}
	for cmd, want := range map[string]string{"open-ap": "Opening the hotspot.\n", "retry": "Trying again.\n", "auto": "Back to automatic.\n"} {
		if c.exec(cmd) != 0 || c.stdout.String() != want {
			t.Fatalf("%s: %q", cmd, c.stdout.String())
		}
	}
	if c.exec("servicestart", "--interface", "br0", "--seconds", "1") != 0 || c.stdout.String() != "br0\n" {
		t.Fatalf("%q", c.stdout.String())
	}
}

func TestServicestartForbiddenForTheWebUser(t *testing.T) {
	c := newCLI(t)
	serveStub(t, c, api.RoleWeb)
	if c.exec("servicestart", "--interface", "wlan0") != 1 || !strings.Contains(c.stderr.String(), "(forbidden)") {
		t.Fatalf("%q", c.stderr.String())
	}
}

func TestUnreachableDaemon(t *testing.T) {
	c := newCLI(t)
	if c.exec("servicestart", "--interface", "wlan0", "--seconds", "1") != 1 || c.stdout.String() != servicestart.Unreachable+"\n" {
		t.Fatalf("%q", c.stdout.String())
	}
	for _, cmd := range []string{"status", "open-ap", "retry", "auto"} {
		if c.exec(cmd) != 1 || c.stderr.String() != servicestart.Unreachable+"\n" {
			t.Fatalf("%s: %q", cmd, c.stderr.String())
		}
	}
}
