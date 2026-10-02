package radio

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

// stepClock advances by the requested duration whenever code waits on it,
// so polling loops run instantly and deterministically.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// watchRunner runs a check before each command reaches the strict fake, so
// tests can assert what is on disk at the moment a command runs.
type watchRunner struct {
	fake   *runner.Fake
	checks map[string]func()
}

func (w *watchRunner) before(name string, args []string) {
	if check := w.checks[name+" "+strings.Join(args, " ")]; check != nil {
		check()
	}
}

func (w *watchRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	w.before(name, args)
	return w.fake.Run(ctx, name, args...)
}

func (w *watchRunner) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	w.before(name, args)
	return w.fake.RunInput(ctx, input, name, args...)
}

// ctrlServer is a scripted wpa_supplicant/hostapd control socket.
type ctrlServer struct {
	conn   *net.UnixConn
	mu     sync.Mutex
	script map[string][]string // replies per command; the last one repeats
	events []string            // sent right after ATTACH's OK
	got    []string
}

func newCtrl(t *testing.T, path string, script map[string][]string) *ctrlServer {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	s := &ctrlServer{conn: conn, script: script}
	go s.serve()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *ctrlServer) serve() {
	buf := make([]byte, 4096)
	for {
		n, addr, err := s.conn.ReadFromUnix(buf)
		if err != nil {
			return
		}
		cmd := string(buf[:n])
		s.mu.Lock()
		s.got = append(s.got, cmd)
		replies := s.script[cmd]
		reply := "UNKNOWN COMMAND\n"
		if len(replies) > 0 {
			reply = replies[0]
			if len(replies) > 1 {
				s.script[cmd] = replies[1:]
			}
		}
		events := s.events
		s.mu.Unlock()
		_, _ = s.conn.WriteToUnix([]byte(reply), addr)
		if cmd == "ATTACH" {
			for _, e := range events {
				_, _ = s.conn.WriteToUnix([]byte(e), addr)
			}
		}
	}
}

func (s *ctrlServer) set(cmd string, replies ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script[cmd] = replies
}

func (s *ctrlServer) setEvents(events ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = events
}

func (s *ctrlServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

type fakePortal struct {
	mu     sync.Mutex
	addr   netip.AddrPort
	closed bool
	fail   error
}

func (p *fakePortal) listen(addr netip.AddrPort, _ http.Handler) (io.Closer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return nil, p.fail
	}
	p.addr, p.closed = addr, false
	return p, nil
}

func (p *fakePortal) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

type kills struct {
	mu   sync.Mutex
	sent []string
}

func (k *kills) record(pid int, sig syscall.Signal) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sent = append(k.sent, sig.String()+" "+strconv.Itoa(pid))
}

type rig struct {
	t        *testing.T
	l        paths.Layout
	fake     *runner.Fake
	watch    *watchRunner
	radio    *Radio
	hostapd  *ctrlServer
	portal   *fakePortal
	addrs    []netip.Prefix
	selected chan string
	clock    *stepClock
}

const testdata = "../../testdata/"

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, to, string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(data)
}

// newRig lays out a device after first boot: RaspAP's upstream files with
// the per-device passphrase, raspap.auth, and the given wpa_supplicant.conf.
func newRig(t *testing.T, wpaFixture string, steps ...runner.Step) *rig {
	t.Helper()
	// A short root keeps socket paths within sun_path's 108 bytes.
	root, err := os.MkdirTemp("", "radio")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	l := paths.Rooted(root)
	hostapd, err := os.ReadFile(testdata + "upstream/hostapd.conf")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, l.HostapdConf, strings.Replace(string(hostapd), "wpa_passphrase=ChangeMe", "wpa_passphrase=Qx7mKp2vRt9w", 1))
	copyFile(t, testdata+"upstream/dhcpcd.conf", l.RaspAPDhcpcd)
	copyFile(t, testdata+"image/lighttpd.conf", l.LighttpdConf)
	if wpaFixture != "" {
		copyFile(t, testdata+"upstream/"+wpaFixture, l.WpaConf)
	}
	writeFile(t, l.RaspAPAuth, "admin\n"+testAuthHash+"\n0\n")
	writeFile(t, l.WlanMAC, "dc:a6:32:01:1a:2b\n")
	writeFile(t, l.DnsmasqConf, captive.DnsmasqClient)
	for _, dir := range []string{l.Proc, filepath.Dir(l.ScanCache), filepath.Dir(l.CtrlDir)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.APStepTimeout = time.Second
	r := &rig{t: t, l: l, fake: runner.NewFake(steps...), portal: &fakePortal{}, selected: make(chan string, 4),
		clock: &stepClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}}
	r.watch = &watchRunner{fake: r.fake, checks: map[string]func(){}}
	r.hostapd = newCtrl(t, l.HostapdCtrl, map[string][]string{"STATUS": {"state=ENABLED\nnum_sta[0]=0\n"}})
	if err := os.MkdirAll(l.RunDir, 0755); err != nil {
		t.Fatal(err)
	}
	r.radio = New(Options{
		Layout: l, Config: cfg, Runner: r.watch, Clock: r.clock,
		Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reaper:             Reaper{Proc: l.Proc, Kill: func(int, syscall.Signal) error { return nil }, Clock: r.clock},
		Addrs:              func() ([]netip.Prefix, error) { return r.addrs, nil },
		Listen:             r.portal.listen,
		AfterFailedHandoff: func() bool { return false },
		Selected:           r.selected,
		NetdevGID:          os.Getgid(), WebUID: os.Getuid(), WebGID: os.Getgid(),
	})
	return r
}

// done asserts that every scripted command ran.
func (r *rig) done() {
	r.t.Helper()
	if n := r.fake.Remaining(); n != 0 {
		calls := r.fake.Calls()
		last := calls[len(calls)-1]
		r.t.Fatalf("%d scripted commands did not run; last call %s %v", n, last.Name, last.Args)
	}
}

func step(cmdline string) runner.Step {
	fields := strings.Fields(cmdline)
	return runner.Step{Name: fields[0], Args: fields[1:]}
}

func failing(s runner.Step) runner.Step {
	s.Err = errors.New("exit status 1")
	return s
}

const scanOutput = "BSS 00:11:22:33:44:55(on wlan0)\n\tSSID: Home\n\tsignal: -48.00 dBm\n"

func openSteps() []runner.Step {
	nft := step("nft -f -")
	nft.Input = []byte(captive.OpenBatch)
	scan := step("iw dev wlan0 scan")
	scan.Output = []byte(scanOutput)
	return []runner.Step{
		authStep(testAuthHash, "custom"),
		step("systemctl stop airhatch-wpa-supplicant.service"),
		step("ip link set wlan0 up"),
		scan,
		step("systemctl restart dhcpcd.service"),
		step("ip -4 addr flush dev wlan0"),
		step("ip addr add 10.3.141.1/24 dev wlan0"),
		nft,
		step("systemctl restart dnsmasq.service"),
		step("systemctl is-active dnsmasq.service"),
		step("systemctl start hostapd.service"),
		step("systemctl start raspap-network-activity@wlan0.service"),
	}
}

func closeSteps() []runner.Step {
	return []runner.Step{
		step("systemctl stop hostapd.service hostapd@*.service"),
		listHostapd(),
		step("systemctl restart dnsmasq.service"),
		step("systemctl is-active dnsmasq.service"),
		nftCloseStep(),
		step("ip -4 addr flush dev wlan0"),
		step("systemctl restart dhcpcd.service"),
	}
}

// listHostapd lists the running hostapd units; none by default.
func listHostapd() runner.Step {
	return step("systemctl list-units --plain --no-legend --state=active,activating,deactivating,reloading hostapd.service hostapd@*.service")
}

func concat(groups ...[]runner.Step) []runner.Step {
	var out []runner.Step
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

const (
	// testAuthHash is the rig's admin hash, of a password that is not "secret".
	testAuthHash = "$2y$10$abcdefghijklmnopqrstuuJ9gU7j5bm4bvZB5ZQWyJw8U0nJ0j0aW"
	// upstreamDefaultHash is RaspAP's built-in admin/secret (U:src/RaspAP/Auth/HTTPAuth.php:28-31).
	upstreamDefaultHash = "$2y$10$YKIyWAmnQLtiJAy6QgHQ.eCpY4m.HCEbiHaTgN6.acNC6bDElzt.i"
)

// authStep is php-cli checking an admin hash against RaspAP's default
// password; verdict is what the script prints.
func authStep(hash, verdict string) runner.Step {
	return runner.Step{Name: "php", Args: []string{"-r", defaultLoginScript}, Input: []byte("secret\n" + hash + "\n"), Output: []byte(verdict + "\n")}
}

// Positions in closeSteps().
const (
	listHostapdStep    = 1
	restartDnsmasqStep = 2
	nftCloseStepIndex  = 4
)

func nftCloseStep() runner.Step {
	nft := step("nft -f -")
	nft.Input = []byte(captive.CloseBatch)
	return nft
}

// cleanFreshSteps is the stale cleanup when nothing was left behind.
func cleanFreshSteps() []runner.Step {
	return []runner.Step{listHostapd(), nftCloseStep()}
}
