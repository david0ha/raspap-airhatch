// Package firstboot replaces RaspAP's published default logins with
// per-device secrets once (docs/DESIGN.md §8).
package firstboot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/atomicfile"
	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

const (
	alphabet         = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	adminLength      = 16
	passphraseLength = 12
	adminUser        = "admin"
	defaultSSID      = "ssid=RaspAP"
	defaultPSK       = "wpa_passphrase=ChangeMe"
	macWait          = 10 * time.Second
	hashScript       = `echo password_hash(rtrim(fgets(STDIN)), PASSWORD_BCRYPT);`
)

var bcrypt = regexp.MustCompile(`^\$2y\$[^\s]+$`)

// Options are first boot's dependencies.
type Options struct {
	Layout paths.Layout
	Runner runner.InputRunner
	Clock  clock.Clock
	Log    *slog.Logger
	Random io.Reader
	WebUID int
	WebGID int
}

// generated holds the secrets this run applies; "" means unchanged.
type generated struct {
	ssid, passphrase, password string
}

func (g generated) any() bool { return g.ssid != "" || g.passphrase != "" || g.password != "" }

// Run does every step under the lock. It is idempotent: a step whose target
// is no longer the upstream default is skipped.
func Run(ctx context.Context, o Options) error {
	unlock, err := lock(o.Layout.FirstbootLock)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(o.Layout.FirstbootDone); err == nil {
		return nil
	}
	hostapd, err := os.ReadFile(o.Layout.HostapdConf)
	if err != nil {
		return err
	}
	g, err := plan(ctx, o, hostapd)
	if err != nil {
		return err
	}
	// The notice is written before any secret is applied: a crash in
	// between must not leave a secret that nobody can read.
	if g.any() {
		if err := notify(o, g); err != nil {
			return err
		}
	}
	if g.password != "" {
		if err := writeAuth(ctx, o, g.password); err != nil {
			return err
		}
	}
	if g.ssid != "" || g.passphrase != "" {
		if err := writeHostapd(o.Layout.HostapdConf, hostapd, g); err != nil {
			return err
		}
	}
	return atomicfile.Write(o.Layout.FirstbootDone, nil, atomicfile.Options{Mode: 0644, UID: -1, GID: -1})
}

func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// plan picks what to generate: only defaults that are still in place.
func plan(ctx context.Context, o Options, hostapd []byte) (generated, error) {
	var g generated
	lines := strings.Split(string(hostapd), "\n")
	var err error
	if hasLine(lines, defaultSSID) {
		g.ssid = "RaspAP-" + suffix(ctx, o)
	}
	if hasLine(lines, defaultPSK) {
		if g.passphrase, err = secret(o.Random, passphraseLength); err != nil {
			return g, err
		}
	}
	if _, err := os.Stat(o.Layout.RaspAPAuth); errors.Is(err, os.ErrNotExist) {
		if g.password, err = secret(o.Random, adminLength); err != nil {
			return g, err
		}
	}
	return g, nil
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// suffix is the last two bytes of wlan0's MAC, waiting for the interface,
// else the start of SHA-256(machine-id).
func suffix(ctx context.Context, o Options) string {
	var mac net.HardwareAddr
	_ = clock.PollUntil(ctx, o.Clock, macWait, 500*time.Millisecond, func(context.Context) (bool, error) {
		data, err := os.ReadFile(o.Layout.WlanMAC)
		if err != nil {
			return false, nil
		}
		mac, err = net.ParseMAC(strings.TrimSpace(string(data)))
		return err == nil && len(mac) >= 2, nil
	})
	if len(mac) >= 2 {
		return strings.ToUpper(hex.EncodeToString(mac[len(mac)-2:]))
	}
	id, _ := os.ReadFile(o.Layout.MachineID)
	sum := sha256.Sum256(id)
	return strings.ToUpper(hex.EncodeToString(sum[:2]))
}

// secret draws n characters uniformly from alphabet.
func secret(random io.Reader, n int) (string, error) {
	out := make([]byte, n)
	size := big.NewInt(int64(len(alphabet)))
	for i := range out {
		k, err := rand.Int(random, size)
		if err != nil {
			return "", fmt.Errorf("random: %w", err)
		}
		out[i] = alphabet[k.Int64()]
	}
	return string(out), nil
}

func writeAuth(ctx context.Context, o Options, password string) error {
	out, err := o.Runner.RunInput(ctx, []byte(password+"\n"), "php", "-r", hashScript)
	if err != nil {
		return fmt.Errorf("hash the admin password: %w", err)
	}
	hash := strings.TrimSpace(string(out))
	if !bcrypt.MatchString(hash) {
		return errors.New("hash the admin password: php did not return a bcrypt hash")
	}
	if err := os.MkdirAll(filepath.Dir(o.Layout.RaspAPAuth), 0755); err != nil {
		return err
	}
	content := adminUser + "\n" + hash + "\n0\n"
	return atomicfile.Write(o.Layout.RaspAPAuth, []byte(content), atomicfile.Options{Mode: 0600, UID: o.WebUID, GID: o.WebGID})
}

func writeHostapd(path string, original []byte, g generated) error {
	keep, err := atomicfile.Preserve(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(original), "\n")
	for i, l := range lines {
		switch {
		case l == defaultSSID && g.ssid != "":
			lines[i] = "ssid=" + g.ssid
		case l == defaultPSK && g.passphrase != "":
			lines[i] = "wpa_passphrase=" + g.passphrase
		}
	}
	return atomicfile.Write(path, []byte(strings.Join(lines, "\n")), keep)
}

// notify writes the notice to /boot and the console. A notice left by an
// interrupted earlier run is kept and the new values are appended.
func notify(o Options, g generated) error {
	text := noticeText(o.Layout, g)
	previous, err := os.ReadFile(o.Layout.Notice)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(previous) > 0 {
		text = string(previous) + "\n" + text
	}
	// /boot is vfat: no owner or mode to set.
	if err := atomicfile.Write(o.Layout.Notice, []byte(text), atomicfile.Options{UID: -1, GID: -1}); err != nil {
		return err
	}
	o.Log.Info("credentials written to " + paths.Default().Notice)
	if console, err := os.OpenFile(o.Layout.Console, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_, _ = console.WriteString("\n" + text + "\n")
		console.Close()
	}
	return nil
}

func noticeText(l paths.Layout, g generated) string {
	value := func(v string) string {
		if v == "" {
			return "unchanged"
		}
		return v
	}
	user := "unchanged"
	if g.password != "" {
		user = adminUser
	}
	var b strings.Builder
	b.WriteString("raspap-airhatch first-boot credentials\n\n")
	fmt.Fprintf(&b, "Hotspot SSID:          %s\n", value(g.ssid))
	fmt.Fprintf(&b, "Hotspot passphrase:    %s\n", value(g.passphrase))
	fmt.Fprintf(&b, "RaspAP admin user:     %s\n", user)
	fmt.Fprintf(&b, "RaspAP admin password: %s\n", value(g.password))
	if dhcpcd, err := os.ReadFile(l.RaspAPDhcpcd); err == nil {
		if prefix, ok := raspapconf.APAddress(dhcpcd); ok {
			fmt.Fprintf(&b, "RaspAP on the hotspot: http://%s/\n", prefix.Addr())
		}
	}
	b.WriteString("\nChange these in RaspAP (Authentication, Hotspot) and delete this file.\n")
	return b.String()
}
