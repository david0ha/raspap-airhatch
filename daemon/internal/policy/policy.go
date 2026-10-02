// Package policy is airhatchd's state machine (docs/DESIGN.md §3) as pure
// functions: it reads observations, operation results and API requests and
// returns the next state and at most one operation to run. It does no I/O.
package policy

import (
	"fmt"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/servicestart"
)

// Phase is the published state.
type Phase string

const (
	Init       Phase = "init"
	Connecting Phase = "connecting"
	Connected  Phase = "connected"
	Lost       Phase = "lost"
	AP         Phase = "ap"
	Handoff    Phase = "handoff"
	APFailed   Phase = "ap_failed"
	Held       Phase = "held"
)

// Mode is wlan0's mode in this phase: client, ap or none.
func (p Phase) Mode() string {
	switch p {
	case Connecting, Connected, Lost:
		return "client"
	case AP, Handoff:
		return "ap"
	}
	return "none"
}

func (p Phase) client() bool { return p.Mode() == "client" }
func (p Phase) ap() bool     { return p.Mode() == "ap" }

// Override is the user's runtime choice; it is auto after boot.
type Override string

const (
	Auto Override = "auto"
	Pin  Override = "ap"
	Off  Override = "off"
)

// Op names an operation of the supervisor's single operation slot.
type Op string

const (
	None              Op = ""
	Startup           Op = "startup"
	OpenAP            Op = "open_ap"
	Reapply           Op = "reapply"
	CloseAP           Op = "close_ap"
	ClientAttempt     Op = "client_attempt"
	StopHostapd       Op = "stop_hostapd"
	RestartSupplicant Op = "restart_supplicant"
)

// Action is the operation to start next.
type Action struct {
	Op        Op
	Budget    time.Duration
	Preferred string
}

// Client is the network wlan0 is joined to.
type Client struct {
	SSID string
	IPv4 string
}

// Failure is the last public error and when it happened.
type Failure struct {
	*publicerror.Error
	At time.Time
}

// State is owned by the supervisor loop and changed only through Policy.
type State struct {
	Phase         Phase
	Override      Override
	HoldUntil     time.Time
	DownSince     time.Time
	RetryAt       time.Time
	BusyRetryAt   time.Time
	HandoffAt     time.Time
	NextOpenAt    time.Time
	Backoff       time.Duration
	NeedClose     bool
	HandoffFailed bool
	LastError     *Failure
	Client        Client

	preferred          string
	attemptFromHandoff bool
	pendingOpen        bool
	pendingOpenAt      time.Time
	pendingReapply     bool
	pendingRetry       bool
	pendingStop        bool
	// afterClose is a result whose follow-up waits for close_ap: its own
	// close failed, so nothing may start while the AP fences are up.
	afterClose *Result
}

// New is the state at daemon start.
func (p Policy) New() State {
	return State{Phase: Init, Override: Auto, Backoff: p.Cfg.BackoffMin}
}

// Observation is one tick's view of the system, plus the triggers the
// supervisor collected since the previous tick.
type Observation struct {
	Saved                  int
	Changed                bool
	ChangedSSID            string
	Selected               bool
	SelectedSSID           string
	StrayReaped            bool
	ClientUp               bool
	ClientSSID             string
	ClientIPv4             string
	SupplicantSocketGone   bool
	HostapdActive          bool
	HostapdResult          string
	HostapdEnabled         bool
	HostapdStoppedByDaemon bool
	Stations               int
}

// Result is a finished operation.
type Result struct {
	Op          Op
	Err         *publicerror.Error
	CloseFailed bool
	Adopted     bool
	Client      Client
	Saved       int
}

// Policy holds the timers the rules use.
type Policy struct {
	Cfg config.Config
}

// Tick applies the rules for a phase with no operation running.
func (p Policy) Tick(s State, o Observation, now time.Time) (State, Action) {
	if s.NeedClose {
		return s, Action{Op: CloseAP}
	}
	if s.Override == Off && !now.Before(s.HoldUntil) {
		s.Override, s.HoldUntil = Auto, time.Time{}
	}
	switch {
	case s.Phase == Connected || s.Phase == Lost:
		return p.tickClient(s, o, now)
	case s.Phase.ap():
		return p.tickAP(s, o, now)
	case s.Phase == APFailed || s.Phase == Held:
		return p.tickIdle(s, o, now)
	}
	return s, Action{}
}

func (p Policy) tickClient(s State, o Observation, now time.Time) (State, Action) {
	switch {
	case o.HostapdActive:
		s.fail(publicerror.New(publicerror.APServiceOutsideDaemon, ""), now)
		return s, Action{Op: StopHostapd}
	case o.SupplicantSocketGone:
		return s, Action{Op: RestartSupplicant}
	case s.pendingOpen && !now.Before(s.pendingOpenAt):
		s.pendingOpen = false
		return s, Action{Op: OpenAP}
	case s.pendingRetry:
		return p.attempt(s, p.Cfg.ConnectTimeout, "", false)
	case o.ClientUp:
		s.Phase, s.DownSince = Connected, time.Time{}
		s.Client = Client{SSID: o.ClientSSID, IPv4: o.ClientIPv4}
		return s, Action{}
	case s.Phase == Connected:
		s.Phase, s.DownSince = Lost, now
		return s, Action{}
	case s.Override != Off && now.Sub(s.DownSince) >= p.Cfg.DisconnectGrace:
		return s, Action{Op: OpenAP}
	}
	return s, Action{}
}

func (p Policy) tickAP(s State, o Observation, now time.Time) (State, Action) {
	userStopped := !o.HostapdActive && o.HostapdResult == "success" && !o.HostapdStoppedByDaemon
	if s.pendingStop || userStopped {
		s.pendingStop = false
		s.Override, s.HoldUntil = Off, now.Add(p.Cfg.OffHold)
		if o.Saved > 0 {
			return p.attempt(s, p.Cfg.ConnectTimeout, "", false)
		}
		s.Phase = Held
		return s, Action{Op: CloseAP}
	}
	if !o.HostapdActive {
		s = p.openFailed(s, publicerror.New(publicerror.APStartFailed, ""), now)
		return s, Action{Op: CloseAP}
	}
	if s.pendingReapply {
		s.pendingReapply = false
		return s, Action{Op: Reapply}
	}
	trigger := o.Changed || o.Selected || o.StrayReaped || s.pendingRetry
	s.pendingRetry = false
	if trigger && o.Saved > 0 {
		if s.Override == Pin {
			s.Override = Auto
		}
		s.Phase, s.HandoffAt = Handoff, now.Add(p.Cfg.HandoffDelay)
		switch {
		case o.Selected:
			s.preferred = o.SelectedSSID
		case o.Changed:
			s.preferred = o.ChangedSSID
		}
		return s, Action{}
	}
	if o.StrayReaped && !o.HostapdEnabled {
		return s, Action{Op: Reapply}
	}
	if s.Phase == Handoff {
		if now.Before(s.HandoffAt) {
			return s, Action{}
		}
		return p.attempt(s, p.Cfg.ConnectTimeout, s.preferred, true)
	}
	idleRetry := !now.Before(s.RetryAt) && o.Stations == 0
	if s.Override == Auto && o.Saved > 0 && (idleRetry || !now.Before(s.BusyRetryAt)) {
		return p.attempt(s, p.Cfg.ConnectTimeout, "", false)
	}
	return s, Action{}
}

func (p Policy) tickIdle(s State, o Observation, now time.Time) (State, Action) {
	if s.pendingOpen && !now.Before(s.pendingOpenAt) {
		s.pendingOpen = false
		return s, Action{Op: OpenAP}
	}
	if (s.pendingRetry || o.Changed) && o.Saved > 0 {
		return p.attempt(s, p.Cfg.ConnectTimeout, "", false)
	}
	due := s.Phase == APFailed && !now.Before(s.NextOpenAt) || s.Phase == Held && s.Override != Off
	if !due {
		return s, Action{}
	}
	if o.Saved > 0 {
		return p.attempt(s, p.Cfg.ConnectTimeout, "", false)
	}
	return s, Action{Op: OpenAP}
}

func (p Policy) attempt(s State, budget time.Duration, preferred string, fromHandoff bool) (State, Action) {
	s.Phase, s.HandoffAt, s.preferred, s.pendingRetry = Connecting, time.Time{}, "", false
	s.attemptFromHandoff = fromHandoff
	if !fromHandoff {
		s.HandoffFailed = false
	}
	return s, Action{Op: ClientAttempt, Budget: budget, Preferred: preferred}
}

func (s *State) fail(err *publicerror.Error, now time.Time) {
	s.LastError = &Failure{Error: err, At: now}
}

func (p Policy) openFailed(s State, err *publicerror.Error, now time.Time) State {
	s.fail(err, now)
	s.Phase, s.NextOpenAt = APFailed, now.Add(s.Backoff)
	s.Backoff = min(2*s.Backoff, p.Cfg.BackoffMax)
	return s
}

// Complete applies a finished operation's result. While a close is
// outstanding (NeedClose) it starts nothing: Tick retries close_ap first, and
// the follow-up of the result whose close failed is applied once it succeeds.
func (p Policy) Complete(s State, r Result, now time.Time) (State, Action) {
	if r.Op == CloseAP {
		s.NeedClose = r.Err != nil
		if s.NeedClose || s.afterClose == nil {
			return s, Action{}
		}
		r, s.afterClose = *s.afterClose, nil
	}
	s, a := p.complete(s, r, now)
	if r.CloseFailed {
		s.NeedClose = true
		if a.Op != None {
			r.CloseFailed = false
			s.afterClose = &r
		}
		return s, Action{}
	}
	return s, a
}

func (p Policy) complete(s State, r Result, now time.Time) (State, Action) {
	switch r.Op {
	case Startup:
		switch {
		case r.Adopted:
			s.pendingOpen = false
			s.Phase, s.Client = Connected, r.Client
		case s.pendingOpen || r.Saved == 0:
			s.pendingOpen = false
			return s, Action{Op: OpenAP}
		default:
			return p.attempt(s, p.Cfg.BootTimeout, "", false)
		}
	case OpenAP, Reapply:
		if r.Err != nil {
			return p.openFailed(s, r.Err, now), Action{}
		}
		s.Phase, s.pendingOpen = AP, false
		s.RetryAt, s.BusyRetryAt = now.Add(p.Cfg.RetryInterval), now.Add(p.Cfg.RetryIntervalBusy)
		s.NextOpenAt, s.Backoff = time.Time{}, p.Cfg.BackoffMin
	case ClientAttempt:
		if r.Err == nil {
			s.Phase, s.Client = Connected, r.Client
			if s.Override == Off {
				s.Override, s.HoldUntil = Auto, time.Time{}
			}
			s.NextOpenAt, s.Backoff, s.LastError, s.HandoffFailed = time.Time{}, p.Cfg.BackoffMin, nil, false
			return s, Action{}
		}
		s.fail(r.Err, now)
		s.HandoffFailed = s.attemptFromHandoff
		if s.Override == Off {
			s.Phase = Held
			return s, Action{}
		}
		return s, Action{Op: OpenAP}
	}
	return s, Action{}
}

// OpenAP is the API "open_ap": pin the hotspot. From a client state it opens
// after handoff_delay, so the reply reaches the page first.
func (p Policy) OpenAP(s State, now time.Time) (State, string) {
	s.Override, s.HoldUntil = Pin, time.Time{}
	switch {
	case s.Phase == AP:
		return s, "The hotspot is already open; it stays open until you choose Back to automatic."
	case s.Phase == Handoff:
		s.Phase, s.HandoffAt, s.preferred = AP, time.Time{}, ""
		return s, "Hand-off cancelled; the hotspot stays open."
	case s.Phase.client():
		s.pendingOpen, s.pendingOpenAt = true, now.Add(p.Cfg.HandoffDelay)
		return s, fmt.Sprintf("The hotspot opens in %s. This page loses the device until you join the hotspot.",
			servicestart.FormatDuration(p.Cfg.HandoffDelay))
	}
	s.pendingOpen, s.pendingOpenAt = true, now
	return s, "Opening the hotspot."
}

// RetryClient is the API "retry_client".
func (p Policy) RetryClient(s State, saved int, now time.Time) (State, string, *publicerror.Error) {
	if saved == 0 {
		return s, "", publicerror.New(publicerror.NoSavedNetwork, "")
	}
	switch {
	case s.Phase == Init || s.Phase == Connecting:
		return s, "A Wi-Fi attempt is already running.", nil
	case s.Phase.ap():
		s.pendingRetry = true
		return s, fmt.Sprintf("The hotspot closes in %s while the device tries the saved Wi-Fi networks. If that fails, it comes back.",
			servicestart.FormatDuration(p.Cfg.HandoffDelay)), nil
	}
	s.pendingRetry = true
	return s, "Trying the saved Wi-Fi networks again.", nil
}

// Auto is the API "auto": back to automatic, hold cleared.
func (p Policy) Auto(s State) (State, string) {
	s.Override, s.HoldUntil, s.pendingOpen = Auto, time.Time{}, false
	return s, "Back to automatic: Wi-Fi first, the hotspot only when Wi-Fi is unavailable."
}

// ServiceStart handles RaspAP's hotspot Start/Restart/Stop (DESIGN §7) and
// returns the line the shim prints.
func (p Policy) ServiceStart(s State, r servicestart.Request, now time.Time) (State, string) {
	if !r.Supported() {
		return s, servicestart.Unsupported
	}
	if r.Stop {
		s.Override, s.HoldUntil, s.pendingOpen = Off, now.Add(p.Cfg.OffHold), false
		s.pendingStop = s.Phase.ap()
		return s, servicestart.Stopped(p.Cfg.OffHold)
	}
	switch {
	case s.Phase.client():
		s.Override, s.HoldUntil, s.pendingOpen = Auto, time.Time{}, false
		return s, servicestart.ClientActive
	case s.Phase == AP:
		s.pendingReapply = true
		return s, servicestart.Restarting
	case s.Phase == Handoff:
		return s, servicestart.HandoffBusy
	}
	s.Override, s.HoldUntil = Auto, time.Time{}
	s.pendingOpen, s.pendingOpenAt = true, now
	return s, servicestart.Opening
}
