// Package supervisor runs airhatchd's loop: it observes wlan0 every tick,
// feeds the policy, runs one radio operation at a time and answers the API
// (docs/DESIGN.md §3, §5.4-5.5).
package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/policy"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/radio"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
)

const (
	// opDeadline bounds an operation's own steps; a client attempt gets its
	// budget on top, so a configured timeout of up to 10 minutes holds.
	opDeadline      = 2 * time.Minute
	shutdownTimeout = 30 * time.Second
	errorLogPeriod  = time.Minute
)

// Radio is what the supervisor needs from wlan0 (implemented by radio.Radio).
type Radio interface {
	Cleanup(ctx context.Context) error
	Adopt(ctx context.Context) (policy.Client, bool)
	Networks() ([]raspapconf.Network, error)
	OpenAP(ctx context.Context) error
	CloseAP(ctx context.Context) error
	ClientAttempt(ctx context.Context, budget time.Duration, preferred string) (policy.Client, error)
	StopHostapd(ctx context.Context) error
	RestartSupplicant(ctx context.Context) error
	Observe(ctx context.Context, apMode bool) (radio.Snapshot, error)
	Maintain()
	OpenPlan() (radio.Plan, bool)
	Shutdown(ctx context.Context) error
}

// Options are the supervisor's dependencies.
type Options struct {
	Config config.Config
	Radio  Radio
	Clock  clock.Clock
	Log    *slog.Logger
	// Selected carries SSIDs chosen with SELECT_NETWORK on the stand-in.
	Selected <-chan string
	// CredentialsFile is reported in the status while it exists.
	CredentialsFile string
}

// Supervisor owns the policy state. Only its loop goroutine changes it; the
// API reaches the loop through requests.
type Supervisor struct {
	cfg      config.Config
	pol      policy.Policy
	radio    Radio
	clock    clock.Clock
	log      *slog.Logger
	selected <-chan string
	creds    string

	requests           chan func()
	stopped            chan struct{}
	status             atomic.Pointer[Status]
	afterFailedHandoff atomic.Bool

	// Loop-owned.
	state       policy.State
	op          *running
	observing   bool
	networks    []raspapconf.Network
	saved       int
	stations    int
	notices     []string
	pickedSSID  string
	picked      bool
	lastErrLog  time.Time
	applied     int // observations applied, for tests to step the loop
	apPlan      radio.Plan
	apOpen      bool
	observeDone chan observed
	opDone      chan finished
}

type running struct {
	name    policy.Op
	started time.Time
	budget  time.Duration
	cancel  context.CancelFunc
}

// finished is an operation's result plus what the loop may not read from
// the radio while another operation runs.
type finished struct {
	result   policy.Result
	networks []raspapconf.Network
	plan     radio.Plan
	apOpen   bool
}

type observed struct {
	snap radio.Snapshot
	err  error
}

// New builds a supervisor in state init.
func New(o Options) *Supervisor {
	s := &Supervisor{
		cfg: o.Config, pol: policy.Policy{Cfg: o.Config}, radio: o.Radio, clock: o.Clock, log: o.Log,
		selected: o.Selected, creds: o.CredentialsFile,
		requests:    make(chan func()),
		stopped:     make(chan struct{}),
		observeDone: make(chan observed, 1),
		opDone:      make(chan finished, 1),
	}
	s.state = s.pol.New()
	s.publish()
	return s
}

// AfterFailedHandoff reports whether the last hand-off attempt failed; the
// captive portal then lands on the plugin page.
func (s *Supervisor) AfterFailedHandoff() bool { return s.afterFailedHandoff.Load() }

// Run drives the loop until ctx ends, then closes an AP this daemon opened
// and leaves a client connection untouched.
func (s *Supervisor) Run(ctx context.Context) error {
	defer close(s.stopped)
	var wg sync.WaitGroup
	s.start(ctx, &wg, policy.Action{Op: policy.Startup})
	tick := s.clock.After(s.cfg.Tick)
	for {
		select {
		case <-ctx.Done():
			return s.stop(&wg)
		case <-tick:
			tick = s.clock.After(s.cfg.Tick)
			if s.op == nil && !s.observing {
				s.observe(ctx, &wg)
			}
		case o := <-s.observeDone:
			s.observing = false
			s.apply(ctx, &wg, o)
		case f := <-s.opDone:
			s.op = nil
			s.start(ctx, &wg, s.complete(f))
		case ssid := <-s.selected:
			s.picked, s.pickedSSID = true, ssid
		case request := <-s.requests:
			request()
		}
		s.publish()
	}
}

func (s *Supervisor) stop(wg *sync.WaitGroup) error {
	if s.op != nil {
		s.op.cancel()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return s.radio.Shutdown(ctx)
}

func (s *Supervisor) observe(ctx context.Context, wg *sync.WaitGroup) {
	s.observing = true
	apMode := s.state.Phase.Mode() == "ap"
	wg.Add(1)
	go func() {
		defer wg.Done()
		octx, cancel := context.WithTimeout(ctx, s.cfg.ObserveTimeout)
		defer cancel()
		snap, err := s.radio.Observe(octx, apMode)
		if apMode {
			s.radio.Maintain()
		}
		s.observeDone <- observed{snap, err}
	}()
}

// apply feeds one observation to the policy and starts the action it picks.
func (s *Supervisor) apply(ctx context.Context, wg *sync.WaitGroup, o observed) {
	s.applied++
	now := s.clock.Now()
	if o.err != nil && now.Sub(s.lastErrLog) >= errorLogPeriod {
		s.lastErrLog = now
		s.log.Warn("observation incomplete", "error", o.err)
	}
	obs := o.snap.Obs
	obs.Changed, obs.ChangedSSID = raspapconf.Changed(s.networks, o.snap.Networks)
	obs.Selected, obs.SelectedSSID = s.picked, s.pickedSSID
	s.picked, s.pickedSSID = false, ""
	s.networks, s.saved, s.stations = o.snap.Networks, obs.Saved, obs.Stations
	s.notices = nil
	if s.state.Phase.Mode() == "ap" && !o.snap.DnsmasqActive {
		s.notices = []string{"dnsmasq_stopped_outside_airhatch"}
	}
	var next policy.Action
	s.state, next = s.pol.Tick(s.state, obs, now)
	s.start(ctx, wg, next)
}

// start runs an action in the operation slot.
func (s *Supervisor) start(ctx context.Context, wg *sync.WaitGroup, a policy.Action) {
	if a.Op == policy.None {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, opDeadline+a.Budget)
	s.op = &running{name: a.Op, started: s.clock.Now(), budget: a.Budget, cancel: cancel}
	s.log.Info("operation", "op", string(a.Op))
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		s.opDone <- s.run(opCtx, a)
	}()
}

func (s *Supervisor) run(ctx context.Context, a policy.Action) finished {
	f := finished{result: s.operate(ctx, a)}
	if a.Op == policy.Startup {
		networks, err := s.radio.Networks()
		s.warn("read wpa_supplicant.conf", err)
		f.networks, f.result.Saved = networks, raspapconf.Saved(networks)
	}
	f.plan, f.apOpen = s.radio.OpenPlan()
	return f
}

func (s *Supervisor) operate(ctx context.Context, a policy.Action) policy.Result {
	r := policy.Result{Op: a.Op}
	var err error
	switch a.Op {
	case policy.Startup:
		err = s.radio.Cleanup(ctx)
		r.Client, r.Adopted = s.radio.Adopt(ctx)
	case policy.OpenAP:
		err = s.radio.OpenAP(ctx)
	case policy.Reapply:
		if err = s.radio.CloseAP(ctx); err == nil {
			err = s.radio.OpenAP(ctx)
		}
	case policy.CloseAP:
		err = s.radio.CloseAP(ctx)
	case policy.ClientAttempt:
		r.Client, err = s.radio.ClientAttempt(ctx, a.Budget, a.Preferred)
	case policy.StopHostapd:
		err = s.radio.StopHostapd(ctx)
	case policy.RestartSupplicant:
		err = s.radio.RestartSupplicant(ctx)
	}
	if err != nil {
		s.log.Warn("operation failed", "op", string(a.Op), "error", err)
	}
	r.CloseFailed = errors.Is(err, radio.ErrCloseFailed)
	r.Err = publicerror.As(err)
	if err != nil && r.Err == nil && (r.CloseFailed || a.Op == policy.OpenAP || a.Op == policy.Reapply) {
		r.Err = publicerror.Wrap(publicerror.APStartFailed, "", err)
	}
	return r
}

// complete applies an operation's result. The startup's network list is the
// baseline for hand-off triggers.
func (s *Supervisor) complete(f finished) policy.Action {
	if f.result.Op == policy.Startup {
		s.networks, s.saved = f.networks, f.result.Saved
	}
	s.apPlan, s.apOpen = f.plan, f.apOpen
	var next policy.Action
	s.state, next = s.pol.Complete(s.state, f.result, s.clock.Now())
	return next
}

func (s *Supervisor) warn(what string, err error) {
	if err != nil {
		s.log.Warn(what, "error", err)
	}
}
