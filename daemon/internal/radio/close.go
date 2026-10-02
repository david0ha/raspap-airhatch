package radio

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/dhcpcdrender"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/standin"
)

// CloseAP closes the hotspot (docs/DESIGN.md §5.2). Every step is
// idempotent, so it is safe when nothing is open. Its safety steps are
// mandatory: every hostapd unit stopped, the client fence loaded by a
// running dnsmasq, the nft table deleted and the AP address gone from wlan0.
// If one fails, CloseAP returns ErrCloseFailed and the close stays pending:
// no client starts or is adopted until a CloseAP succeeds. When hostapd does
// not stop, the fences stay up. The stand-in stays bound, so RaspAP never
// finds the supplicant's socket path free while the AP side comes down; the
// client start releases it. Once hostapd is stopped, the remaining steps run
// on a detached context: they remove the fences and must not be cut short
// by a cancelled operation.
func (r *Radio) CloseAP(ctx context.Context) error {
	r.closePending = true
	r.closePortal(r.open)
	r.open = nil
	if err := removeIfExists(r.l.APLease); err != nil {
		return fmt.Errorf("%w: remove lease: %w", ErrCloseFailed, err)
	}
	r.stoppedByDaemon = true
	if err := r.stopHostapd(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCloseFailed, err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	fence := r.fenceDnsmasq(ctx)
	nft := r.deleteNftTable(ctx)
	address := r.removeAPAddress(ctx)
	r.warn("write mode", writeRoot(r.l.ModeFile, string(dhcpcdrender.Client)+"\n"))
	r.warn("restart dhcpcd", r.units.Restart(ctx, paths.DhcpcdUnit))
	if err := errors.Join(fence, nft, address); err != nil {
		return fmt.Errorf("%w: %w", ErrCloseFailed, err)
	}
	r.closePending = false
	return nil
}

// closePortal stops the portal of an open AP.
func (r *Radio) closePortal(opened *openAP) {
	if opened != nil && opened.portal != nil {
		r.warn("close portal", opened.portal.Close())
	}
}

// releaseStandIn frees the supplicant's socket path.
func (r *Radio) releaseStandIn() {
	if r.standin != nil {
		r.warn("close stand-in", r.standin.Close())
		r.standin = nil
	}
}

// stopHostapd stops hostapd and every hostapd@ instance, and confirms that
// none runs.
func (r *Radio) stopHostapd(ctx context.Context) error {
	if err := r.units.Stop(ctx, paths.HostapdUnit, paths.HostapdInstances); err != nil {
		return err
	}
	running, err := r.hostapdRunning(ctx)
	if err == nil && running {
		err = errors.New("a hostapd unit is still running")
	}
	return err
}

func (r *Radio) hostapdRunning(ctx context.Context) (bool, error) {
	return r.units.AnyRunning(ctx, paths.HostapdUnit, paths.HostapdInstances)
}

// fenceDnsmasq takes DHCP and DNS off wlan0: the client include, loaded by a
// dnsmasq restart that leaves dnsmasq active.
func (r *Radio) fenceDnsmasq(ctx context.Context) error {
	write := writeRoot(r.l.DnsmasqConf, captive.DnsmasqClient)
	if err := r.units.Restart(ctx, paths.DnsmasqUnit); err != nil {
		return errors.Join(write, fmt.Errorf("restart dnsmasq: %w", err))
	}
	return errors.Join(write, r.waitDnsmasqActive(ctx))
}

func (r *Radio) waitDnsmasqActive(ctx context.Context) error {
	if err := r.waitFor(ctx, func(ctx context.Context) (bool, error) {
		return r.units.IsActive(ctx, paths.DnsmasqUnit), nil
	}); err != nil {
		return fmt.Errorf("dnsmasq not active: %w", err)
	}
	return nil
}

func (r *Radio) deleteNftTable(ctx context.Context) error {
	if _, err := r.run.RunInput(ctx, []byte(captive.CloseBatch), "nft", "-f", "-"); err != nil {
		return fmt.Errorf("delete nft table: %w", err)
	}
	return nil
}

// removeAPAddress flushes wlan0's IPv4 and confirms the AP address is gone.
func (r *Radio) removeAPAddress(ctx context.Context) error {
	if _, err := r.run.Run(ctx, "ip", "-4", "addr", "flush", "dev", paths.Iface); err != nil {
		return fmt.Errorf("flush wlan0: %w", err)
	}
	present, err := r.hasAPAddress()
	if err == nil && present {
		err = errors.New("AP address still on wlan0")
	}
	return err
}

// hasAPAddress reports whether wlan0 carries the AP address of RaspAP's
// dhcpcd.conf.
func (r *Radio) hasAPAddress() (bool, error) {
	apAddr := r.apAddr()
	if !apAddr.IsValid() {
		return false, nil
	}
	addrs, err := r.addrs()
	if err != nil {
		return false, fmt.Errorf("list wlan0 addresses: %w", err)
	}
	for _, p := range addrs {
		if p.Addr() == apAddr {
			return true, nil
		}
	}
	return false, nil
}

// Cleanup removes what a previous airhatchd left behind (docs/DESIGN.md §5.4)
// before any decision, and is what "airhatchd cleanup" runs after airhatchd
// exits: strays, a dead stand-in socket, an AP and the nft table. A failure
// leaves the close pending, like a failed CloseAP.
func (r *Radio) Cleanup(ctx context.Context) error {
	if err := r.reap(ctx); err != nil {
		r.closePending = true
		return fmt.Errorf("%w: %w", ErrCloseFailed, err)
	}
	r.warn("remove stale stand-in", standin.RemoveStale(r.l.CtrlStandIn))
	if r.apLeft(ctx) {
		return r.CloseAP(ctx)
	}
	if err := r.deleteNftTable(ctx); err != nil {
		r.closePending = true
		return fmt.Errorf("%w: %w", ErrCloseFailed, err)
	}
	return nil
}

// apLeft reports whether any trace of an AP remains: the lease, mode=ap, a
// dhcpcd running the AP render, a dnsmasq include that is not the client
// fence, the AP address on wlan0, or a running hostapd unit (any instance;
// an unreadable unit list counts as running).
func (r *Radio) apLeft(ctx context.Context) bool {
	if exists(r.l.APLease) || dhcpcdrender.ReadMode(r.l.ModeFile) == dhcpcdrender.AP {
		return true
	}
	if rendered, err := os.ReadFile(r.l.DhcpcdConf); err == nil && dhcpcdrender.DeniesWlan0(rendered) {
		return true
	}
	if include, err := os.ReadFile(r.l.DnsmasqConf); err != nil || !captive.IsClientInclude(include) {
		return true
	}
	present, err := r.hasAPAddress()
	r.warn("AP address", err)
	if present {
		return true
	}
	running, err := r.hostapdRunning(ctx)
	r.warn("list hostapd units", err)
	return running || err != nil
}

// Shutdown closes an AP this daemon opened, or finishes a close that failed,
// leaves a client untouched, and releases the stand-in.
func (r *Radio) Shutdown(ctx context.Context) error {
	var err error
	if r.open != nil || r.closePending {
		err = r.CloseAP(ctx)
	}
	r.releaseStandIn()
	return err
}
