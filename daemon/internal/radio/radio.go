// Package radio runs the mode switches of wlan0 in the exact order of
// docs/DESIGN.md §5: opening and closing the hotspot, a client attempt, the
// stale cleanup at daemon start, and each tick's observation.
package radio

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/atomicfile"
	"github.com/david0ha/raspap-airhatch/daemon/internal/captive"
	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
	"github.com/david0ha/raspap-airhatch/daemon/internal/scan"
	"github.com/david0ha/raspap-airhatch/daemon/internal/standin"
	"github.com/david0ha/raspap-airhatch/daemon/internal/units"
)

// ErrCloseFailed means close_ap (or the stale cleanup) did not reach a
// verified safe state, so close_ap must be retried before anything else runs
// on wlan0.
var ErrCloseFailed = errors.New("close_ap did not finish")

// cleanupTimeout bounds the steps that remove the AP fences; they run on a
// detached context so a cancelled operation cannot leave them half done.
const cleanupTimeout = 30 * time.Second

// Radio owns wlan0's mode. The supervisor calls its operations one at a time.
type Radio struct {
	l                  paths.Layout
	cfg                config.Config
	run                runner.InputRunner
	units              units.Units
	clock              clock.Clock
	reaper             Reaper
	log                *slog.Logger
	addrs              func() ([]netip.Prefix, error)
	listen             func(netip.AddrPort, http.Handler) (io.Closer, error)
	afterFailedHandoff func() bool
	selected           chan<- string

	netdevGID int
	webUID    int
	webGID    int

	open *openAP
	// standin outlives open: it is released only for a client start.
	standin         *standin.Server
	stoppedByDaemon bool
	closePending    bool
}

// openAP is what a successful open_ap holds until close_ap.
type openAP struct {
	plan   Plan
	portal io.Closer
	seeder *scan.Seeder
}

// Options are Radio's dependencies.
type Options struct {
	Layout paths.Layout
	Config config.Config
	Runner runner.InputRunner
	Clock  clock.Clock
	Log    *slog.Logger
	Reaper Reaper
	// Addrs lists wlan0's addresses.
	Addrs func() ([]netip.Prefix, error)
	// Listen starts the captive portal listener.
	Listen func(netip.AddrPort, http.Handler) (io.Closer, error)
	// AfterFailedHandoff picks the portal's landing page (captive.Landing).
	AfterFailedHandoff func() bool
	// Selected receives the SSID of a SELECT_NETWORK on the stand-in.
	Selected  chan<- string
	NetdevGID int
	WebUID    int
	WebGID    int
}

// New builds a Radio.
func New(o Options) *Radio {
	return &Radio{
		l: o.Layout, cfg: o.Config, run: o.Runner, units: units.Units{Runner: o.Runner},
		clock: o.Clock, reaper: o.Reaper, log: o.Log, addrs: o.Addrs, listen: o.Listen,
		afterFailedHandoff: o.AfterFailedHandoff, selected: o.Selected,
		netdevGID: o.NetdevGID, webUID: o.WebUID, webGID: o.WebGID,
	}
}

// SystemAddrs lists the addresses of wlan0 on this host.
func SystemAddrs() ([]netip.Prefix, error) {
	iface, err := net.InterfaceByName(paths.Iface)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			addr, _ := netip.AddrFromSlice(ipnet.IP)
			ones, _ := ipnet.Mask.Size()
			out = append(out, netip.PrefixFrom(addr.Unmap(), ones))
		}
	}
	return out, nil
}

// SystemListen starts the captive portal listener.
func SystemListen(addr netip.AddrPort, h http.Handler) (io.Closer, error) {
	return captive.Listen(addr, h)
}

// OpenPlan is the plan of the open AP.
func (r *Radio) OpenPlan() (Plan, bool) {
	if r.open == nil {
		return Plan{}, false
	}
	return r.open.plan, true
}

func writeRoot(path, content string) error {
	return atomicfile.Write(path, []byte(content), atomicfile.Options{Mode: 0644, UID: -1, GID: -1})
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// warn logs a step failure that does not stop the sequence.
func (r *Radio) warn(step string, err error) {
	if err != nil {
		r.log.Warn(step, "error", err)
	}
}
