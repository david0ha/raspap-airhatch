package radio

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

// Every safety step of close_ap is mandatory: a failure keeps the close
// pending, and nothing starts or adopts a client until a close succeeds.

func assertPending(t *testing.T, r *rig, err error) {
	t.Helper()
	if !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("close reported %v, want ErrCloseFailed", err)
	}
	if !r.radio.closePending {
		t.Fatal("close not pending after a failed safety step")
	}
}

func TestCloseKeepsPendingWhenDnsmasqRestartFails(t *testing.T) {
	close := closeSteps()
	close[restartDnsmasqStep] = failing(close[restartDnsmasqStep])
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), close)...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	assertPending(t, r, r.radio.CloseAP(ctx))
}

func TestCloseKeepsPendingWhenTheFenceCannotBeWritten(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	// A directory in its place makes the atomic rename fail.
	if err := os.Remove(r.l.DnsmasqConf); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.l.DnsmasqConf, "x"), "")
	assertPending(t, r, r.radio.CloseAP(ctx))
	r.done()
}

func TestCloseKeepsPendingWhileTheAPAddressRemains(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	r.addrs = []netip.Prefix{netip.MustParsePrefix("10.3.141.1/24")}
	assertPending(t, r, r.radio.CloseAP(ctx))
}

func TestClientAttemptAfterAFailedDnsmasqRestartStartsNoClient(t *testing.T) {
	close := closeSteps()
	close[restartDnsmasqStep] = failing(close[restartDnsmasqStep])
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), close)...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	opened := len(r.fake.Calls())
	if _, err := r.radio.ClientAttempt(ctx, 45*time.Second, "Home"); !errors.Is(err, ErrCloseFailed) {
		t.Fatalf("%v", err)
	}
	for _, c := range r.fake.Calls()[opened:] {
		if c.Name == "systemctl" && c.Args[1] == "airhatch-wpa-supplicant.service" {
			t.Fatalf("supplicant touched during a failed close: %v", c.Args)
		}
	}
}

func TestCleanupFailsWhenTheNftTableCannotBeDeleted(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(cleanFreshSteps()[:len(cleanFreshSteps())-1], []runner.Step{failing(nftCloseStep())})...)
	assertPending(t, r, r.radio.Cleanup(ctx))
	r.done()
}

func TestNoAdoptionWhileACloseIsPending(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(cleanFreshSteps()[:len(cleanFreshSteps())-1],
		[]runner.Step{failing(nftCloseStep()), step("systemctl is-active airhatch-wpa-supplicant.service")})...)
	_ = r.radio.Cleanup(ctx)
	supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	r.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	if _, ok := r.radio.Adopt(ctx); ok {
		t.Fatal("adopted a client while the close is pending")
	}
}

// The stand-in keeps the supplicant's socket path until the client starts,
// so RaspAP never sees it missing while the AP side is torn down.
func TestStandInStaysBoundUntilTheClientStarts(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps(), startSupplicant)...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	var problems []string
	for _, s := range closeSteps() {
		cmd := s.Name + " " + strings.Join(s.Args, " ")
		r.watch.checks[cmd] = func() {
			if !exists(r.l.CtrlStandIn) {
				problems = append(problems, "stand-in gone at: "+cmd)
			}
		}
	}
	if err := r.radio.CloseAP(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(r.l.CtrlStandIn) {
		t.Fatal("stand-in released by the close")
	}
	r.watch.checks["systemctl start airhatch-wpa-supplicant.service"] = func() {
		if exists(r.l.CtrlStandIn) {
			problems = append(problems, "stand-in still bound when the supplicant starts")
		}
		supplicant(t, r, "wpa_state=COMPLETED\nssid=Home\n")
	}
	r.addrs = []netip.Prefix{netip.MustParsePrefix("192.168.1.23/24")}
	if _, err := r.radio.ClientAttempt(ctx, 45*time.Second, ""); err != nil {
		t.Fatal(err)
	}
	r.done()
	for _, p := range problems {
		t.Error(p)
	}
}

func TestShutdownReleasesTheStandIn(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), closeSteps())...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.radio.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	r.done()
	if exists(r.l.CtrlStandIn) {
		t.Fatal("stand-in left behind at shutdown")
	}
}

func brokenReaper(r *rig) {
	r.radio.reaper.Proc = filepath.Join(r.l.Proc, "missing")
}

func TestReapFailureStartsNoClient(t *testing.T) {
	r := newRig(t, "wpa_supplicant-one-wpa.conf")
	brokenReaper(r)
	if _, err := r.radio.ClientAttempt(ctx, 45*time.Second, ""); err == nil {
		t.Fatal("client attempt ignored a failed reap")
	}
	if n := len(r.fake.Calls()); n != 0 {
		t.Fatalf("client attempt ran %d commands after a failed reap", n)
	}
}

func TestReapFailureFailsTheOpen(t *testing.T) {
	open := openSteps()
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(open[:2], closeSteps())...)
	brokenReaper(r)
	err := r.radio.OpenAP(ctx)
	if public := publicerror.As(err); public == nil || public.Code != publicerror.APStartFailed {
		t.Fatalf("%v", err)
	}
	// Right after the client stop (open[1]) comes the rollback's close.
	if calls := r.fake.Calls(); len(calls) < 3 || calls[2].Name != closeSteps()[0].Name || !slices.Equal(calls[2].Args, closeSteps()[0].Args) {
		t.Fatalf("open continued after a failed reap: %v", calls)
	}
	r.done()
}

func TestReapFailureFailsTheCleanup(t *testing.T) {
	r := newRig(t, "")
	brokenReaper(r)
	assertPending(t, r, r.radio.Cleanup(ctx))
	if n := len(r.fake.Calls()); n != 0 {
		t.Fatalf("cleanup ran %d commands after a failed reap", n)
	}
}

// Credentials: the AP opens only when RaspAP's login is not the published
// default as www-data reads it.

func TestPreflightRefusesUnusableAdminLogin(t *testing.T) {
	cases := map[string]func(r *rig){
		"unreadable by www-data": func(r *rig) {
			if err := os.Chmod(r.l.RaspAPAuth, 0600); err != nil {
				t.Fatal(err)
			}
			r.radio.webUID = os.Getuid() + 1
		},
		"directory closed to www-data": func(r *rig) {
			if err := os.Chmod(filepath.Dir(r.l.RaspAPAuth), 0700); err != nil {
				t.Fatal(err)
			}
			r.radio.webUID, r.radio.webGID = os.Getuid()+1, os.Getgid()+1
		},
		"empty":      func(r *rig) { writeFile(t, r.l.RaspAPAuth, "") },
		"no hash":    func(r *rig) { writeFile(t, r.l.RaspAPAuth, "admin\n") },
		"not a file": func(r *rig) { os.Remove(r.l.RaspAPAuth); writeFile(t, filepath.Join(r.l.RaspAPAuth, "x"), "") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var steps []runner.Step
			if name == "default secret" {
				steps = append(steps, authStep(upstreamDefaultHash, "default"))
			}
			r := newRig(t, "wpa_supplicant-one-wpa.conf", steps...)
			mutate(r)
			err := r.radio.OpenAP(ctx)
			if public := publicerror.As(err); public == nil || public.Code != publicerror.DefaultAdminLogin {
				t.Fatalf("%v", err)
			}
			r.done()
		})
	}
}

func TestPreflightRefusesPlaceholderPassphrases(t *testing.T) {
	for _, psk := range []string{"ChangeMe", "changeme", "", "secret", "password", "12345678"} {
		t.Run(psk, func(t *testing.T) {
			r := newRig(t, "wpa_supplicant-one-wpa.conf")
			replaceIn(t, r.l.HostapdConf, "wpa_passphrase=Qx7mKp2vRt9w", "wpa_passphrase="+psk)
			err := r.radio.OpenAP(ctx)
			if public := publicerror.As(err); public == nil || public.Code != publicerror.DefaultAPPassphrase {
				t.Fatalf("%v", err)
			}
			if len(r.fake.Calls()) != 0 {
				t.Fatal("a refused open ran commands")
			}
		})
	}
}

func TestCloseKeepsPendingWhenDnsmasqStaysInactive(t *testing.T) {
	close := closeSteps()
	inactive := failing(close[restartDnsmasqStep+1])
	steps := concat(close[:restartDnsmasqStep+1], []runner.Step{inactive, inactive, inactive, inactive, inactive}, close[restartDnsmasqStep+2:])
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), steps)...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	assertPending(t, r, r.radio.CloseAP(ctx))
	r.done()
}

func TestCloseKeepsPendingWhileAHostapdInstanceRuns(t *testing.T) {
	close := closeSteps()
	close[listHostapdStep].Output = []byte("hostapd@wlan0.service loaded active running Advanced IEEE 802.11 AP\n")
	r := newRig(t, "wpa_supplicant-one-wpa.conf", concat(openSteps(), close[:listHostapdStep+1])...)
	if err := r.radio.OpenAP(ctx); err != nil {
		t.Fatal(err)
	}
	apContent := readFile(t, r.l.DnsmasqConf)
	assertPending(t, r, r.radio.CloseAP(ctx))
	r.done()
	if readFile(t, r.l.DnsmasqConf) != apContent {
		t.Fatal("fences dropped while a hostapd instance runs")
	}
}

// Each trace of an AP on its own makes the stale cleanup run a full close.
func TestCleanupDetectsEachLeftoverOnItsOwn(t *testing.T) {
	runningInstance := listHostapd()
	runningInstance.Output = []byte("hostapd@wlan0.service loaded active running Advanced IEEE 802.11 AP\n")
	cases := map[string]struct {
		setup func(r *rig)
		steps []runner.Step
	}{
		"AP dnsmasq include": {func(r *rig) {
			writeFile(t, r.l.DnsmasqConf, captive.DnsmasqAP(netip.MustParseAddr("10.3.141.1"), true))
		}, closeSteps()},
		"hostapd template instance": {func(*rig) {}, concat([]runner.Step{runningInstance}, closeSteps())},
		"unreadable unit list":      {func(*rig) {}, concat([]runner.Step{failing(listHostapd())}, closeSteps())},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "", c.steps...)
			c.setup(r)
			if err := r.radio.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			r.done()
			assertClosed(t, r)
		})
	}
}

func TestPreflightRefusesWhenTheLoginCannotBeChecked(t *testing.T) {
	for name, s := range map[string]runner.Step{
		"php fails":       {Name: "php", Err: errors.New("php: not found")},
		"unexpected text": {Output: []byte("Warning: something\n")},
	} {
		t.Run(name, func(t *testing.T) {
			step := authStep(testAuthHash, "")
			step.Output, step.Err = s.Output, s.Err
			r := newRig(t, "wpa_supplicant-one-wpa.conf", step)
			err := r.radio.OpenAP(ctx)
			if public := publicerror.As(err); public == nil || public.Code != publicerror.DefaultAdminLogin {
				t.Fatalf("%v", err)
			}
			r.done()
		})
	}
}

// The check is RaspAP's own password_verify, run by php-cli as on the image.
func TestDefaultLoginScriptMatchesRaspAP(t *testing.T) {
	php := runner.NewExec(time.Minute, 1<<16)
	for hash, want := range map[string]bool{upstreamDefaultHash: true, testAuthHash: false} {
		got, err := isDefaultLogin(ctx, php, hash)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v, want %v", hash, got, err, want)
		}
	}
}
