package supervisor

import (
	"os"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/policy"
)

// Status is the status object of docs/API.md §3.
type Status struct {
	Version         int             `json:"version"`
	State           policy.Phase    `json:"state"`
	Mode            string          `json:"mode"`
	Operation       *Operation      `json:"operation"`
	Override        policy.Override `json:"override"`
	HoldUntil       *time.Time      `json:"hold_until"`
	SavedNetworks   int             `json:"saved_networks"`
	Client          *ClientStatus   `json:"client"`
	AP              *APStatus       `json:"ap"`
	Timers          Timers          `json:"timers"`
	Config          ConfigStatus    `json:"config"`
	LastError       *LastError      `json:"last_error"`
	Notices         []string        `json:"notices"`
	CredentialsFile *string         `json:"credentials_file"`
}

// Operation is the running operation.
type Operation struct {
	Name      policy.Op `json:"name"`
	StartedAt time.Time `json:"started_at"`
}

// ClientStatus is the joined network.
type ClientStatus struct {
	SSID string `json:"ssid"`
	IPv4 string `json:"ipv4"`
}

// APStatus is the open hotspot.
type APStatus struct {
	SSID     string `json:"ssid"`
	IPv4     string `json:"ipv4"`
	Stations int    `json:"stations"`
	Captive  bool   `json:"captive"`
}

// Timers are the deadlines that apply in the current state.
type Timers struct {
	AttemptEndsAt *time.Time `json:"attempt_ends_at"`
	GraceEndsAt   *time.Time `json:"grace_ends_at"`
	RetryAt       *time.Time `json:"retry_at"`
	BusyRetryAt   *time.Time `json:"busy_retry_at"`
	HandoffAt     *time.Time `json:"handoff_at"`
	NextOpenAt    *time.Time `json:"next_open_at"`
	BackoffS      int        `json:"backoff_s"`
}

// ConfigStatus is the part of the config the plugin explains.
type ConfigStatus struct {
	BootTimeoutS       int `json:"boot_timeout_s"`
	ConnectTimeoutS    int `json:"connect_timeout_s"`
	DisconnectGraceS   int `json:"disconnect_grace_s"`
	RetryIntervalS     int `json:"retry_interval_s"`
	RetryIntervalBusyS int `json:"retry_interval_busy_s"`
	HandoffDelayS      int `json:"handoff_delay_s"`
	OffHoldS           int `json:"off_hold_s"`
}

// LastError is the last public error.
type LastError struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Status returns the snapshot published after the loop's last step.
func (s *Supervisor) Status() Status { return *s.status.Load() }

// publish builds the status from loop-owned state.
func (s *Supervisor) publish() {
	st := s.state
	out := Status{
		Version: 1, State: st.Phase, Mode: st.Phase.Mode(), Override: st.Override,
		SavedNetworks: s.saved, Notices: append([]string{}, s.notices...),
		Timers: Timers{BackoffS: seconds(st.Backoff)},
		Config: ConfigStatus{
			BootTimeoutS: seconds(s.cfg.BootTimeout), ConnectTimeoutS: seconds(s.cfg.ConnectTimeout),
			DisconnectGraceS: seconds(s.cfg.DisconnectGrace), RetryIntervalS: seconds(s.cfg.RetryInterval),
			RetryIntervalBusyS: seconds(s.cfg.RetryIntervalBusy), HandoffDelayS: seconds(s.cfg.HandoffDelay),
			OffHoldS: seconds(s.cfg.OffHold),
		},
	}
	if s.op != nil {
		if name, shown := publicOp(s.op.name); shown {
			out.Operation = &Operation{Name: name, StartedAt: s.op.started.UTC()}
		}
		if s.op.name == policy.ClientAttempt {
			out.Timers.AttemptEndsAt = utc(s.op.started.Add(s.op.budget))
		}
	}
	if st.Override == policy.Off {
		out.HoldUntil = utc(st.HoldUntil)
	}
	if st.Phase == policy.Connected {
		out.Client = &ClientStatus{SSID: st.Client.SSID, IPv4: st.Client.IPv4}
	}
	if st.Phase.Mode() == "ap" && s.apOpen {
		out.AP = &APStatus{SSID: s.apPlan.SSID, IPv4: s.apPlan.Prefix.String(), Stations: s.stations, Captive: s.cfg.Captive}
	}
	switch st.Phase {
	case policy.Lost:
		out.Timers.GraceEndsAt = utc(st.DownSince.Add(s.cfg.DisconnectGrace))
	case policy.AP:
		out.Timers.RetryAt, out.Timers.BusyRetryAt = utc(st.RetryAt), utc(st.BusyRetryAt)
	case policy.Handoff:
		out.Timers.HandoffAt = utc(st.HandoffAt)
	case policy.APFailed:
		out.Timers.NextOpenAt = utc(st.NextOpenAt)
	}
	if st.LastError != nil {
		out.LastError = &LastError{Code: string(st.LastError.Code), Message: st.LastError.Message, At: st.LastError.At.UTC()}
	}
	if _, err := os.Stat(s.creds); err == nil {
		out.CredentialsFile = &s.creds
	}
	s.afterFailedHandoff.Store(st.HandoffFailed)
	s.status.Store(&out)
}

// publicOp maps an operation to the status vocabulary. Reapply is a close
// and an open; the startup and the small repairs are not shown.
func publicOp(op policy.Op) (policy.Op, bool) {
	switch op {
	case policy.OpenAP, policy.Reapply:
		return policy.OpenAP, true
	case policy.CloseAP, policy.ClientAttempt:
		return op, true
	}
	return policy.None, false
}

func utc(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func seconds(d time.Duration) int { return int(d / time.Second) }
