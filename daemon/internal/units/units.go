// Package units drives systemd units through systemctl.
package units

import (
	"context"
	"strings"

	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

// Units runs systemctl through a runner.
type Units struct {
	Runner runner.Runner
}

// State is a unit's ActiveState and Result.
type State struct {
	Active string
	Result string
}

func (u Units) verb(ctx context.Context, verb, unit string) error {
	_, err := u.Runner.Run(ctx, "systemctl", verb, unit)
	return err
}

// Start runs systemctl start.
func (u Units) Start(ctx context.Context, unit string) error { return u.verb(ctx, "start", unit) }

// Stop runs systemctl stop; units may be glob patterns of loaded units.
func (u Units) Stop(ctx context.Context, units ...string) error {
	_, err := u.Runner.Run(ctx, "systemctl", append([]string{"stop"}, units...)...)
	return err
}

// Restart runs systemctl restart.
func (u Units) Restart(ctx context.Context, unit string) error { return u.verb(ctx, "restart", unit) }

// IsActive reports systemctl is-active; any failure counts as inactive.
func (u Units) IsActive(ctx context.Context, unit string) bool {
	return u.verb(ctx, "is-active", unit) == nil
}

// AnyRunning reports whether a unit matching one of the names or glob
// patterns is active, activating, deactivating or reloading.
func (u Units) AnyRunning(ctx context.Context, units ...string) (bool, error) {
	args := append([]string{"list-units", "--plain", "--no-legend", "--state=active,activating,deactivating,reloading"}, units...)
	out, err := u.Runner.Run(ctx, "systemctl", args...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// Show reads ActiveState and Result.
func (u Units) Show(ctx context.Context, unit string) (State, error) {
	out, err := u.Runner.Run(ctx, "systemctl", "show", "-p", "ActiveState,Result", unit)
	if err != nil {
		return State{}, err
	}
	var s State
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ActiveState":
			s.Active = value
		case "Result":
			s.Result = value
		}
	}
	return s, nil
}
