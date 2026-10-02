package policy

import (
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/servicestart"
)

var (
	t0  = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cfg = config.Default()
	p   = Policy{Cfg: cfg}
)

func at(d time.Duration) time.Time { return t0.Add(d) }

// apUp is a healthy hotspot observation.
func apUp(saved, stations int) Observation {
	return Observation{Saved: saved, Stations: stations, HostapdActive: true, HostapdEnabled: true}
}

func inAP(override Override) State {
	s := p.New()
	s.Phase, s.Override = AP, override
	s.RetryAt, s.BusyRetryAt = at(cfg.RetryInterval), at(cfg.RetryIntervalBusy)
	return s
}

func connected() State {
	s := p.New()
	s.Phase = Connected
	s.Client = Client{SSID: "Home", IPv4: "192.168.1.23/24"}
	return s
}

func want(t *testing.T, name string, s State, a Action, phase Phase, op Op) {
	t.Helper()
	if s.Phase != phase || a.Op != op {
		t.Fatalf("%s: phase %s op %q, want %s %q", name, s.Phase, a.Op, phase, op)
	}
}

func TestNewState(t *testing.T) {
	s := p.New()
	if s.Phase != Init || s.Override != Auto || s.Backoff != cfg.BackoffMin {
		t.Fatalf("%+v", s)
	}
}

func TestStartup(t *testing.T) {
	s, a := p.Complete(p.New(), Result{Op: Startup, Adopted: true, Client: Client{SSID: "Home", IPv4: "192.168.1.5/24"}, Saved: 1}, t0)
	want(t, "adopt", s, a, Connected, None)
	if s.Client.SSID != "Home" {
		t.Fatal("adopted client not recorded")
	}

	s, a = p.Complete(p.New(), Result{Op: Startup, Saved: 2}, t0)
	want(t, "saved", s, a, Connecting, ClientAttempt)
	if a.Budget != cfg.BootTimeout {
		t.Fatalf("boot attempt budget %v", a.Budget)
	}

	s, a = p.Complete(p.New(), Result{Op: Startup}, t0)
	want(t, "none saved", s, a, Init, OpenAP)
}

func TestClientAttemptResults(t *testing.T) {
	s := p.New()
	s.Phase, s.Override, s.HoldUntil, s.Backoff = Connecting, Off, at(time.Hour), time.Minute
	s.LastError = &Failure{Error: publicerror.New(publicerror.ConnectTimeout, ""), At: t0}
	ok, a := p.Complete(s, Result{Op: ClientAttempt, Client: Client{SSID: "Home", IPv4: "10.0.0.2/24"}}, at(time.Second))
	want(t, "success", ok, a, Connected, None)
	if ok.Override != Auto || !ok.HoldUntil.IsZero() || ok.Backoff != cfg.BackoffMin || ok.LastError != nil {
		t.Fatalf("success must clear off, hold, backoff and last error: %+v", ok)
	}

	s.Override, s.HoldUntil = Auto, time.Time{}
	failed, a := p.Complete(s, Result{Op: ClientAttempt, Err: publicerror.New(publicerror.WrongPassword, "Home")}, at(time.Second))
	want(t, "failure opens AP", failed, a, Connecting, OpenAP)
	if failed.LastError == nil || failed.LastError.Code != publicerror.WrongPassword || !failed.LastError.At.Equal(at(time.Second)) {
		t.Fatalf("last error %+v", failed.LastError)
	}

	s.Override, s.HoldUntil = Off, at(10*time.Minute)
	held, a := p.Complete(s, Result{Op: ClientAttempt, Err: publicerror.New(publicerror.ConnectTimeout, "")}, at(time.Second))
	want(t, "failure while off", held, a, Held, None)
}

func TestOpenAPResultsAndBackoff(t *testing.T) {
	s := p.New()
	s.pendingOpen = true
	ok, a := p.Complete(s, Result{Op: OpenAP}, t0)
	want(t, "open", ok, a, AP, None)
	if !ok.RetryAt.Equal(at(cfg.RetryInterval)) || !ok.BusyRetryAt.Equal(at(cfg.RetryIntervalBusy)) || ok.pendingOpen {
		t.Fatalf("%+v", ok)
	}

	s = p.New()
	for i, wantBackoff := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 2 * time.Minute, 2 * time.Minute} {
		var a Action
		s, a = p.Complete(s, Result{Op: OpenAP, Err: publicerror.New(publicerror.NoAPAddress, "")}, t0)
		want(t, "failed open", s, a, APFailed, None)
		if !s.NextOpenAt.Equal(t0.Add(wantBackoff)) {
			t.Fatalf("attempt %d: next open %v, want +%v", i, s.NextOpenAt.Sub(t0), wantBackoff)
		}
	}
	if s.LastError.Code != publicerror.NoAPAddress {
		t.Fatal("refusal not recorded")
	}
	s, _ = p.Complete(s, Result{Op: OpenAP}, t0)
	if s.Backoff != cfg.BackoffMin {
		t.Fatal("successful open must reset the backoff")
	}
}

func TestCloseFailureRetriesCloseFirst(t *testing.T) {
	s := inAP(Auto)
	s, a := p.Complete(s, Result{Op: CloseAP, Err: publicerror.New(publicerror.APStartFailed, ""), CloseFailed: true}, t0)
	if !s.NeedClose || a.Op != None {
		t.Fatal("close failure not recorded")
	}
	s, a = p.Tick(s, apUp(1, 0), at(time.Hour))
	if a.Op != CloseAP {
		t.Fatalf("next tick must retry close_ap, got %q", a.Op)
	}
	s, _ = p.Complete(s, Result{Op: CloseAP}, at(time.Hour))
	if s.NeedClose {
		t.Fatal("successful close kept NeedClose")
	}
	s = p.New()
	s.Phase = Connecting
	s, a = p.Complete(s, Result{Op: ClientAttempt, Err: publicerror.New(publicerror.APStartFailed, ""), CloseFailed: true}, t0)
	if !s.NeedClose {
		t.Fatal("attempt whose close_ap failed must retry close")
	}
	if a.Op != None {
		t.Fatalf("attempt whose close_ap failed started %q before the close succeeded", a.Op)
	}
	s, a = p.Tick(s, Observation{Saved: 1}, at(time.Second))
	if a.Op != CloseAP {
		t.Fatalf("next tick must retry close_ap, got %q", a.Op)
	}
	s, a = p.Complete(s, Result{Op: CloseAP}, at(2*time.Second))
	want(t, "the deferred open after the close", s, a, Connecting, OpenAP)
	if s.NeedClose {
		t.Fatal("successful close kept NeedClose")
	}
}

func TestStartupWithFailedCleanupClosesBeforeTheAttempt(t *testing.T) {
	s, a := p.Complete(p.New(), Result{Op: Startup, Err: publicerror.New(publicerror.APStartFailed, ""), CloseFailed: true, Saved: 1}, t0)
	if !s.NeedClose || a.Op != None {
		t.Fatalf("startup whose cleanup failed: NeedClose %v op %q", s.NeedClose, a.Op)
	}
	s, a = p.Tick(s, Observation{Saved: 1}, at(time.Second))
	if a.Op != CloseAP {
		t.Fatalf("next tick must retry close_ap, got %q", a.Op)
	}
	s, a = p.Complete(s, Result{Op: CloseAP, Err: publicerror.New(publicerror.APStartFailed, ""), CloseFailed: true}, at(2*time.Second))
	if !s.NeedClose || a.Op != None {
		t.Fatalf("a second failed close: NeedClose %v op %q", s.NeedClose, a.Op)
	}
	s, a = p.Complete(s, Result{Op: CloseAP}, at(3*time.Second))
	want(t, "the deferred boot attempt", s, a, Connecting, ClientAttempt)
	if a.Budget != cfg.BootTimeout {
		t.Fatalf("boot attempt budget %v", a.Budget)
	}
	s, a = p.Complete(s, Result{Op: CloseAP}, at(4*time.Second))
	if a.Op != None {
		t.Fatalf("the deferred action ran twice: %q", a.Op)
	}
}

func TestDeferredOpenHonoursAStopDuringTheClose(t *testing.T) {
	s := p.New()
	s.Phase = Connecting
	s, _ = p.Complete(s, Result{Op: ClientAttempt, Err: publicerror.New(publicerror.ConnectTimeout, ""), CloseFailed: true}, t0)
	s, _ = p.ServiceStart(s, servicestart.Request{Interface: "wlan0", Stop: true}, at(time.Second))
	s, a := p.Complete(s, Result{Op: CloseAP}, at(2*time.Second))
	want(t, "stopped while the close was retried", s, a, Held, None)
}

func TestConnectedAndLost(t *testing.T) {
	s, a := p.Tick(connected(), Observation{Saved: 1, ClientUp: true, ClientSSID: "Home", ClientIPv4: "192.168.1.23/24"}, t0)
	want(t, "steady", s, a, Connected, None)

	s, a = p.Tick(connected(), Observation{Saved: 1}, t0)
	want(t, "drop", s, a, Lost, None)
	if !s.DownSince.Equal(t0) {
		t.Fatal("down_since not set")
	}

	back, a := p.Tick(s, Observation{Saved: 1, ClientUp: true}, at(30*time.Second))
	want(t, "back", back, a, Connected, None)

	still, a := p.Tick(s, Observation{Saved: 1}, at(cfg.DisconnectGrace-time.Second))
	want(t, "within grace", still, a, Lost, None)

	gone, a := p.Tick(s, Observation{Saved: 1}, at(cfg.DisconnectGrace))
	want(t, "grace over", gone, a, Lost, OpenAP)

	s.Override, s.HoldUntil = Off, at(time.Hour)
	off, a := p.Tick(s, Observation{Saved: 1}, at(cfg.DisconnectGrace))
	want(t, "grace over while off", off, a, Lost, None)
}

func TestClientMaintenance(t *testing.T) {
	s, a := p.Tick(connected(), Observation{Saved: 1, ClientUp: true, HostapdActive: true}, t0)
	if a.Op != StopHostapd || s.LastError == nil || s.LastError.Code != publicerror.APServiceOutsideDaemon {
		t.Fatalf("hostapd in client mode: %q %+v", a.Op, s.LastError)
	}
	_, a = p.Tick(connected(), Observation{Saved: 1, SupplicantSocketGone: true}, t0)
	if a.Op != RestartSupplicant {
		t.Fatalf("socket gone: %q", a.Op)
	}
}

func TestAPRetries(t *testing.T) {
	s, a := p.Tick(inAP(Auto), apUp(1, 0), at(cfg.RetryInterval-time.Second))
	want(t, "before retry", s, a, AP, None)

	s, a = p.Tick(inAP(Auto), apUp(1, 0), at(cfg.RetryInterval))
	want(t, "retry", s, a, Connecting, ClientAttempt)
	if a.Budget != cfg.ConnectTimeout || a.Preferred != "" {
		t.Fatalf("%+v", a)
	}

	s, a = p.Tick(inAP(Auto), apUp(1, 2), at(cfg.RetryInterval))
	want(t, "5 min retry never fires with stations", s, a, AP, None)

	s, a = p.Tick(inAP(Auto), apUp(1, 2), at(cfg.RetryIntervalBusy))
	want(t, "busy retry fires with stations", s, a, Connecting, ClientAttempt)

	s, a = p.Tick(inAP(Auto), apUp(0, 0), at(cfg.RetryIntervalBusy))
	want(t, "nothing saved", s, a, AP, None)

	s, a = p.Tick(inAP(Pin), apUp(1, 0), at(cfg.RetryIntervalBusy))
	want(t, "pinned AP suppresses retries", s, a, AP, None)
}

func TestHandoffTriggers(t *testing.T) {
	triggers := map[string]Observation{
		"file change": {Saved: 2, Changed: true, ChangedSSID: "New"},
		"connect":     {Saved: 2, Selected: true, SelectedSSID: "Picked"},
		"stray":       {Saved: 2, StrayReaped: true},
	}
	for name, o := range triggers {
		o.HostapdActive, o.HostapdEnabled, o.Stations = true, true, 3
		s, a := p.Tick(inAP(Pin), o, at(time.Second))
		want(t, name, s, a, Handoff, None)
		if s.Override != Auto {
			t.Errorf("%s: a hand-off trigger must clear override=ap", name)
		}
		if !s.HandoffAt.Equal(at(time.Second + cfg.HandoffDelay)) {
			t.Errorf("%s: handoff_at %v", name, s.HandoffAt)
		}
		s, a = p.Tick(s, apUp(2, 3), at(time.Second+cfg.HandoffDelay))
		want(t, name+" fires with stations", s, a, Connecting, ClientAttempt)
		if a.Budget != cfg.ConnectTimeout {
			t.Errorf("%s: budget %v", name, a.Budget)
		}
	}
	s, _ := p.Tick(inAP(Auto), Observation{Saved: 2, Selected: true, SelectedSSID: "Picked", HostapdActive: true}, t0)
	_, a := p.Tick(s, apUp(2, 0), at(cfg.HandoffDelay))
	if a.Preferred != "Picked" {
		t.Fatalf("preferred %q", a.Preferred)
	}
}

func TestHandoffRearmsAndNeedsSavedNetwork(t *testing.T) {
	s, _ := p.Tick(inAP(Auto), Observation{Saved: 1, Changed: true, ChangedSSID: "A", HostapdActive: true}, t0)
	s, a := p.Tick(s, Observation{Saved: 2, Changed: true, ChangedSSID: "B", HostapdActive: true}, at(4*time.Second))
	want(t, "re-arm", s, a, Handoff, None)
	if !s.HandoffAt.Equal(at(4*time.Second + cfg.HandoffDelay)) {
		t.Fatalf("handoff_at %v", s.HandoffAt)
	}
	_, a = p.Tick(s, apUp(2, 0), at(4*time.Second+cfg.HandoffDelay))
	if a.Preferred != "B" {
		t.Fatalf("preferred %q", a.Preferred)
	}

	s, a = p.Tick(inAP(Auto), Observation{Saved: 0, StrayReaped: true, HostapdActive: true, HostapdEnabled: true}, t0)
	want(t, "stray without networks", s, a, AP, None)
	s, a = p.Tick(inAP(Auto), Observation{Saved: 0, StrayReaped: true, HostapdActive: true, HostapdEnabled: false}, t0)
	want(t, "stray took the interface", s, a, AP, Reapply)
}

func TestFailedHandoffSwitchesLanding(t *testing.T) {
	s, _ := p.Tick(inAP(Auto), Observation{Saved: 1, Changed: true, HostapdActive: true}, t0)
	s, _ = p.Tick(s, apUp(1, 0), at(cfg.HandoffDelay))
	s, a := p.Complete(s, Result{Op: ClientAttempt, Err: publicerror.New(publicerror.WrongPassword, "Home")}, at(time.Minute))
	if !s.HandoffFailed || a.Op != OpenAP {
		t.Fatal("failed hand-off not recorded")
	}
	s, _ = p.Complete(s, Result{Op: OpenAP}, at(time.Minute))
	if !s.HandoffFailed {
		t.Fatal("the reopened AP must land on the plugin page")
	}
	s, _ = p.Tick(s, apUp(1, 0), s.RetryAt)
	if s.HandoffFailed {
		t.Fatal("a periodic attempt clears the failed hand-off landing")
	}
}

func TestHotspotStoppedOutsideDaemon(t *testing.T) {
	stopped := Observation{Saved: 1, HostapdActive: false, HostapdResult: "success"}
	s, a := p.Tick(inAP(Auto), stopped, t0)
	want(t, "user stop with networks", s, a, Connecting, ClientAttempt)
	if s.Override != Off || !s.HoldUntil.Equal(at(cfg.OffHold)) {
		t.Fatalf("hold %+v", s)
	}

	stopped.Saved = 0
	s, a = p.Tick(inAP(Auto), stopped, t0)
	want(t, "user stop without networks", s, a, Held, CloseAP)

	stopped.HostapdStoppedByDaemon = true
	s, a = p.Tick(inAP(Auto), stopped, t0)
	if s.Override == Off {
		t.Fatal("a stop by airhatchd is not a user stop")
	}

	crashed := Observation{Saved: 1, HostapdActive: false, HostapdResult: "exit-code"}
	s, a = p.Tick(inAP(Auto), crashed, t0)
	want(t, "hostapd failed", s, a, APFailed, CloseAP)
	if !s.NextOpenAt.Equal(at(cfg.BackoffMin)) || s.LastError.Code != publicerror.APStartFailed {
		t.Fatalf("%+v", s)
	}
}

func TestIdleStates(t *testing.T) {
	failed := p.New()
	failed.Phase, failed.NextOpenAt = APFailed, at(time.Minute)
	s, a := p.Tick(failed, Observation{Saved: 1}, at(30*time.Second))
	want(t, "backoff pending", s, a, APFailed, None)
	s, a = p.Tick(failed, Observation{Saved: 1}, at(time.Minute))
	want(t, "backoff over, saved", s, a, Connecting, ClientAttempt)
	s, a = p.Tick(failed, Observation{}, at(time.Minute))
	want(t, "backoff over, none saved", s, a, APFailed, OpenAP)
	s, a = p.Tick(failed, Observation{Saved: 1, Changed: true}, at(time.Second))
	want(t, "network saved while failed", s, a, Connecting, ClientAttempt)

	held := p.New()
	held.Phase, held.Override, held.HoldUntil = Held, Off, at(cfg.OffHold)
	s, a = p.Tick(held, Observation{Saved: 1}, at(time.Minute))
	want(t, "holding", s, a, Held, None)
	s, a = p.Tick(held, Observation{Saved: 1, Changed: true}, at(time.Minute))
	want(t, "network saved while held", s, a, Connecting, ClientAttempt)
	s, a = p.Tick(held, Observation{Saved: 1}, at(cfg.OffHold))
	want(t, "hold over, saved", s, a, Connecting, ClientAttempt)
	if s.Override != Auto {
		t.Fatal("expired hold must return to auto")
	}
	s, a = p.Tick(held, Observation{}, at(cfg.OffHold))
	want(t, "hold over, none saved", s, a, Held, OpenAP)
	auto, _ := p.Auto(held)
	s, a = p.Tick(auto, Observation{}, at(time.Second))
	want(t, "back to automatic", s, a, Held, OpenAP)
}

func TestAPIOpenAP(t *testing.T) {
	s, msg := p.OpenAP(connected(), t0)
	if s.Override != Pin || msg == "" {
		t.Fatal(msg)
	}
	_, a := p.Tick(s, Observation{Saved: 1, ClientUp: true}, at(cfg.HandoffDelay-time.Second))
	if a.Op != None {
		t.Fatal("open_ap from a client state must wait handoff_delay")
	}
	_, a = p.Tick(s, Observation{Saved: 1, ClientUp: true}, at(cfg.HandoffDelay))
	if a.Op != OpenAP {
		t.Fatalf("explicit open_ap from connected: %q", a.Op)
	}

	held := p.New()
	held.Phase, held.Override, held.HoldUntil = Held, Off, at(time.Hour)
	s, _ = p.OpenAP(held, t0)
	_, a = p.Tick(s, Observation{Saved: 1}, t0)
	if a.Op != OpenAP || s.Override != Pin {
		t.Fatalf("open_ap from held: %q", a.Op)
	}

	ho, _ := p.Tick(inAP(Auto), Observation{Saved: 1, Changed: true, HostapdActive: true}, t0)
	s, _ = p.OpenAP(ho, t0)
	if s.Phase != AP || !s.HandoffAt.IsZero() {
		t.Fatal("open_ap during a hand-off keeps the AP")
	}
}

func TestAPIRetryClient(t *testing.T) {
	if _, _, err := p.RetryClient(inAP(Auto), 0, t0); err == nil || err.Code != publicerror.NoSavedNetwork {
		t.Fatalf("%v", err)
	}
	s, msg, err := p.RetryClient(inAP(Pin), 1, t0)
	if err != nil || msg == "" {
		t.Fatal(err)
	}
	s, a := p.Tick(s, apUp(1, 2), t0)
	want(t, "retry on AP is a hand-off", s, a, Handoff, None)
	if s.Override != Auto {
		t.Fatal("retry_client is a hand-off trigger")
	}

	s, _, _ = p.RetryClient(connected(), 1, t0)
	_, a = p.Tick(s, Observation{Saved: 1, ClientUp: true}, t0)
	if a.Op != ClientAttempt || a.Budget != cfg.ConnectTimeout {
		t.Fatalf("retry in client state: %+v", a)
	}

	failed := p.New()
	failed.Phase, failed.NextOpenAt = APFailed, at(time.Hour)
	s, _, _ = p.RetryClient(failed, 1, t0)
	_, a = p.Tick(s, Observation{Saved: 1}, t0)
	if a.Op != ClientAttempt {
		t.Fatalf("retry in ap_failed: %q", a.Op)
	}

	busy := p.New()
	busy.Phase = Connecting
	s, msg, err = p.RetryClient(busy, 1, t0)
	if err != nil || s.pendingRetry || msg == "" {
		t.Fatal("retry during an attempt adds nothing")
	}
}

func TestServiceStart(t *testing.T) {
	req := servicestart.Request{Interface: "wlan0"}
	lost := connected()
	lost.Phase, lost.Override, lost.DownSince = Lost, Off, t0
	cases := []struct {
		state State
		line  string
	}{
		{connected(), servicestart.ClientActive},
		{lost, servicestart.ClientActive},
		{inAP(Auto), servicestart.Restarting},
		{func() State { s := inAP(Auto); s.Phase = Handoff; return s }(), servicestart.HandoffBusy},
		{p.New(), servicestart.Opening},
	}
	for _, c := range cases {
		if _, line := p.ServiceStart(c.state, req, t0); line != c.line {
			t.Errorf("%s: %q", c.state.Phase, line)
		}
	}
	s, _ := p.ServiceStart(lost, req, t0)
	if s.Override != Auto {
		t.Fatal("start clears the override in client states")
	}
	_, a := p.Tick(s, Observation{Saved: 1}, t0)
	if a.Op == OpenAP {
		t.Fatal("start in a client state never opens the AP")
	}
	s, _ = p.ServiceStart(inAP(Auto), req, t0)
	if _, a = p.Tick(s, apUp(1, 1), t0); a.Op != Reapply {
		t.Fatalf("start in ap: %q", a.Op)
	}

	held := p.New()
	held.Phase, held.Override, held.HoldUntil = Held, Off, at(time.Hour)
	s, _ = p.ServiceStart(held, req, t0)
	if _, a = p.Tick(s, Observation{Saved: 1}, t0); a.Op != OpenAP || s.Override != Auto {
		t.Fatalf("start in held: %q", a.Op)
	}

	if _, line := p.ServiceStart(inAP(Auto), servicestart.Request{Interface: "br0"}, t0); line != servicestart.Unsupported {
		t.Fatal(line)
	}
}

func TestServiceStartStop(t *testing.T) {
	stop := servicestart.Request{Interface: "wlan0", Stop: true}
	s, line := p.ServiceStart(inAP(Auto), stop, t0)
	if line != "airhatch: hotspot off; it stays off for 15m unless you start it again." {
		t.Fatal(line)
	}
	s, a := p.Tick(s, apUp(1, 1), t0)
	want(t, "stop on AP", s, a, Connecting, ClientAttempt)
	if s.Override != Off {
		t.Fatal("override")
	}
	c, _ := p.ServiceStart(connected(), stop, t0)
	if _, a = p.Tick(c, Observation{Saved: 1, ClientUp: true}, t0); a.Op != None || c.Override != Off {
		t.Fatalf("stop in client state: %q", a.Op)
	}
}

func TestStartDuringInit(t *testing.T) {
	s, line := p.ServiceStart(p.New(), servicestart.Request{Interface: "wlan0"}, t0)
	if line != servicestart.Opening {
		t.Fatal(line)
	}
	_, a := p.Complete(s, Result{Op: Startup, Saved: 1}, t0)
	if a.Op != OpenAP {
		t.Fatalf("start during init opens the AP after cleanup: %q", a.Op)
	}
	adopted, _ := p.Complete(s, Result{Op: Startup, Saved: 1, Adopted: true}, t0)
	if adopted.pendingOpen {
		t.Fatal("an adopted client is never cut by a pending start")
	}
}

func TestAPNeverOpensFromConnectedWithoutAPI(t *testing.T) {
	s := connected()
	s.Override, s.HoldUntil = Off, at(time.Minute)
	for _, now := range []time.Time{t0, at(time.Minute), at(time.Hour)} {
		var a Action
		s, a = p.Tick(s, Observation{Saved: 1, ClientUp: true}, now)
		if a.Op == OpenAP || a.Op == Reapply {
			t.Fatalf("opened from connected at %v", now)
		}
	}
}

func TestStatusMode(t *testing.T) {
	for phase, mode := range map[Phase]string{Init: "none", Connecting: "client", Connected: "client", Lost: "client",
		AP: "ap", Handoff: "ap", APFailed: "none", Held: "none"} {
		if got := phase.Mode(); got != mode {
			t.Errorf("%s: %s", phase, got)
		}
	}
}
