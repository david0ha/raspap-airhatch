package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/policy"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/radio"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
	"github.com/david0ha/raspap-airhatch/daemon/internal/servicestart"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

var plan = radio.Plan{Prefix: netip.MustParsePrefix("10.3.141.1/24"), SSID: "RaspAP-1A2B", LighttpdPort: 80}

// fakeRadio records operations and answers from mutable fields.
type fakeRadio struct {
	mu         sync.Mutex
	calls      []string
	networks   []raspapconf.Network
	adopt      *policy.Client
	attempt    func(preferred string) (policy.Client, error)
	openErr    error
	closeErrs  []error
	cleanupErr error
	snap       radio.Snapshot
	observeErr error
	open       bool
	block      chan struct{} // when set, ClientAttempt waits on it or ctx
	// attemptTime is the time an attempt's context leaves until its deadline.
	attemptTime time.Duration
}

func (f *fakeRadio) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeRadio) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeRadio) set(update func(f *fakeRadio)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

func (f *fakeRadio) Cleanup(context.Context) error {
	f.record("cleanup")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cleanupErr
}

func (f *fakeRadio) Adopt(context.Context) (policy.Client, bool) {
	f.record("adopt")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adopt == nil {
		return policy.Client{}, false
	}
	return *f.adopt, true
}

func (f *fakeRadio) Networks() ([]raspapconf.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.networks, nil
}

func (f *fakeRadio) OpenAP(context.Context) error {
	f.record("open_ap")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open = f.openErr == nil
	return f.openErr
}

func (f *fakeRadio) CloseAP(context.Context) error {
	f.record("close_ap")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.closeErrs) > 0 {
		err := f.closeErrs[0]
		f.closeErrs = f.closeErrs[1:]
		if err != nil {
			return err
		}
	}
	f.open = false
	return nil
}

func (f *fakeRadio) ClientAttempt(ctx context.Context, budget time.Duration, preferred string) (policy.Client, error) {
	f.record("client_attempt %s %q", budget, preferred)
	f.mu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		f.attemptTime = time.Until(deadline)
	}
	block, attempt := f.block, f.attempt
	f.open = false
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return policy.Client{}, publicerror.Wrap(publicerror.ConnectTimeout, "", ctx.Err())
		}
	}
	if attempt == nil {
		return policy.Client{}, publicerror.New(publicerror.ConnectTimeout, "")
	}
	return attempt(preferred)
}

func (f *fakeRadio) StopHostapd(context.Context) error { f.record("stop_hostapd"); return nil }
func (f *fakeRadio) RestartSupplicant(context.Context) error {
	f.record("restart_supplicant")
	return nil
}

func (f *fakeRadio) Observe(context.Context, bool) (radio.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap := f.snap
	snap.Networks = f.networks
	snap.Obs.Saved = raspapconf.Saved(f.networks)
	return snap, f.observeErr
}

func (f *fakeRadio) Maintain() { f.record("maintain") }

func (f *fakeRadio) OpenPlan() (radio.Plan, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return plan, f.open
}

func (f *fakeRadio) Shutdown(context.Context) error {
	f.record("shutdown")
	return nil
}

type harness struct {
	t        *testing.T
	sup      *Supervisor
	radio    *fakeRadio
	clock    *clock.Fake
	cfg      config.Config
	selected chan string
	logs     *syncBuffer
	done     chan error
	cancel   context.CancelFunc
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func network(ssid, digest string) raspapconf.Network {
	return raspapconf.Network{SSID: ssid, Digest: sha256.Sum256([]byte(digest))}
}

var home = []raspapconf.Network{network("Home", "d1")}

func healthyAP() radio.Snapshot {
	return radio.Snapshot{Obs: policy.Observation{HostapdActive: true, HostapdEnabled: true}, DnsmasqActive: true}
}

func start(t *testing.T, fr *fakeRadio) *harness {
	t.Helper()
	return startWith(t, fr, config.Default())
}

func startWith(t *testing.T, fr *fakeRadio, cfg config.Config) *harness {
	t.Helper()
	h := &harness{t: t, radio: fr, clock: clock.NewFake(t0), cfg: cfg, selected: make(chan string, 1), logs: &syncBuffer{}, done: make(chan error, 1)}
	creds := filepath.Join(t.TempDir(), "raspap-airhatch.txt")
	h.sup = New(Options{Config: cfg, Radio: fr, Clock: h.clock, Log: slog.New(slog.NewTextHandler(h.logs, nil)),
		Selected: h.selected, CredentialsFile: creds})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- h.sup.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	return h
}

// eventually waits for the published status to satisfy cond.
func (h *harness) eventually(what string, cond func(Status) bool) Status {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := h.sup.Status()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: status %+v; radio calls %v", what, st, h.radio.Calls())
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) idleIn(phase policy.Phase) Status {
	h.t.Helper()
	return h.eventually("idle in "+string(phase), func(s Status) bool { return s.State == phase && s.Operation == nil })
}

// tick advances fake time by d (at least one tick) once the loop waits on
// its ticker, and then waits until the observation it triggers is applied.
func (h *harness) tick(d time.Duration) {
	h.t.Helper()
	before := h.observations()
	for h.clock.Waiters() == 0 {
		time.Sleep(time.Millisecond)
	}
	h.clock.Advance(max(d, h.cfg.Tick))
	deadline := time.Now().Add(3 * time.Second)
	for h.observations() == before {
		if time.Now().After(deadline) {
			h.t.Fatal("tick was not observed")
		}
		time.Sleep(time.Millisecond)
	}
}

// observations asks the loop how many observations it applied.
func (h *harness) observations() int {
	n := 0
	_ = h.sup.do(context.Background(), func() { n = h.sup.applied })
	return n
}

func (h *harness) callsWith(prefix string) []string {
	var out []string
	for _, c := range h.radio.Calls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func TestStartupJoinsASavedNetwork(t *testing.T) {
	fr := &fakeRadio{networks: home, attempt: func(string) (policy.Client, error) {
		return policy.Client{SSID: "Home", IPv4: "192.168.1.23/24"}, nil
	}}
	h := start(t, fr)
	st := h.idleIn(policy.Connected)
	if st.Client == nil || st.Client.SSID != "Home" || st.Mode != "client" || st.SavedNetworks != 1 {
		t.Fatalf("%+v", st)
	}
	if got := strings.Join(fr.Calls(), ","); got != `cleanup,adopt,client_attempt 45s ""` {
		t.Fatal(got)
	}
}

func TestStartupAdoptsAConnectedClient(t *testing.T) {
	fr := &fakeRadio{networks: home, adopt: &policy.Client{SSID: "Home", IPv4: "192.168.1.23/24"}}
	h := start(t, fr)
	h.idleIn(policy.Connected)
	if len(h.callsWith("client_attempt")) != 0 {
		t.Fatal("restarted an adopted client")
	}
}

func TestStartupWithoutNetworksOpensTheAP(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP()}
	h := start(t, fr)
	st := h.idleIn(policy.AP)
	if st.AP == nil || *st.AP != (APStatus{SSID: "RaspAP-1A2B", IPv4: "10.3.141.1/24", Captive: true}) || st.Mode != "ap" {
		t.Fatalf("%+v", st.AP)
	}
	if st.Timers.RetryAt == nil || !st.Timers.RetryAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("retry_at %v", st.Timers.RetryAt)
	}
	fr.set(func(f *fakeRadio) { f.snap.DnsmasqActive, f.snap.Obs.Stations = false, 2 })
	h.tick(0)
	st = h.sup.Status()
	if strings.Join(st.Notices, ",") != "dnsmasq_stopped_outside_airhatch" || st.AP.Stations != 2 {
		t.Fatalf("%+v", st)
	}
	if len(h.callsWith("maintain")) == 0 {
		t.Fatal("AP not maintained on a tick")
	}
	fr.set(func(f *fakeRadio) { f.snap.DnsmasqActive = true })
	h.tick(0)
	if n := h.sup.Status().Notices; len(n) != 0 {
		t.Fatal(n)
	}
}

func TestSavedNetworkHandsOffWithItsSSID(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP()}
	h := start(t, fr)
	h.idleIn(policy.AP)
	fr.set(func(f *fakeRadio) {
		f.networks = []raspapconf.Network{network("Cafe", "d2")}
		f.block = make(chan struct{})
	})
	h.tick(0)
	st := h.idleIn(policy.Handoff)
	if st.Timers.HandoffAt == nil {
		t.Fatal("no handoff_at")
	}
	h.tick(h.cfg.HandoffDelay)
	st = h.eventually("attempt running", func(s Status) bool { return s.Operation != nil })
	if st.Operation.Name != policy.ClientAttempt || st.Timers.AttemptEndsAt == nil || st.State != policy.Connecting {
		t.Fatalf("%+v", st)
	}
	if got := h.callsWith("client_attempt"); len(got) != 1 || got[0] != `client_attempt 45s "Cafe"` {
		t.Fatal(got)
	}
	close(fr.block)
	st = h.idleIn(policy.AP)
	if st.LastError == nil || st.LastError.Code != string(publicerror.ConnectTimeout) {
		t.Fatalf("%+v", st.LastError)
	}
	if !h.sup.AfterFailedHandoff() {
		t.Fatal("portal not switched to the plugin landing after a failed hand-off")
	}
}

func TestStandInSelectIsAHandoffTrigger(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP(), networks: []raspapconf.Network{network("Home", "d1"), network("Cafe", "d2")}}
	h := start(t, fr) // the boot attempt fails, so the AP opens
	h.idleIn(policy.AP)
	h.selected <- "Cafe"
	for len(h.selected) > 0 {
		time.Sleep(time.Millisecond)
	}
	h.tick(0)
	h.idleIn(policy.Handoff)
	h.tick(h.cfg.HandoffDelay)
	h.eventually("attempt with the selected SSID", func(Status) bool {
		got := h.callsWith("client_attempt")
		return len(got) == 2 && got[1] == `client_attempt 45s "Cafe"`
	})
}

func TestUserStoppedHotspotHolds(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP()}
	h := start(t, fr)
	h.idleIn(policy.AP)
	fr.set(func(f *fakeRadio) { f.snap.Obs.HostapdActive, f.snap.Obs.HostapdResult = false, "success" })
	h.tick(0)
	st := h.idleIn(policy.Held)
	if st.Override != policy.Off || st.HoldUntil == nil || st.Mode != "none" || st.AP != nil {
		t.Fatalf("%+v", st)
	}
	if got := h.callsWith("close_ap"); len(got) != 1 {
		t.Fatal(got)
	}
}

// An attempt gets its whole configured budget (up to 10 minutes) plus the
// time to close the AP and stop a failed client.
func TestAttemptDeadlineCoversTheConfiguredBudget(t *testing.T) {
	cfg := config.Default()
	cfg.BootTimeout = 10 * time.Minute
	fr := &fakeRadio{networks: home, attempt: func(string) (policy.Client, error) {
		return policy.Client{SSID: "Home", IPv4: "192.168.1.23/24"}, nil
	}}
	h := startWith(t, fr, cfg)
	h.idleIn(policy.Connected)
	fr.mu.Lock()
	left := fr.attemptTime
	fr.mu.Unlock()
	if left < cfg.BootTimeout || left > cfg.BootTimeout+opDeadline {
		t.Fatalf("attempt deadline %v for a %v budget", left, cfg.BootTimeout)
	}
}

func TestFailedCloseIsRetried(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP(), closeErrs: []error{radio.ErrCloseFailed}}
	h := start(t, fr)
	h.idleIn(policy.AP)
	fr.set(func(f *fakeRadio) { f.snap.Obs.HostapdActive, f.snap.Obs.HostapdResult = false, "success" })
	h.tick(0)
	h.idleIn(policy.Held)
	h.tick(0)
	h.eventually("close retried", func(Status) bool { return len(h.callsWith("close_ap")) == 2 })
}

func TestFailedStartupCleanupClosesBeforeTheBootAttempt(t *testing.T) {
	fr := &fakeRadio{networks: home, cleanupErr: radio.ErrCloseFailed, attempt: func(string) (policy.Client, error) {
		return policy.Client{SSID: "Home", IPv4: "192.168.1.23/24"}, nil
	}}
	h := start(t, fr)
	h.eventually("startup finished", func(st Status) bool { return len(fr.Calls()) >= 2 && st.Operation == nil })
	h.tick(0)
	h.idleIn(policy.Connected)
	if got := strings.Join(fr.Calls(), ","); got != `cleanup,adopt,close_ap,client_attempt 45s ""` {
		t.Fatal(got)
	}
}

func TestFailedHandoffCloseRetriesTheCloseBeforeReopening(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP()}
	h := start(t, fr)
	h.idleIn(policy.AP)
	fr.set(func(f *fakeRadio) {
		f.networks = home
		f.attempt = func(string) (policy.Client, error) { return policy.Client{}, radio.ErrCloseFailed }
	})
	h.tick(0)
	h.idleIn(policy.Handoff)
	h.tick(h.cfg.HandoffDelay)
	h.eventually("attempt finished", func(st Status) bool {
		return len(h.callsWith("client_attempt")) == 1 && st.Operation == nil
	})
	h.tick(0)
	h.idleIn(policy.AP)
	calls := fr.Calls()
	var after []string
	for i, c := range calls {
		if strings.HasPrefix(c, "client_attempt") {
			after = calls[i:]
			break
		}
	}
	if len(after) < 3 || after[1] != "close_ap" || after[2] != "open_ap" {
		t.Fatalf("after the failed attempt: %v", after)
	}
}

func TestAPIRequests(t *testing.T) {
	fr := &fakeRadio{networks: home, adopt: &policy.Client{SSID: "Home", IPv4: "192.168.1.23/24"}, snap: radio.Snapshot{
		Obs: policy.Observation{ClientUp: true, ClientSSID: "Home", ClientIPv4: "192.168.1.23/24"}}}
	h := start(t, fr)
	h.idleIn(policy.Connected)
	ctx := context.Background()

	lines, err := h.sup.ServiceStart(ctx, []string{"--interface", "br0", "--seconds", "1"})
	if err != nil || lines[0] != servicestart.Unsupported {
		t.Fatal(lines, err)
	}
	lines, _ = h.sup.ServiceStart(ctx, []string{"--interface", "wlan0", "--seconds", "1"})
	if lines[0] != servicestart.ClientActive {
		t.Fatal(lines)
	}
	msg, err := h.sup.RetryClient(ctx)
	if err != nil || msg != "Trying the saved Wi-Fi networks again." {
		t.Fatal(msg, err)
	}
	h.tick(0)
	h.eventually("retry ran", func(Status) bool { return len(h.callsWith("client_attempt")) == 1 })
	h.idleIn(policy.AP) // the fake attempt fails, so the AP opens

	msg, err = h.sup.OpenAP(ctx)
	if err != nil || !strings.Contains(msg, "already open") || h.sup.Status().Override != policy.Pin {
		t.Fatal(msg, err)
	}
	msg, err = h.sup.Auto(ctx)
	if err != nil || !strings.HasPrefix(msg, "Back to automatic") || h.sup.Status().Override != policy.Auto {
		t.Fatal(msg, err)
	}
}

func TestRetryWithoutSavedNetworks(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP()}
	h := start(t, fr)
	h.idleIn(policy.AP)
	_, err := h.sup.RetryClient(context.Background())
	if public := publicerror.As(err); public == nil || public.Code != publicerror.NoSavedNetwork {
		t.Fatal(err)
	}
}

func TestObservationErrorsAreLoggedNotPublished(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP(), observeErr: errors.New("hostapd STATUS: timeout")}
	h := start(t, fr)
	h.idleIn(policy.AP)
	h.tick(0)
	h.tick(0)
	if st := h.sup.Status(); st.LastError != nil {
		t.Fatalf("%+v", st.LastError)
	}
	if n := strings.Count(h.logs.String(), "observation incomplete"); n != 1 {
		t.Fatalf("logged %d times within a minute", n)
	}
	h.tick(time.Minute)
	if n := strings.Count(h.logs.String(), "observation incomplete"); n != 2 {
		t.Fatalf("logged %d times after a minute", n)
	}
}

func TestShutdownCancelsTheOperationAndClosesTheAP(t *testing.T) {
	fr := &fakeRadio{networks: home, block: make(chan struct{})}
	h := start(t, fr)
	h.eventually("attempt running", func(s Status) bool { return s.Operation != nil })
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
	if calls := fr.Calls(); calls[len(calls)-1] != "shutdown" {
		t.Fatal(calls)
	}
	if _, err := h.sup.Auto(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatal(err)
	}
}

func TestStatusJSONShape(t *testing.T) {
	fr := &fakeRadio{snap: healthyAP()}
	h := start(t, fr)
	h.idleIn(policy.AP)
	if err := os.WriteFile(h.sup.creds, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = h.sup.Auto(context.Background())
	data, err := json.Marshal(h.sup.Status())
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	keys := []string{"version", "state", "mode", "operation", "override", "hold_until", "saved_networks", "client",
		"ap", "timers", "config", "last_error", "notices", "credentials_file"}
	if len(obj) != len(keys) {
		t.Fatalf("keys %d: %s", len(obj), data)
	}
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			t.Fatalf("missing %s: %s", k, data)
		}
	}
	for k, want := range map[string]string{"operation": "null", "client": "null", "last_error": "null", "hold_until": "null",
		"notices": "[]", "state": `"ap"`, "version": "1", "credentials_file": fmt.Sprintf("%q", h.sup.creds)} {
		if got := string(obj[k]); got != want {
			t.Errorf("%s = %s, want %s", k, got, want)
		}
	}
	var timers map[string]json.RawMessage
	_ = json.Unmarshal(obj["timers"], &timers)
	if string(timers["retry_at"]) != `"2026-10-01T12:05:00Z"` || string(timers["handoff_at"]) != "null" || string(timers["backoff_s"]) != "5" {
		t.Fatalf("timers %s", obj["timers"])
	}
	if string(obj["config"]) != `{"boot_timeout_s":45,"connect_timeout_s":45,"disconnect_grace_s":60,"retry_interval_s":300,"retry_interval_busy_s":900,"handoff_delay_s":5,"off_hold_s":900}` {
		t.Fatalf("config %s", obj["config"])
	}
}
