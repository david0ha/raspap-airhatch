package radio

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
	"github.com/david0ha/raspap-airhatch/daemon/internal/wpactrl"
)

var ctx = context.Background()

func TestOpenAPExactOrder(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", openSteps()...)
	var problems []string
	expect := func(when string, ok bool) {
		if !ok {
			problems = append(problems, when)
		}
	}
	r.watch.checks["ip link set wlan0 up"] = func() {
		expect("stand-in bound right after the client stop", exists(r.l.CtrlStandIn))
	}
	r.watch.checks["systemctl restart dhcpcd.service"] = func() {
		expect("mode=ap before the dhcpcd restart renders", readFile(t, r.l.ModeFile) == "ap\n")
	}
	r.watch.checks["systemctl restart dnsmasq.service"] = func() {
		calls := r.fake.Calls()
		last := calls[len(calls)-1]
		expect("nft applied before dnsmasq restarts", last.Name == "nft" && string(last.Input) == captive.OpenBatch)
		expect("AP dnsmasq content on disk before dnsmasq restarts",
			readFile(t, r.l.DnsmasqConf) == captive.DnsmasqAP(netip.MustParseAddr("10.3.141.1"), true))
		expect("no lease before dnsmasq", !exists(r.l.APLease))
	}
	r.watch.checks["systemctl start hostapd.service"] = func() {
		expect("lease exists when hostapd starts", exists(r.l.APLease))
	}
	r.watch.checks["systemctl start raspap-network-activity@wlan0.service"] = func() {
		expect("stand-in bound before network activity", exists(r.l.CtrlStandIn))
		expect("portal listening on the AP address", r.portal.addr == netip.MustParseAddrPort("10.3.141.1:8099"))
	}
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	for _, p := range problems {
		t.Error(p)
	}
	if _, open := r.radio.OpenPlan(); !open {
		t.Fatal("not open")
	}
	if seed := readFile(t, filepath.Join(r.l.ScanCache, cacheKey(t, r.l.WpaConf))); seed != scanOutput {
		t.Fatalf("scan cache seed %q", seed)
	}
	reply, err := wpactrl.Command(ctx, r.l.CtrlStandIn, t.TempDir(), "LIST_NETWORKS")
	if err != nil || !strings.Contains(reply, "0\tHome\tany\t") {
		t.Fatalf("stand-in: %q %v", reply, err)
	}
}

func cacheKey(t *testing.T, conf string) string {
	info, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	return "nearby_wifi_stations_" + strconv.FormatInt(info.ModTime().Unix(), 10)
}

func TestSecondOpenIsNoOp(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", openSteps()...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	if n := len(r.fake.Calls()); n != len(openSteps()) {
		t.Fatalf("second open ran commands: %d calls", n)
	}
}

func TestOpenWithoutScanOrCaptive(t *testing.T) {
	open := openSteps()
	steps := concat(open[:2], open[4:7], open[8:]) // no scan, no nft
	r := newRig(t, "wpa_supplicant-empty.conf", steps...)
	r.radio.cfg.ScanTimeout, r.radio.cfg.Captive = 0, false
	r.watch.checks["systemctl restart dnsmasq.service"] = func() {
		if got := readFile(t, r.l.DnsmasqConf); got != "# airhatch: ap\n" {
			t.Errorf("dnsmasq content %q", got)
		}
	}
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	if r.portal.addr.IsValid() {
		t.Fatal("portal started with captive=false")
	}
}

func TestFailedScanIsBestEffort(t *testing.T) {
	steps := openSteps()
	steps[3] = failing(steps[3])
	r := newRig(t, "wpa_supplicant-one-wpa.conf", steps...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
}

func TestPreflightRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(r *rig)
		code   publicerror.Code
	}{
		"bridged":        {func(r *rig) { writeFile(t, r.l.HostapdIni, "BridgedEnable = 1\n") }, publicerror.UnsupportedRaspAPMode},
		"ap+client":      {func(r *rig) { writeFile(t, r.l.HostapdIni, "WifiAPEnable=1\n") }, publicerror.UnsupportedRaspAPMode},
		"uap0":           {func(r *rig) { replaceIn(t, r.l.HostapdConf, "interface=wlan0", "interface=uap0") }, publicerror.UnsupportedRaspAPMode},
		"no hostapd":     {func(r *rig) { os.Remove(r.l.HostapdConf) }, publicerror.UnsupportedRaspAPMode},
		"default psk":    {func(r *rig) { copyFile(t, testdata+"upstream/hostapd.conf", r.l.HostapdConf) }, publicerror.DefaultAPPassphrase},
		"default login":  {func(r *rig) { os.Remove(r.l.RaspAPAuth) }, publicerror.DefaultAdminLogin},
		"no AP address":  {func(r *rig) { writeFile(t, r.l.RaspAPDhcpcd, "hostname\ninterface wlan0\nnogateway\n") }, publicerror.NoAPAddress},
		"no dhcpcd.conf": {func(r *rig) { os.Remove(r.l.RaspAPDhcpcd) }, publicerror.NoAPAddress},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "wpa_supplicant-one-wpa.conf")
			c.mutate(r)
			err := r.radio.OpenAP(ctx)
			if public := publicerror.As(err); public == nil || public.Code != c.code {
				t.Fatalf("%v", err)
			}
			if len(r.fake.Calls()) != 0 {
				t.Fatal("a refused open ran commands")
			}
		})
	}
	r := newRig(t, "wpa_supplicant-one-wpa.conf", authStep(testAuthHash, "custom"))
	writeFile(t, r.l.LighttpdConf, "server.port = 8080\n")
	plan, err := r.radio.Preflight(ctx)
	if err != nil || plan.LighttpdPort != 8080 || plan.SSID != "RaspAP" || plan.Prefix != netip.MustParsePrefix("10.3.141.1/24") {
		t.Fatalf("%+v %v", plan, err)
	}
}

func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	writeFile(t, path, strings.Replace(readFile(t, path), old, new, 1))
}

func assertClosed(t *testing.T, r *rig) {
	t.Helper()
	if exists(r.l.APLease) || readFile(t, r.l.ModeFile) != "client\n" || readFile(t, r.l.DnsmasqConf) != captive.DnsmasqClient {
		t.Fatalf("not closed: lease=%v mode=%q dnsmasq=%q", exists(r.l.APLease), readFile(t, r.l.ModeFile), readFile(t, r.l.DnsmasqConf))
	}
	if _, open := r.radio.OpenPlan(); open {
		t.Fatal("still open")
	}
	if r.radio.closePending {
		t.Fatal("close still pending")
	}
}

func TestOpenRollsBackAtEveryFailingStep(t *testing.T) {
	open := openSteps()
	for i := 4; i <= 10; i++ {
		failAt := i
		t.Run(open[failAt].Name+" "+strings.Join(open[failAt].Args, " "), func(t *testing.T) {
			steps := append([]runner.Step(nil), open[:failAt]...)
			if open[failAt].Args[0] == "is-active" {
				// dnsmasq never becomes active: polled for ap_step_timeout.
				for range 5 {
					steps = append(steps, failing(open[failAt]))
				}
			} else {
				steps = append(steps, failing(open[failAt]))
			}
			r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(steps, closeSteps())...)
			err := r.radio.OpenAP(ctx)
			if public := publicerror.As(err); public == nil || public.Code != publicerror.APStartFailed {
				t.Fatalf("%v", err)
			}
			r.done()
			assertClosed(t, r)
		})
	}
}

func TestOpenRollsBackWhenHostapdNeverEnables(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps()[:10], closeSteps())...)
	r.hostapd.set("STATUS", "state=DISABLED\nnum_sta[0]=0\n")
	if err := r.radio.OpenAP(ctx); publicerror.As(err) == nil {
		t.Fatalf("%v", err)
	}
	r.done()
	assertClosed(t, r)
}

func TestOpenRollsBackWhenStandInCannotBind(t *testing.T) {
	// The stand-in binds right after the client stop, before any AP step.
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps()[:2], closeSteps())...)
	writeFile(t, r.l.CtrlStandIn, "not a socket")
	if err := r.radio.OpenAP(ctx); publicerror.As(err) == nil {
		t.Fatalf("%v", err)
	}
	r.done()
	if exists(r.l.APLease) || readFile(t, r.l.ModeFile) != "client\n" {
		t.Fatal("not rolled back")
	}
}

func TestOpenRollsBackWhenPortalFails(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps()[:10], closeSteps())...)
	r.portal.fail = errors.New("address in use")
	if err := r.radio.OpenAP(ctx); publicerror.As(err) == nil {
		t.Fatalf("%v", err)
	}
	r.done()
	assertClosed(t, r)
}

func TestCloseAPExactOrder(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	var problems []string
	r.watch.checks["systemctl stop hostapd.service hostapd@*.service"] = func() {
		if exists(r.l.APLease) {
			problems = append(problems, "lease still present when hostapd stops")
		}
		if !r.portal.closed {
			problems = append(problems, "portal open when hostapd stops")
		}
		if !exists(r.l.CtrlStandIn) {
			problems = append(problems, "stand-in released before hostapd stops")
		}
	}
	r.watch.checks["systemctl restart dhcpcd.service"] = func() {
		if readFile(t, r.l.DnsmasqConf) != captive.DnsmasqClient {
			problems = append(problems, "client dnsmasq content not written before dhcpcd restarts")
		}
		if readFile(t, r.l.ModeFile) != "client\n" {
			problems = append(problems, "mode not client before dhcpcd restarts")
		}
	}
	if err := r.radio.CloseAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	for _, p := range problems {
		t.Error(p)
	}
	assertClosed(t, r)
	if !r.radio.stoppedByDaemon {
		t.Fatal("close_ap must record that airhatchd stopped hostapd")
	}
}

func TestCloseIsSafeWhenNothingIsOpen(t *testing.T) {
	r := newRig(t, "", closeSteps()...)
	if err := r.radio.CloseAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	assertClosed(t, r)
}

func TestFailedHostapdStopKeepsFences(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), []runner.Step{failing(closeSteps()[0])}, closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	apContent := readFile(t, r.l.DnsmasqConf)
	err := r.radio.CloseAP(ctx)
	if !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("%v", err)
	}
	if readFile(t, r.l.DnsmasqConf) != apContent || readFile(t, r.l.ModeFile) != "ap\n" {
		t.Fatal("fences dropped after a failed hostapd stop")
	}
	if calls := r.fake.Calls(); calls[len(calls)-1].Args[0] != "stop" {
		t.Fatal("close continued after the failed stop")
	}
	if err := r.radio.CloseAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	assertClosed(t, r)
}

func TestShutdownClosesOnlyAnOpenAP(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf")
	if err := r.radio.Shutdown(ctx); err != nil || len(r.fake.Calls()) != 0 {
		t.Fatal("shutdown touched a client")
	}
	r = newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.radio.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	assertClosed(t, r)
}

func TestCleanup(t *testing.T) {
	fresh := newRig(t, "", cleanFreshSteps()...)
	if err := fresh.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	fresh.done()

	left := newRig(t, "", closeSteps()...)
	writeFile(t, left.l.APLease, "")
	writeFile(t, left.l.ModeFile, "ap\n")
	writeFile(t, left.l.DnsmasqConf, captive.DnsmasqAP(netip.MustParseAddr("10.3.141.1"), true))
	if err := left.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	left.done()
	assertClosed(t, left)

	runningUnit := listHostapd()
	runningUnit.Output = []byte("hostapd.service loaded active running Access point and authentication server\n")
	running := newRig(t, "", concat([]runner.Step{runningUnit}, closeSteps())...)
	if err := running.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	running.done()

	content := newRig(t, "", closeSteps()...)
	writeFile(t, content.l.DnsmasqConf, captive.DnsmasqAP(netip.MustParseAddr("10.3.141.1"), true))
	if err := content.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	content.done()
	if readFile(t, content.l.DnsmasqConf) != captive.DnsmasqClient {
		t.Fatal("dnsmasq include not reset")
	}

	stale := newRig(t, "", cleanFreshSteps()...)
	ctrl := newCtrl(t, stale.l.CtrlStandIn, map[string][]string{})
	ctrl.conn.Close()
	if err := stale.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(stale.l.CtrlStandIn) {
		t.Fatal("dead stand-in socket kept")
	}
}

func TestCleanupKeepsALiveSupplicantSocket(t *testing.T) {
	r := newRig(t, "", cleanFreshSteps()...)
	newCtrl(t, r.l.CtrlStandIn, map[string][]string{"PING": {"PONG\n"}})
	if err := r.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(r.l.CtrlStandIn) {
		t.Fatal("removed a running supplicant's socket")
	}
}

func TestMaintainRebindsStandInAndRefreshesSeed(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", openSteps()...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.l.CtrlStandIn); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(r.l.WpaConf, later, later); err != nil {
		t.Fatal(err)
	}
	r.radio.Maintain()
	if reply, err := wpactrl.Command(ctx, r.l.CtrlStandIn, t.TempDir(), "PING"); err != nil || reply != "PONG\n" {
		t.Fatalf("stand-in not rebound: %q %v", reply, err)
	}
	if seed := readFile(t, filepath.Join(r.l.ScanCache, cacheKey(t, r.l.WpaConf))); seed != scanOutput {
		t.Fatalf("seed not rewritten for the new mtime: %q", seed)
	}
}

func TestStandInSelectReachesTheSupervisor(t *testing.T) {
	r := newRig(t, "wpa_supplicant-wpa-open.conf", openSteps()...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if reply, err := wpactrl.Command(ctx, r.l.CtrlStandIn, t.TempDir(), "SELECT_NETWORK 1"); err != nil || reply != "OK\n" {
		t.Fatalf("%q %v", reply, err)
	}
	if ssid := <-r.selected; ssid != "Cafe Free" {
		t.Fatal(ssid)
	}
}

// supplicant scripts the real supplicant's control socket for an attempt.
func supplicant(t *testing.T, r *rig, status ...string) *ctrlServer {
	return newCtrl(t, r.l.CtrlStandIn, map[string][]string{
		"PING": {"PONG\n"}, "ATTACH": {"OK\n"}, "STATUS": status, "RECONFIGURE": {"OK\n"},
		"LIST_NETWORKS":    {"network id / ssid / bssid / flags\n0\tHome\tany\t\n1\tCafe Free\tany\t\n"},
		"SELECT_NETWORK 1": {"OK\n"},
	})
}

var startSupplicant = []runner.Step{
	failing(step("systemctl is-active airhatch-wpa-supplicant.service")),
	step("systemctl start airhatch-wpa-supplicant.service"),
}

func TestClientAttemptSuccess(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", startSupplicant...)
	sup := supplicant(t, r, "wpa_state=SCANNING\n", "wpa_state=COMPLETED\nssid=Home\n")
	r.addrs = []netip.Prefix{netip.MustParsePrefix("fe80::1/64"), netip.MustParsePrefix("192.168.1.23/24")}
	client, err := r.radio.ClientAttempt(ctx, 45*time.Second, "")
	if err != nil || client.SSID != "Home" || client.IPv4 != "192.168.1.23/24" {
		t.Fatalf("%+v %v", client, err)
	}
	r.done()
	if got := strings.Join(sup.commands(), ","); got != "PING,ATTACH,STATUS,STATUS" {
		t.Fatalf("supplicant commands %s", got)
	}
}

func TestClientAttemptClosesAnOpenAPFirst(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps(), startSupplicant)...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.watch.checks["systemctl start airhatch-wpa-supplicant.service"] = func() {
		if exists(r.l.APLease) || readFile(t, r.l.DnsmasqConf) != captive.DnsmasqClient {
			t.Error("client started before the AP closed")
		}
	}
	os.Remove(r.l.CtrlStandIn)
	supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	r.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	if _, err := r.radio.ClientAttempt(ctx, 45*time.Second, ""); err != nil {
		t.Fatal(err)
	}
	r.done()
}

func TestClientAttemptReconfiguresARunningSupplicantAndSelects(t *testing.T) {
	r := newRig(t, "wpa_supplicant-wpa-open.conf", step("systemctl is-active airhatch-wpa-supplicant.service"))
	sup := supplicant(t, r, "wpa_state=COMPLETED\nssid=Cafe Free\n")
	r.addrs = []netip.Prefix{netip.MustParsePrefix("172.16.0.9/16")}
	client, err := r.radio.ClientAttempt(ctx, 45*time.Second, "Cafe Free")
	if err != nil || client.SSID != "Cafe Free" {
		t.Fatalf("%+v %v", client, err)
	}
	r.done()
	if got := strings.Join(sup.commands(), ","); got != "RECONFIGURE,PING,ATTACH,LIST_NETWORKS,SELECT_NETWORK 1,STATUS" {
		t.Fatalf("supplicant commands %s", got)
	}
}

func TestClientAttemptFailures(t *testing.T) {
	stop := step("systemctl stop airhatch-wpa-supplicant.service")
	cases := []struct {
		name   string
		events []string
		status string
		addrs  []netip.Prefix
		code   publicerror.Code
		ssid   string
	}{
		{"wrong key", []string{`<3>CTRL-EVENT-SSID-TEMP-DISABLED id=0 ssid="Home" auth_failures=1 duration=10 reason=WRONG_KEY`},
			"wpa_state=SCANNING\n", nil, publicerror.WrongPassword, "Home"},
		{"not found", []string{"<3>CTRL-EVENT-NETWORK-NOT-FOUND"}, "wpa_state=SCANNING\n", nil, publicerror.NetworkNotFound, "Home"},
		{"no lease", nil, "wpa_state=COMPLETED\nssid=Home\n", []netip.Prefix{netip.MustParsePrefix("10.3.141.1/24")}, publicerror.NoDHCPLease, "Home"},
		{"timeout", nil, "wpa_state=SCANNING\n", nil, publicerror.ConnectTimeout, "Home"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(startSupplicant, []runner.Step{stop})...)
			sup := supplicant(t, r, c.status)
			sup.setEvents(c.events...)
			r.addrs = c.addrs
			start := r.clock.Now()
			_, err := r.radio.ClientAttempt(ctx, 45*time.Second, "")
			public := publicerror.As(err)
			if public == nil || public.Code != c.code || public.Message != publicerror.New(c.code, c.ssid).Message {
				t.Fatalf("%v", err)
			}
			r.done()
			if elapsed := r.clock.Now().Sub(start); elapsed > 46*time.Second {
				t.Fatalf("attempt overran its budget: %v", elapsed)
			}
		})
	}
}

func TestClientAttemptPreconditions(t *testing.T) {
	r := newRig(t, "wpa_supplicant-empty.conf")
	if _, err := r.radio.ClientAttempt(ctx, time.Minute, ""); publicerror.As(err).Code != publicerror.NoSavedNetwork {
		t.Fatalf("%v", err)
	}
	r = newRig(t, "")
	writeFile(t, r.l.WpaConf, "network={\n\tssid=\"Home\"\n\tkey_mgmt=NONE\n}\n")
	if _, err := r.radio.ClientAttempt(ctx, time.Minute, ""); publicerror.As(err).Code != publicerror.WpaConfUnusable {
		t.Fatalf("%v", err)
	}
	if len(r.fake.Calls()) != 0 {
		t.Fatal("refused attempt ran commands")
	}
}

func TestAdopt(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", step("systemctl is-active airhatch-wpa-supplicant.service"))
	supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	r.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	client, ok := r.radio.Adopt(ctx)
	if !ok || client.SSID != "Home" {
		t.Fatalf("%+v %v", client, ok)
	}
	r = newRig(t, "wpa_supplicant-one-wpa.conf", failing(step("systemctl is-active airhatch-wpa-supplicant.service")))
	if _, ok := r.radio.Adopt(ctx); ok {
		t.Fatal("adopted an inactive unit")
	}
}

func TestObserveClientMode(t *testing.T) {
	show := step("systemctl show -p ActiveState,Result hostapd.service")
	show.Output = []byte("ActiveState=inactive\nResult=success\n")
	r := newRig(t, "wpa_supplicant-wpa-open.conf", show)
	supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	r.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	snap, err := r.radio.Observe(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	o := snap.Obs
	if o.Saved != 2 || len(snap.Networks) != 2 || !o.ClientUp || o.ClientSSID != "Home" || o.ClientIPv4 != "192.168.1.23/24" || o.HostapdActive || o.StrayReaped {
		t.Fatalf("%+v", o)
	}
}

func TestObserveSupplicantSocketGone(t *testing.T) {
	show := step("systemctl show -p ActiveState,Result hostapd.service")
	show.Output = []byte("ActiveState=inactive\nResult=success\n")
	r := newRig(t, "wpa_supplicant-one-wpa.conf", show, step("systemctl is-active airhatch-wpa-supplicant.service"))
	snap, _ := r.radio.Observe(ctx, false)
	if !snap.Obs.SupplicantSocketGone || snap.Obs.ClientUp {
		t.Fatalf("%+v", snap.Obs)
	}
}

func TestObserveAPMode(t *testing.T) {
	show := step("systemctl show -p ActiveState,Result hostapd.service")
	show.Output = []byte("ActiveState=active\nResult=success\n")
	r := newRig(t, "wpa_supplicant-one-wpa.conf", show, failing(step("systemctl is-active dnsmasq.service")))
	r.hostapd.set("STATUS", "state=ENABLED\nnum_sta[0]=2\n")
	snap, err := r.radio.Observe(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Obs.HostapdActive || !snap.Obs.HostapdEnabled || snap.Obs.Stations != 2 || snap.DnsmasqActive {
		t.Fatalf("%+v", snap)
	}
}

func TestObserveUnreadableHostapdUnit(t *testing.T) {
	show := failing(step("systemctl show -p ActiveState,Result hostapd.service"))
	r := newRig(t, "wpa_supplicant-one-wpa.conf", show)
	supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	snap, err := r.radio.Observe(ctx, false)
	if err == nil || snap.Obs.HostapdActive {
		t.Fatalf("client mode must not act on an unreadable hostapd: %+v %v", snap.Obs, err)
	}
}

func TestReaper(t *testing.T) {
	proc := t.TempDir()
	process := func(pid, comm, cgroup string, args ...string) {
		dir := filepath.Join(proc, pid)
		writeFile(t, filepath.Join(dir, "comm"), comm+"\n")
		writeFile(t, filepath.Join(dir, "cmdline"), strings.Join(args, "\x00")+"\x00")
		writeFile(t, filepath.Join(dir, "cgroup"), "0::"+cgroup+"\n")
	}
	process("100", "wpa_supplicant", "/system.slice/php-fpm.service", "/sbin/wpa_supplicant", "-i", "wlan0", "-c", "/etc/wpa_supplicant/wpa_supplicant.conf", "-B")
	process("101", "wpa_supplicant", "/system.slice/"+paths.SupplicantUnit, "/usr/sbin/wpa_supplicant", "-i", "wlan0", "-D", "nl80211")
	process("102", "wpa_supplicant", "/system.slice/x.service", "wpa_supplicant", "-iwlan1")
	process("103", "hostapd", "/system.slice/hostapd.service", "hostapd", "-i", "wlan0")
	process("104", "wpa_supplicant", "/user.slice/session-1.scope", "wpa_supplicant", "-iwlan0")
	writeFile(t, filepath.Join(proc, "self", "comm"), "x\n")
	k := &kills{}
	reaper := Reaper{Proc: proc, Clock: &stepClock{now: time.Unix(0, 0)}, Kill: func(pid int, sig syscall.Signal) error {
		k.record(pid, sig)
		if pid == 100 && sig == syscall.SIGTERM {
			return os.RemoveAll(filepath.Join(proc, "100"))
		}
		return nil
	}}
	n, err := reaper.Reap(ctx)
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if got := strings.Join(k.sent, ","); got != "terminated 100,terminated 104,killed 104" {
		t.Fatalf("signals %s", got)
	}
}

func TestClientAttemptWhoseCloseFailsStartsNoClient(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), []runner.Step{failing(closeSteps()[0])})...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := r.radio.ClientAttempt(ctx, 45*time.Second, "Home")
	if !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("%v", err)
	}
	if public := publicerror.As(err); public != nil {
		t.Fatalf("a failed close is not a connection error: %v", public)
	}
	r.done()
	if calls := r.fake.Calls(); calls[len(calls)-1].Args[0] != "stop" {
		t.Fatal("the attempt continued after the failed close")
	}
}

func TestClientAttemptAfterAFailedCloseRetriesTheClose(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf",
		concat(openSteps(), []runner.Step{failing(closeSteps()[0])}, closeSteps(), startSupplicant)...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.radio.CloseAP(ctx); !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("%v", err)
	}
	r.watch.checks["systemctl start airhatch-wpa-supplicant.service"] = func() {
		if readFile(t, r.l.DnsmasqConf) != captive.DnsmasqClient || readFile(t, r.l.ModeFile) != "client\n" {
			t.Error("client started while the AP fences were up")
		}
		supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	}
	r.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	if _, err := r.radio.ClientAttempt(ctx, 45*time.Second, ""); err != nil {
		t.Fatal(err)
	}
	r.done()
}

func TestShutdownFinishesAFailedClose(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), []runner.Step{failing(closeSteps()[0])}, closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.radio.CloseAP(ctx); !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("%v", err)
	}
	if err := r.radio.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	assertClosed(t, r)
}

func TestCloseFinishesWhenCancelledAfterHostapdStopped(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// SIGTERM arrives once hostapd is down: the fences must still come off.
	r.watch.checks["systemctl restart dnsmasq.service"] = cancel
	if err := r.radio.CloseAP(opCtx); err != nil {
		t.Fatal(err)
	}
	r.done()
	assertClosed(t, r)
}

func TestFailedNftDeleteIsRetried(t *testing.T) {
	close := closeSteps()
	close[nftCloseStepIndex] = failing(close[nftCloseStepIndex])
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), close, closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.radio.CloseAP(ctx); !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("a left-over captive table must fail close_ap: %v", err)
	}
	if !r.radio.closePending {
		t.Fatal("close not pending after a failed nft delete")
	}
	if err := r.radio.CloseAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	assertClosed(t, r)
}

func TestCleanupReadsTheRuntimeState(t *testing.T) {
	address := newRig(t, "", closeSteps()...)
	address.addrs = []netip.Prefix{netip.MustParsePrefix("10.3.141.1/24")}
	// The flush removes the address.
	address.watch.checks["ip -4 addr flush dev wlan0"] = func() { address.addrs = nil }
	if err := address.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	address.done()
	assertClosed(t, address)

	render := newRig(t, "", closeSteps()...)
	writeFile(t, render.l.DhcpcdConf, "# Rendered by airhatchd render-dhcpcd from /etc/dhcpcd.conf (mode ap). Do not edit.\ndenyinterfaces wlan0\n")
	if err := render.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	render.done()
	assertClosed(t, render)

	client := newRig(t, "", cleanFreshSteps()...)
	client.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	writeFile(t, client.l.DhcpcdConf, "# Rendered by airhatchd render-dhcpcd from /etc/dhcpcd.conf (mode client). Do not edit.\ninterface wlan0\nnohook wpa_supplicant\n")
	if err := client.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	client.done()
}

func TestCleanupReapsStraysFirst(t *testing.T) {
	r := newRig(t, "", cleanFreshSteps()...)
	dir := filepath.Join(r.l.Proc, "100")
	writeFile(t, filepath.Join(dir, "comm"), "wpa_supplicant\n")
	writeFile(t, filepath.Join(dir, "cmdline"), "/sbin/wpa_supplicant\x00-i\x00wlan0\x00-B\x00")
	writeFile(t, filepath.Join(dir, "cgroup"), "0::/system.slice/php-fpm.service\n")
	k := &kills{}
	r.radio.reaper.Kill = func(pid int, sig syscall.Signal) error {
		if len(r.fake.Calls()) != 0 {
			t.Error("reaped after a command ran")
		}
		k.record(pid, sig)
		return os.RemoveAll(dir)
	}
	if err := r.radio.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	if got := strings.Join(k.sent, ","); got != "terminated 100" {
		t.Fatalf("signals %s", got)
	}
}
