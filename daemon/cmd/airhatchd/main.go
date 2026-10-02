// Command airhatchd owns wlan0's mode on a RaspAP device: Wi-Fi client
// first, the RaspAP hotspot with a captive portal as the fallback.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/api"
	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/config"
	"github.com/david0ha/raspap-airhatch/daemon/internal/dhcpcdrender"
	"github.com/david0ha/raspap-airhatch/daemon/internal/firstboot"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/radio"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
	"github.com/david0ha/raspap-airhatch/daemon/internal/servicestart"
	"github.com/david0ha/raspap-airhatch/daemon/internal/supervisor"
)

const (
	commandTimeout = time.Minute
	maxOutput      = 1 << 20
	callTimeout    = 15 * time.Second
	cleanupTimeout = 2 * time.Minute
	usage          = "usage: airhatchd run|cleanup|firstboot|render-dhcpcd|status|open-ap|retry|auto|servicestart [args...]"
)

// app is the CLI with its environment, so tests can relocate it.
type app struct {
	layout paths.Layout
	stdout io.Writer
	stderr io.Writer
	log    *slog.Logger
	cmds   runner.InputRunner
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	a := app{layout: paths.Default(), stdout: os.Stdout, stderr: os.Stderr,
		log: slog.New(slog.NewTextHandler(os.Stderr, nil)), cmds: runner.NewExec(commandTimeout, maxOutput)}
	os.Exit(a.main(ctx, os.Args[1:]))
}

func (a app) main(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.stderr, usage)
		return 2
	}
	switch command, rest := args[0], args[1:]; command {
	case "run":
		return a.run(ctx)
	case "cleanup":
		return a.cleanup()
	case "firstboot":
		return a.firstboot(ctx)
	case "render-dhcpcd":
		if err := dhcpcdrender.Write(a.layout.RaspAPDhcpcd, a.layout.ModeFile, a.layout.DhcpcdConf); err != nil {
			fmt.Fprintln(a.stderr, "airhatchd render-dhcpcd:", err)
			return 1
		}
		return 0
	case "status":
		return a.status(ctx)
	case "open-ap":
		return a.message(ctx, "open_ap")
	case "retry":
		return a.message(ctx, "retry_client")
	case "auto":
		return a.message(ctx, "auto")
	case "servicestart":
		return a.servicestart(ctx, rest)
	}
	fmt.Fprintln(a.stderr, usage)
	return 2
}

func (a app) run(ctx context.Context) int {
	cfg, err := config.Load(a.layout.DaemonConf)
	if err != nil {
		fmt.Fprintf(a.stderr, "airhatchd: %s: %v\n", a.layout.DaemonConf, err)
		return 2
	}
	if err := os.MkdirAll(a.layout.RunDir, 0755); err != nil {
		a.log.Error("create run directory", "error", err)
		return 1
	}
	selected := make(chan string, 1)
	var sup *supervisor.Supervisor
	r := a.radio(cfg, func() bool { return sup.AfterFailedHandoff() }, selected)
	sup = supervisor.New(supervisor.Options{Config: cfg, Radio: r, Clock: clock.Real{}, Log: a.log,
		Selected: selected, CredentialsFile: a.layout.Notice})
	server, err := api.Listen(api.Options{Path: a.layout.APISock, GID: paths.WebGID, Roles: api.DefaultRoles(),
		Backend: backend{sup}, Log: a.log})
	if err != nil {
		a.log.Error("API socket", "error", err)
		return 1
	}
	serveCtx, stopServing := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(serveCtx) }()
	err = sup.Run(ctx)
	stopServing()
	if serveErr := <-served; serveErr != nil {
		a.log.Error("API socket", "error", serveErr)
	}
	if err != nil {
		a.log.Error("shutdown", "error", err)
	}
	return 0
}

func (a app) radio(cfg config.Config, afterFailedHandoff func() bool, selected chan<- string) *radio.Radio {
	return radio.New(radio.Options{
		Layout: a.layout, Config: cfg, Runner: a.cmds, Clock: clock.Real{}, Log: a.log,
		Reaper: radio.Reaper{Proc: a.layout.Proc, Kill: radio.SystemKill, Clock: clock.Real{}},
		Addrs:  radio.SystemAddrs, Listen: radio.SystemListen,
		AfterFailedHandoff: afterFailedHandoff,
		Selected:           selected, NetdevGID: a.netdevGID(), WebUID: paths.WebUID, WebGID: paths.WebGID,
	})
}

// cleanup is airhatchd.service's ExecStopPost: whether airhatchd stopped or
// crashed, an AP it left behind is closed so nothing serves wlan0 while it
// is down (docs/DESIGN.md §5.4). Its closing steps use no daemon timers, so
// it runs on the defaults even when the daemon config is broken.
func (a app) cleanup() int {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := a.radio(config.Default(), func() bool { return false }, nil).Cleanup(ctx); err != nil {
		a.log.Error("cleanup", "error", err)
		return 1
	}
	return 0
}

// netdevGID is the group of the stand-in socket, like the real supplicant's.
func (a app) netdevGID() int {
	group, err := user.LookupGroup("netdev")
	if err == nil {
		if gid, err := strconv.Atoi(group.Gid); err == nil {
			return gid
		}
	}
	a.log.Warn("netdev group not found; the stand-in socket is root-only", "error", err)
	return 0
}

// backend adapts the supervisor to the API.
type backend struct{ *supervisor.Supervisor }

func (b backend) Status() any { return b.Supervisor.Status() }

func (a app) firstboot(ctx context.Context) int {
	err := firstboot.Run(ctx, firstboot.Options{
		Layout: a.layout, Runner: a.cmds, Clock: clock.Real{}, Log: a.log,
		Random: rand.Reader, WebUID: paths.WebUID, WebGID: paths.WebGID,
	})
	if err != nil {
		a.log.Error("first boot", "error", err)
		return 1
	}
	return 0
}

func (a app) call(ctx context.Context, req api.Request) (api.Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return api.Call(ctx, a.layout.APISock, req)
}

func (a app) status(ctx context.Context) int {
	reply, err := a.call(ctx, api.Request{Op: "status"})
	if err != nil {
		fmt.Fprintln(a.stderr, servicestart.Unreachable)
		return 1
	}
	if !reply.OK {
		return a.failed(reply)
	}
	var indented any
	if err := json.Unmarshal(reply.Status, &indented); err != nil {
		fmt.Fprintln(a.stderr, "airhatchd: bad status:", err)
		return 1
	}
	out, _ := json.MarshalIndent(indented, "", "  ")
	fmt.Fprintln(a.stdout, string(out))
	return 0
}

func (a app) message(ctx context.Context, op string) int {
	reply, err := a.call(ctx, api.Request{Op: op})
	if err != nil {
		fmt.Fprintln(a.stderr, servicestart.Unreachable)
		return 1
	}
	if !reply.OK {
		return a.failed(reply)
	}
	fmt.Fprintln(a.stdout, reply.Message)
	return 0
}

func (a app) servicestart(ctx context.Context, args []string) int {
	reply, err := a.call(ctx, api.Request{Op: "servicestart", Args: args})
	if err != nil {
		fmt.Fprintln(a.stdout, servicestart.Unreachable)
		return 1
	}
	if !reply.OK {
		return a.failed(reply)
	}
	for _, line := range reply.Lines {
		fmt.Fprintln(a.stdout, line)
	}
	return 0
}

func (a app) failed(reply api.Reply) int {
	if reply.Error == nil {
		reply.Error = &api.ErrorBody{Code: api.CodeInternal, Message: "airhatchd returned no error detail."}
	}
	fmt.Fprintf(a.stderr, "airhatch: %s (%s)\n", reply.Error.Message, reply.Error.Code)
	return 1
}
