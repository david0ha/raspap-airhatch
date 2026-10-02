package radio

import (
	"context"
	"errors"

	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/policy"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
)

// Snapshot is one tick's observation (docs/DESIGN.md §5.5). Obs leaves the
// trigger fields that need history (Changed, Selected) to the supervisor.
type Snapshot struct {
	Obs           policy.Observation
	Networks      []raspapconf.Network
	DnsmasqActive bool
}

// Observe reads the system for a phase of the given mode. Strays are reaped
// in every mode. The error collects what could not be read.
func (r *Radio) Observe(ctx context.Context, apMode bool) (Snapshot, error) {
	var snap Snapshot
	var errs []error
	networks, err := r.Networks()
	errs = append(errs, err)
	snap.Networks = networks
	snap.Obs.Saved = raspapconf.Saved(networks)
	reaped, err := r.reaper.Reap(ctx)
	errs = append(errs, err)
	snap.Obs.StrayReaped = reaped > 0
	snap.Obs.HostapdStoppedByDaemon = r.stoppedByDaemon

	hostapd, err := r.units.Show(ctx, paths.HostapdUnit)
	errs = append(errs, err)
	snap.Obs.HostapdActive = hostapd.Active != "inactive" && hostapd.Active != "failed"
	if err != nil {
		// An unreadable unit keeps the current mode's expectation, so no
		// rule acts on a guess.
		snap.Obs.HostapdActive = apMode
	}
	snap.Obs.HostapdResult = hostapd.Result

	if apMode {
		status, err := r.hostapdStatus(ctx)
		errs = append(errs, err)
		snap.Obs.HostapdEnabled = err != nil || status.enabled
		snap.Obs.Stations = status.stations
		snap.DnsmasqActive = r.units.IsActive(ctx, paths.DnsmasqUnit)
		return snap, errors.Join(errs...)
	}
	if !exists(r.l.CtrlStandIn) {
		snap.Obs.SupplicantSocketGone = r.units.IsActive(ctx, paths.SupplicantUnit)
	} else {
		status, err := r.supplicantStatus(ctx)
		errs = append(errs, err)
		ipv4 := r.clientIPv4()
		snap.Obs.ClientUp = status.completed && ipv4.IsValid()
		if snap.Obs.ClientUp {
			snap.Obs.ClientSSID, snap.Obs.ClientIPv4 = status.ssid, ipv4.String()
		}
	}
	return snap, errors.Join(errs...)
}

// Networks reads RaspAP's saved networks; a missing file has none.
func (r *Radio) Networks() ([]raspapconf.Network, error) {
	wpa, err := raspapconf.ReadWpa(r.l.WpaConf)
	return wpa.Networks, err
}

// StopHostapd stops a hostapd that was started outside airhatchd while
// wlan0 is a client, and any hostapd@ instance.
func (r *Radio) StopHostapd(ctx context.Context) error {
	return r.units.Stop(ctx, paths.HostapdUnit, paths.HostapdInstances)
}

// RestartSupplicant restarts the client after its socket was deleted
// (RaspAP's "Reinitialize").
func (r *Radio) RestartSupplicant(ctx context.Context) error {
	return r.units.Restart(ctx, paths.SupplicantUnit)
}
