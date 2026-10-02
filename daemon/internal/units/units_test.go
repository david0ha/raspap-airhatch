package units

import (
	"context"
	"errors"
	"testing"

	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

func TestVerbsRunSystemctl(t *testing.T) {
	fake := runner.NewFake(
		runner.Step{Name: "systemctl", Args: []string{"start", "a.service"}},
		runner.Step{Name: "systemctl", Args: []string{"stop", "a.service"}},
		runner.Step{Name: "systemctl", Args: []string{"restart", "a.service"}},
		runner.Step{Name: "systemctl", Args: []string{"restart", "b.service"}, Err: errors.New("exit 1")},
	)
	u := Units{Runner: fake}
	ctx := context.Background()
	if u.Start(ctx, "a.service") != nil || u.Stop(ctx, "a.service") != nil || u.Restart(ctx, "a.service") != nil {
		t.Fatal("unexpected error")
	}
	if u.Restart(ctx, "b.service") == nil {
		t.Fatal("error swallowed")
	}
	if fake.Remaining() != 0 {
		t.Fatal("steps left")
	}
}

func TestIsActive(t *testing.T) {
	fake := runner.NewFake(
		runner.Step{Name: "systemctl", Args: []string{"is-active", "a.service"}, Output: []byte("active\n")},
		runner.Step{Name: "systemctl", Args: []string{"is-active", "a.service"}, Output: []byte("inactive\n"), Err: errors.New("exit 3")},
	)
	u := Units{Runner: fake}
	if !u.IsActive(context.Background(), "a.service") || u.IsActive(context.Background(), "a.service") {
		t.Fatal("is-active misread")
	}
}

func TestShow(t *testing.T) {
	fake := runner.NewFake(
		runner.Step{Name: "systemctl", Args: []string{"show", "-p", "ActiveState,Result", "hostapd.service"}, Output: []byte("Result=exit-code\nActiveState=failed\n")},
		runner.Step{Name: "systemctl", Args: []string{"show", "-p", "ActiveState,Result", "hostapd.service"}, Err: errors.New("no bus")},
	)
	u := Units{Runner: fake}
	state, err := u.Show(context.Background(), "hostapd.service")
	if err != nil || state != (State{Active: "failed", Result: "exit-code"}) {
		t.Fatalf("%+v %v", state, err)
	}
	if _, err := u.Show(context.Background(), "hostapd.service"); err == nil {
		t.Fatal("error swallowed")
	}
}

func TestStopSeveralUnits(t *testing.T) {
	fake := runner.NewFake(runner.Step{Name: "systemctl", Args: []string{"stop", "hostapd.service", "hostapd@*.service"}})
	if err := (Units{Runner: fake}).Stop(context.Background(), "hostapd.service", "hostapd@*.service"); err != nil || fake.Remaining() != 0 {
		t.Fatalf("%v", err)
	}
}

func TestAnyRunning(t *testing.T) {
	list := []string{"list-units", "--plain", "--no-legend", "--state=active,activating,deactivating,reloading", "hostapd.service", "hostapd@*.service"}
	fake := runner.NewFake(
		runner.Step{Name: "systemctl", Args: list},
		runner.Step{Name: "systemctl", Args: list, Output: []byte("hostapd@wlan0.service loaded deactivating stop-sigterm AP\n")},
		runner.Step{Name: "systemctl", Args: list, Err: errors.New("no bus")},
	)
	u := Units{Runner: fake}
	for i, want := range []bool{false, true} {
		if got, err := u.AnyRunning(context.Background(), "hostapd.service", "hostapd@*.service"); err != nil || got != want {
			t.Fatalf("call %d: %v %v", i, got, err)
		}
	}
	if _, err := u.AnyRunning(context.Background(), "hostapd.service", "hostapd@*.service"); err == nil {
		t.Fatal("error swallowed")
	}
}
