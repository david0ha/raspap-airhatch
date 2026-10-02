package radio

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
	"github.com/david0ha/raspap-airhatch/daemon/internal/scan"
	"github.com/david0ha/raspap-airhatch/daemon/internal/standin"
	"github.com/david0ha/raspap-airhatch/daemon/internal/wpactrl"
)

const pollInterval = 200 * time.Millisecond

// OpenAP opens the hotspot (docs/DESIGN.md §5.1). It is a no-op while an AP
// is open. A failure after the client stop runs CloseAP before returning.
func (r *Radio) OpenAP(ctx context.Context) error {
	if r.open != nil {
		return nil
	}
	plan, err := r.Preflight(ctx)
	if err != nil {
		return err
	}
	r.stoppedByDaemon = false
	if err := r.units.Stop(ctx, paths.SupplicantUnit); err != nil {
		return publicerror.Wrap(publicerror.APStartFailed, "", err)
	}
	opened := &openAP{plan: plan}
	err = r.reap(ctx)
	// The stand-in holds the supplicant's socket path for the whole open, so
	// a supplicant started meanwhile cannot bind it and take wlan0.
	if err == nil {
		err = r.bindStandIn()
	}
	if err == nil {
		err = r.startAP(ctx, opened, r.scan(ctx))
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		r.closePortal(opened)
		return errors.Join(publicerror.Wrap(publicerror.APStartFailed, "", err), r.CloseAP(cleanup))
	}
	r.open = opened
	r.warn("start network activity", r.units.Start(ctx, paths.NetworkActivityUnit))
	return nil
}

// bindStandIn keeps a stand-in bound on the supplicant's socket path: the
// one a close left bound, or a new one if there is none or its path was
// removed or replaced.
func (r *Radio) bindStandIn() error {
	if r.standin != nil && !r.standin.Gone() {
		return nil
	}
	r.releaseStandIn()
	server, err := standin.Start(r.standinOptions())
	if err != nil {
		return fmt.Errorf("stand-in: %w", err)
	}
	r.standin = server
	return nil
}

func (r *Radio) reap(ctx context.Context) error {
	if _, err := r.reaper.Reap(ctx); err != nil {
		return fmt.Errorf("reap stray wpa_supplicant: %w", err)
	}
	return nil
}

// scan is best effort: a failed or disabled scan seeds an empty list.
func (r *Radio) scan(ctx context.Context) []byte {
	if r.cfg.ScanTimeout == 0 {
		return nil
	}
	out, err := scan.Run(ctx, r.run, r.cfg.ScanTimeout)
	if err != nil {
		r.log.Warn("pre-AP scan", "error", err)
		return nil
	}
	return out
}

// startAP runs steps 3-9 and records what it opened in opened, also on error.
func (r *Radio) startAP(ctx context.Context, opened *openAP, scanned []byte) error {
	plan := opened.plan
	if err := writeRoot(r.l.ModeFile, "ap\n"); err != nil {
		return err
	}
	if err := r.units.Restart(ctx, paths.DhcpcdUnit); err != nil {
		return err
	}
	if _, err := r.run.Run(ctx, "ip", "-4", "addr", "flush", "dev", paths.Iface); err != nil {
		return err
	}
	if _, err := r.run.Run(ctx, "ip", "addr", "add", plan.Prefix.String(), "dev", paths.Iface); err != nil {
		return err
	}
	if r.cfg.Captive {
		if _, err := r.run.RunInput(ctx, []byte(captive.OpenBatch), "nft", "-f", "-"); err != nil {
			return err
		}
	}
	if err := writeRoot(r.l.DnsmasqConf, captive.DnsmasqAP(plan.Prefix.Addr(), r.cfg.Captive)); err != nil {
		return err
	}
	if err := r.units.Restart(ctx, paths.DnsmasqUnit); err != nil {
		return err
	}
	if err := r.waitDnsmasqActive(ctx); err != nil {
		return err
	}
	if err := writeRoot(r.l.APLease, ""); err != nil {
		return err
	}
	if err := r.units.Start(ctx, paths.HostapdUnit); err != nil {
		return err
	}
	if err := r.waitFor(ctx, func(ctx context.Context) (bool, error) {
		status, err := r.hostapdStatus(ctx)
		return status.enabled, err
	}); err != nil {
		return fmt.Errorf("hostapd not enabled: %w", err)
	}
	return r.startServices(opened, scanned)
}

// startServices seeds the scan cache and starts the portal.
func (r *Radio) startServices(opened *openAP, scanned []byte) error {
	opened.seeder = &scan.Seeder{Dir: r.l.ScanCache, WpaConf: r.l.WpaConf, UID: r.webUID, GID: r.webGID, Data: scanned}
	r.warn("seed scan cache", opened.seeder.Refresh())
	if !r.cfg.Captive {
		return nil
	}
	apIP := opened.plan.Prefix.Addr()
	landing := func() string { return captive.Landing(apIP, r.afterFailedHandoff()) }
	portal, err := r.listen(netip.AddrPortFrom(apIP, paths.PortalPort), captive.Handler(apIP, opened.plan.LighttpdPort, landing))
	if err != nil {
		return fmt.Errorf("portal: %w", err)
	}
	opened.portal = portal
	return nil
}

func (r *Radio) standinOptions() standin.Options {
	mac, _ := os.ReadFile(r.l.WlanMAC)
	return standin.Options{
		Path: r.l.CtrlStandIn, GID: r.netdevGID, MAC: strings.TrimSpace(string(mac)),
		Networks: func() []raspapconf.Network {
			networks, err := r.Networks()
			r.warn("read wpa_supplicant.conf", err)
			return networks
		},
		Triggers: r.selected,
	}
}

func (r *Radio) waitFor(ctx context.Context, probe func(context.Context) (bool, error)) error {
	return clock.PollUntil(ctx, r.clock, r.cfg.APStepTimeout, pollInterval, probe)
}

type hostapdStatus struct {
	enabled  bool
	stations int
}

func (r *Radio) hostapdStatus(ctx context.Context) (hostapdStatus, error) {
	reply, err := wpactrl.Command(ctx, r.l.HostapdCtrl, r.l.RunDir, "STATUS")
	if err != nil {
		return hostapdStatus{}, err
	}
	var s hostapdStatus
	for _, line := range strings.Split(reply, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "state":
			s.enabled = value == "ENABLED"
		case "num_sta[0]":
			fmt.Sscan(value, &s.stations)
		}
	}
	return s, nil
}

// Maintain keeps an open AP's stand-in bound and its scan-cache key current.
func (r *Radio) Maintain() {
	if r.open == nil {
		return
	}
	r.warn("rebind stand-in", r.bindStandIn())
	r.warn("seed scan cache", r.open.seeder.Refresh())
}
