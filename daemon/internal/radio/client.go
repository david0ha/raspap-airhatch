package radio

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/policy"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
	"github.com/david0ha/raspap-airhatch/daemon/internal/wpactrl"
)

// ClientAttempt joins a saved network within budget (docs/DESIGN.md §5.3).
// preferred, when set, is selected for this attempt only (never saved).
func (r *Radio) ClientAttempt(ctx context.Context, budget time.Duration, preferred string) (policy.Client, error) {
	wpa, err := raspapconf.ReadWpa(r.l.WpaConf)
	if err != nil {
		return policy.Client{}, publicerror.Wrap(publicerror.WpaConfUnusable, "", err)
	}
	if raspapconf.Saved(wpa.Networks) == 0 {
		return policy.Client{}, publicerror.New(publicerror.NoSavedNetwork, "")
	}
	// The conf names the directory on the device, so it is compared with the
	// image's path even when the layout is relocated.
	if wpa.ControlDir() != paths.Default().CtrlDir {
		return policy.Client{}, publicerror.New(publicerror.WpaConfUnusable, "")
	}
	if r.open != nil || r.closePending {
		if err := r.CloseAP(ctx); err != nil {
			return policy.Client{}, err
		}
	}
	if err := r.reap(ctx); err != nil {
		return policy.Client{}, err
	}
	deadline := r.clock.Now().Add(budget)
	name := attemptSSID(preferred, wpa.Networks)
	client, err := r.attempt(ctx, deadline, preferred, name)
	if err == nil {
		return client, nil
	}
	r.warn("stop wpa_supplicant", r.units.Stop(context.WithoutCancel(ctx), paths.SupplicantUnit))
	if publicerror.As(err) == nil {
		err = publicerror.Wrap(publicerror.ConnectTimeout, name, err)
	}
	return policy.Client{}, err
}

// attempt runs steps 3-7; name is the network to mention in an error. The
// stand-in gives the socket path to the supplicant right before it starts.
func (r *Radio) attempt(ctx context.Context, deadline time.Time, preferred, name string) (policy.Client, error) {
	r.releaseStandIn()
	if r.units.IsActive(ctx, paths.SupplicantUnit) {
		if err := r.request(ctx, "RECONFIGURE"); err != nil {
			return policy.Client{}, err
		}
	} else if err := r.units.Start(ctx, paths.SupplicantUnit); err != nil {
		return policy.Client{}, err
	}
	if err := clock.PollUntil(ctx, r.clock, 5*time.Second, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		reply, err := r.command(ctx, "PING")
		return reply == "PONG\n", err
	}); err != nil {
		return policy.Client{}, fmt.Errorf("wpa_supplicant control socket: %w", err)
	}
	events, err := r.watchEvents(ctx)
	if err != nil {
		return policy.Client{}, err
	}
	defer events.conn.Close()
	if preferred != "" {
		if err := r.selectNetwork(ctx, preferred); err != nil {
			return policy.Client{}, err
		}
	}
	var status supplicantStatus
	assoc := min(r.cfg.AssocTimeout, deadline.Sub(r.clock.Now()))
	err = clock.PollUntil(ctx, r.clock, assoc, time.Second, func(ctx context.Context) (bool, error) {
		status, err = r.supplicantStatus(ctx)
		events.drain(ctx)
		return status.completed, err
	})
	if err != nil {
		return policy.Client{}, events.classify(ctx, false, name)
	}
	var ipv4 netip.Prefix
	err = clock.PollUntil(ctx, r.clock, deadline.Sub(r.clock.Now()), time.Second, func(context.Context) (bool, error) {
		ipv4 = r.clientIPv4()
		return ipv4.IsValid(), nil
	})
	if err != nil {
		return policy.Client{}, events.classify(ctx, true, status.ssid)
	}
	return policy.Client{SSID: status.ssid, IPv4: ipv4.String()}, nil
}

func (r *Radio) command(ctx context.Context, cmd string) (string, error) {
	return wpactrl.Command(ctx, r.l.CtrlStandIn, r.l.RunDir, cmd)
}

func (r *Radio) request(ctx context.Context, cmd string) error {
	reply, err := r.command(ctx, cmd)
	if err != nil {
		return err
	}
	if reply != "OK\n" {
		return fmt.Errorf("%s: %q", cmd, strings.TrimSpace(reply))
	}
	return nil
}

func (r *Radio) selectNetwork(ctx context.Context, ssid string) error {
	list, err := r.command(ctx, "LIST_NETWORKS")
	if err != nil {
		return err
	}
	for _, row := range strings.Split(list, "\n")[1:] {
		cols := strings.Split(row, "\t")
		if len(cols) >= 2 && raspapconf.DecodeText(cols[1]) == ssid {
			return r.request(ctx, "SELECT_NETWORK "+cols[0])
		}
	}
	return publicerror.New(publicerror.NetworkNotFound, ssid)
}

type supplicantStatus struct {
	completed bool
	ssid      string
}

func (r *Radio) supplicantStatus(ctx context.Context) (supplicantStatus, error) {
	reply, err := r.command(ctx, "STATUS")
	if err != nil {
		return supplicantStatus{}, err
	}
	var s supplicantStatus
	for _, line := range strings.Split(reply, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "wpa_state":
			s.completed = value == "COMPLETED"
		case "ssid":
			s.ssid = raspapconf.DecodeText(value)
		}
	}
	return s, nil
}

// clientIPv4 is wlan0's first global IPv4 that is not the AP address.
func (r *Radio) clientIPv4() netip.Prefix {
	addrs, err := r.addrs()
	if err != nil {
		return netip.Prefix{}
	}
	apAddr := r.apAddr()
	for _, p := range addrs {
		if p.Addr().Is4() && p.Addr().IsGlobalUnicast() && p.Addr() != apAddr {
			return p
		}
	}
	return netip.Prefix{}
}

// apAddr is the AP address in RaspAP's dhcpcd.conf, if it names one.
func (r *Radio) apAddr() netip.Addr {
	dhcpcd, err := os.ReadFile(r.l.RaspAPDhcpcd)
	if err != nil {
		return netip.Addr{}
	}
	prefix, _ := raspapconf.APAddress(dhcpcd)
	return prefix.Addr()
}

// Adopt reports a supplicant that is already connected, so a daemon restart
// keeps the connection instead of restarting it. Nothing is adopted while a
// close is pending.
func (r *Radio) Adopt(ctx context.Context) (policy.Client, bool) {
	if r.closePending || !r.units.IsActive(ctx, paths.SupplicantUnit) {
		return policy.Client{}, false
	}
	status, err := r.supplicantStatus(ctx)
	ipv4 := r.clientIPv4()
	if err != nil || !status.completed || !ipv4.IsValid() {
		return policy.Client{}, false
	}
	return policy.Client{SSID: status.ssid, IPv4: ipv4.String()}, true
}

// eventWatch is an attached control socket whose queued events explain a
// failed attempt. It is drained after every status poll.
type eventWatch struct {
	conn      *wpactrl.Conn
	wrongKey  string
	wrongSeen bool
	notFound  bool
}

func (r *Radio) watchEvents(ctx context.Context) (*eventWatch, error) {
	conn, err := wpactrl.Dial(r.l.CtrlStandIn, r.l.RunDir)
	if err != nil {
		return nil, err
	}
	if err := conn.Attach(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return &eventWatch{conn: conn}, nil
}

// drain records the events already queued on the socket.
func (w *eventWatch) drain(ctx context.Context) {
	for {
		short, cancel := context.WithTimeout(ctx, time.Millisecond)
		event, err := w.conn.Receive(short)
		cancel()
		if err != nil {
			return
		}
		switch {
		case strings.Contains(event, "CTRL-EVENT-SSID-TEMP-DISABLED") && strings.Contains(event, "reason=WRONG_KEY"):
			w.wrongSeen, w.wrongKey = true, eventSSID(event)
		case strings.Contains(event, "CTRL-EVENT-NETWORK-NOT-FOUND"):
			w.notFound = true
		}
	}
}

// classify turns what the attempt saw into the public error.
func (w *eventWatch) classify(ctx context.Context, completed bool, ssid string) error {
	w.drain(ctx)
	switch {
	case completed:
		return publicerror.New(publicerror.NoDHCPLease, ssid)
	case w.wrongSeen:
		return publicerror.New(publicerror.WrongPassword, w.wrongKey)
	case w.notFound:
		return publicerror.New(publicerror.NetworkNotFound, ssid)
	}
	return publicerror.New(publicerror.ConnectTimeout, ssid)
}

// eventSSID extracts ssid="…" from a supplicant event.
func eventSSID(event string) string {
	_, rest, ok := strings.Cut(event, ` ssid="`)
	if !ok {
		return ""
	}
	end := strings.LastIndex(rest, `"`)
	if end < 0 {
		return ""
	}
	return raspapconf.DecodeText(rest[:end])
}

// attemptSSID names the network of an attempt when there is a single one.
func attemptSSID(preferred string, networks []raspapconf.Network) string {
	if preferred != "" {
		return preferred
	}
	var only string
	for _, n := range networks {
		if n.SSID != "" && !n.Disabled {
			if only != "" {
				return ""
			}
			only = n.SSID
		}
	}
	return only
}
