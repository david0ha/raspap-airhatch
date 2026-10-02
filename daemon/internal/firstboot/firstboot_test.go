package firstboot

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/clock"
	"github.com/david0ha/raspap-airhatch/daemon/internal/paths"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

const hash = "$2y$10$Wq3cO6s1Jx0E1hQ2y0VbUeQ8m7a1gk2m3n4o5p6q7r8s9t0u1v2w3"

type rig struct {
	l     paths.Layout
	fake  *runner.Fake
	logs  *bytes.Buffer
	opts  Options
	clock *clock.Fake
}

func newRig(t *testing.T, steps ...runner.Step) *rig {
	t.Helper()
	root := t.TempDir()
	l := paths.Rooted(root)
	copyFile(t, "../../testdata/upstream/hostapd.conf", l.HostapdConf, 0644)
	copyFile(t, "../../testdata/upstream/dhcpcd.conf", l.RaspAPDhcpcd, 0644)
	writeFile(t, l.WlanMAC, "dc:a6:32:01:1a:2b\n", 0444)
	writeFile(t, l.Console, "", 0600)
	for _, dir := range []string{filepath.Dir(l.FirstbootDone), filepath.Dir(l.Notice), filepath.Dir(l.RaspAPAuth)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	r := &rig{l: l, fake: runner.NewFake(steps...), logs: &bytes.Buffer{}, clock: clock.NewFake(time.Unix(0, 0))}
	r.opts = Options{Layout: l, Runner: r.fake, Clock: r.clock, Log: slog.New(slog.NewTextHandler(r.logs, nil)),
		Random: seeded(), WebUID: os.Getuid(), WebGID: os.Getgid()}
	return r
}

func copyFile(t *testing.T, from, to string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, to, string(data), mode)
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// seeded is the deterministic randomness of a test run.
func seeded() io.Reader { return mrand.NewChaCha8([32]byte{7}) }

// expected is what a run with seeded() generates on the upstream files: the
// passphrase is drawn before the admin password.
func expected(t *testing.T) (passphrase, password string) {
	t.Helper()
	random := seeded()
	passphrase, err := secret(random, passphraseLength)
	if err != nil {
		t.Fatal(err)
	}
	if password, err = secret(random, adminLength); err != nil {
		t.Fatal(err)
	}
	return passphrase, password
}

func phpStep(t *testing.T, output string) runner.Step {
	_, password := expected(t)
	return runner.Step{Name: "php", Args: []string{"-r", hashScript}, Input: []byte(password + "\n"), Output: []byte(output)}
}

// field returns the value of a "Label: value" line of the notice.
func field(t *testing.T, notice, label string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(label) + `:\s+(\S+)$`).FindStringSubmatch(notice)
	if m == nil {
		t.Fatalf("no %s in notice:\n%s", label, notice)
	}
	return m[1]
}

func TestFirstBootOnUpstreamFiles(t *testing.T) {
	r := newRig(t, phpStep(t, hash))
	upstream := read(t, r.l.HostapdConf)
	if err := Run(context.Background(), r.opts); err != nil {
		t.Fatal(err)
	}
	notice := read(t, r.l.Notice)
	ssid, psk, password := field(t, notice, "Hotspot SSID"), field(t, notice, "Hotspot passphrase"), field(t, notice, "RaspAP admin password")
	if ssid != "RaspAP-1A2B" || field(t, notice, "RaspAP admin user") != "admin" || field(t, notice, "RaspAP on the hotspot") != "http://10.3.141.1/" {
		t.Fatalf("notice:\n%s", notice)
	}
	secretChars := regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789]+$`)
	if len(psk) != 12 || len(password) != 16 || !secretChars.MatchString(psk) || !secretChars.MatchString(password) {
		t.Fatalf("secrets %q %q", psk, password)
	}
	if wantPSK, wantPassword := expected(t); psk != wantPSK || password != wantPassword {
		t.Fatal("notice does not list the generated secrets")
	}

	want := strings.Replace(strings.Replace(upstream, "\nssid=RaspAP\n", "\nssid=RaspAP-1A2B\n", 1),
		"\nwpa_passphrase=ChangeMe\n", "\nwpa_passphrase="+psk+"\n", 1)
	if got := read(t, r.l.HostapdConf); got != want {
		t.Fatalf("hostapd.conf changed beyond the two lines:\n%s", got)
	}
	assertMode(t, r.l.HostapdConf, 0644)

	if got := read(t, r.l.RaspAPAuth); got != "admin\n"+hash+"\n0\n" {
		t.Fatalf("raspap.auth %q", got)
	}
	assertMode(t, r.l.RaspAPAuth, 0600)

	calls := r.fake.Calls()
	if len(calls) != 1 || string(calls[0].Input) != password+"\n" || strings.Contains(strings.Join(calls[0].Args, " "), password) {
		t.Fatalf("php must get the password on stdin only: %+v", calls)
	}
	if read(t, r.l.Console) != "\n"+notice+"\n" {
		t.Fatal("console did not get the notice")
	}
	logs := r.logs.String()
	for _, secret := range []string{psk, password} {
		if strings.Contains(logs, secret) {
			t.Fatal("secret in the log")
		}
	}
	if !strings.Contains(logs, "credentials written to /boot/raspap-airhatch.txt") {
		t.Fatal(logs)
	}
	if _, err := os.Stat(r.l.FirstbootDone); err != nil {
		t.Fatal("no marker")
	}
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode %v", path, info.Mode().Perm())
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
		t.Fatalf("%s owner %d:%d", path, st.Uid, st.Gid)
	}
}

func TestCustomValuesAndExistingAuthAreKept(t *testing.T) {
	r := newRig(t)
	custom := strings.Replace(strings.Replace(read(t, "../../testdata/upstream/hostapd.conf"), "ssid=RaspAP\n", "ssid=Workshop\n", 1),
		"wpa_passphrase=ChangeMe\n", "wpa_passphrase=correct horse battery\n", 1)
	writeFile(t, r.l.HostapdConf, custom, 0640)
	writeFile(t, r.l.RaspAPAuth, "admin\n$2y$10$existing\n0\n", 0600)
	if err := Run(context.Background(), r.opts); err != nil {
		t.Fatal(err)
	}
	if read(t, r.l.HostapdConf) != custom || read(t, r.l.RaspAPAuth) != "admin\n$2y$10$existing\n0\n" {
		t.Fatal("custom values changed")
	}
	assertMode(t, r.l.HostapdConf, 0640)
	if _, err := os.Stat(r.l.Notice); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a notice with nothing generated")
	}
	if _, err := os.Stat(r.l.FirstbootDone); err != nil {
		t.Fatal("no marker")
	}
}

func TestOnlyGeneratedValuesAreListed(t *testing.T) {
	r := newRig(t)
	writeFile(t, r.l.RaspAPAuth, "admin\n$2y$10$existing\n0\n", 0600)
	if err := Run(context.Background(), r.opts); err != nil {
		t.Fatal(err)
	}
	notice := read(t, r.l.Notice)
	if field(t, notice, "RaspAP admin user") != "unchanged" || field(t, notice, "RaspAP admin password") != "unchanged" {
		t.Fatal(notice)
	}
}

func TestMarkerIsLastAndARerunAppends(t *testing.T) {
	failed := phpStep(t, "")
	failed.Err = errors.New("php: not found")
	r := newRig(t, failed)
	if err := Run(context.Background(), r.opts); err == nil {
		t.Fatal("hash failure ignored")
	}
	if _, err := os.Stat(r.l.FirstbootDone); err == nil {
		t.Fatal("marker written after a failure")
	}
	if _, err := os.Stat(r.l.RaspAPAuth); err == nil {
		t.Fatal("auth written without a hash")
	}
	first := read(t, r.l.Notice)

	r.fake = runner.NewFake(phpStep(t, hash+"\n"))
	r.opts.Runner, r.opts.Random = r.fake, seeded()
	if err := Run(context.Background(), r.opts); err != nil {
		t.Fatal(err)
	}
	notice := read(t, r.l.Notice)
	if !strings.HasPrefix(notice, first+"\n") || strings.Count(notice, "first-boot credentials") != 2 {
		t.Fatalf("rerun must keep the earlier notice:\n%s", notice)
	}
	second := notice[len(first)+1:]
	if !strings.Contains(read(t, r.l.HostapdConf), "\nwpa_passphrase="+field(t, second, "Hotspot passphrase")+"\n") {
		t.Fatal("applied passphrase is not the latest one in the notice")
	}
	if read(t, r.l.RaspAPAuth) != "admin\n"+hash+"\n0\n" {
		t.Fatal("hash not trimmed")
	}
}

func TestDoneIsANoOp(t *testing.T) {
	r := newRig(t)
	writeFile(t, r.l.FirstbootDone, "", 0644)
	upstream := read(t, r.l.HostapdConf)
	if err := Run(context.Background(), r.opts); err != nil {
		t.Fatal(err)
	}
	if read(t, r.l.HostapdConf) != upstream {
		t.Fatal("ran after the marker")
	}
}

func TestBadHashIsRefused(t *testing.T) {
	r := newRig(t, phpStep(t, "Warning: something\n"))
	if err := Run(context.Background(), r.opts); err == nil {
		t.Fatal("accepted a non-bcrypt hash")
	}
}

func TestSuffixFallsBackToMachineID(t *testing.T) {
	r := newRig(t)
	if err := os.Remove(r.l.WlanMAC); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.l.MachineID, "0123456789abcdef0123456789abcdef\n", 0444)
	done := make(chan string)
	go func() { done <- suffix(context.Background(), r.opts) }()
	for range 20 {
		for r.clock.Waiters() == 0 {
			time.Sleep(time.Millisecond)
		}
		r.clock.Advance(500 * time.Millisecond)
	}
	// SHA-256 of the machine-id file's bytes.
	if got := <-done; got != "6ED6" {
		t.Fatal(got)
	}
}

func TestSecretsAreUniformOverTheAlphabet(t *testing.T) {
	counts := map[rune]int{}
	for range 200 {
		s, err := secret(rand.Reader, adminLength)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range s {
			counts[c]++
		}
	}
	if len(counts) != len(alphabet) {
		t.Fatalf("%d distinct characters", len(counts))
	}
}
