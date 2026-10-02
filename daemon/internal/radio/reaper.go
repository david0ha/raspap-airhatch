package radio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
)

// Reaper ends wpa_supplicant processes on wlan0 that airhatchd did not start
// (RaspAP's "Reinitialize" and its WiFi page start them with sudo).
type Reaper struct {
	Proc  string
	Kill  func(pid int, sig syscall.Signal) error
	Clock clock.Clock
}

// SystemKill signals a process.
func SystemKill(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// Reap sends SIGTERM to every stray, SIGKILL to those still alive after 2 s,
// and returns how many it found.
func (r Reaper) Reap(ctx context.Context) (int, error) {
	strays, err := r.strays()
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, pid := range strays {
		if err := r.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	for _, pid := range strays {
		gone := clock.PollUntil(ctx, r.Clock, 2*time.Second, 100*time.Millisecond, func(context.Context) (bool, error) {
			return !exists(filepath.Join(r.Proc, strconv.Itoa(pid))), nil
		}) == nil
		if !gone {
			if err := r.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				errs = append(errs, err)
			}
		}
	}
	return len(strays), errors.Join(errs...)
}

func (r Reaper) strays() ([]int, error) {
	entries, err := os.ReadDir(r.Proc)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if r.isStray(filepath.Join(r.Proc, e.Name())) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func (r Reaper) isStray(dir string) bool {
	comm, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil || strings.TrimSpace(string(comm)) != "wpa_supplicant" {
		return false
	}
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil || !onWlan0(strings.Split(string(cmdline), "\x00")) {
		return false
	}
	cgroup, err := os.ReadFile(filepath.Join(dir, "cgroup"))
	return err == nil && !strings.HasSuffix(strings.TrimSpace(string(cgroup)), "/"+paths.SupplicantUnit)
}

func onWlan0(args []string) bool {
	for i, arg := range args {
		if arg == "-i"+paths.Iface || arg == "-i "+paths.Iface || arg == "-i" && i+1 < len(args) && args[i+1] == paths.Iface {
			return true
		}
	}
	return false
}
