package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShippedFileParsesToDefault(t *testing.T) {
	c, err := Load("../../airhatchd.conf")
	if err != nil {
		t.Fatal(err)
	}
	if c != Default() {
		t.Fatalf("shipped config %+v differs from Default() %+v", c, Default())
	}
	data, err := os.ReadFile("../../airhatchd.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys() {
		if !strings.Contains(string(data), "\n"+key+"=") {
			t.Errorf("shipped file does not list %s", key)
		}
	}
}

func TestDefaultsMatchSpec(t *testing.T) {
	c := Default()
	want := Config{
		Tick: time.Second, ObserveTimeout: 5 * time.Second, BootTimeout: 45 * time.Second,
		ConnectTimeout: 45 * time.Second, AssocTimeout: 30 * time.Second, DisconnectGrace: time.Minute,
		RetryInterval: 5 * time.Minute, RetryIntervalBusy: 15 * time.Minute, HandoffDelay: 5 * time.Second,
		OffHold: 15 * time.Minute, BackoffMin: 5 * time.Second, BackoffMax: 2 * time.Minute,
		APStepTimeout: 10 * time.Second, ScanTimeout: 10 * time.Second, Captive: true,
	}
	if c != want {
		t.Fatalf("Default() = %+v", c)
	}
}

func TestMissingFileMeansDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil || c != Default() {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestParseOverrides(t *testing.T) {
	c, err := Parse(strings.NewReader("# comment\n\nretry_interval=1m\ncaptive=false\ndisconnect_grace=0s\nscan_timeout=0s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.RetryInterval != time.Minute || c.Captive || c.DisconnectGrace != 0 || c.ScanTimeout != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestParseErrorsNameTheLine(t *testing.T) {
	cases := map[string]string{
		"unknown":            "tick=1s\nbogus=1\n",
		"duplicate":          "tick=1s\ntick=2s\n",
		"spaces":             "tick = 1s\n",
		"no equals":          "tick\n",
		"bad duration":       "tick=fast\n",
		"bad bool":           "captive=yes\n",
		"tick low":           "tick=50ms\n",
		"tick high":          "tick=11s\n",
		"observe low":        "observe_timeout=500ms\n",
		"observe high":       "observe_timeout=31s\n",
		"boot low":           "boot_timeout=9s\n",
		"boot high":          "boot_timeout=11m\n",
		"connect low":        "connect_timeout=9s\n",
		"connect high":       "connect_timeout=11m\n",
		"assoc low":          "assoc_timeout=4s\n",
		"assoc over connect": "connect_timeout=20s\nassoc_timeout=25s\n",
		"grace negative":     "disconnect_grace=-1s\n",
		"grace high":         "disconnect_grace=61m\n",
		"retry low":          "retry_interval=29s\n",
		"retry high":         "retry_interval=25h\n",
		"busy below retry":   "retry_interval=10m\nretry_interval_busy=9m\n",
		"handoff high":       "handoff_delay=61s\n",
		"hold high":          "off_hold=25h\n",
		"backoff min low":    "backoff_min=500ms\n",
		"backoff min high":   "backoff_min=3m\n",
		"backoff max high":   "backoff_max=61m\n",
		"step low":           "ap_step_timeout=500ms\n",
		"step high":          "ap_step_timeout=61s\n",
		"scan high":          "scan_timeout=61s\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(text))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "line ") {
				t.Fatalf("error does not name a line: %v", err)
			}
		})
	}
}

func TestCrossBoundUsesBoundKeyDefault(t *testing.T) {
	// assoc_timeout is not in the file, so the error names connect_timeout's line.
	_, err := Parse(strings.NewReader("tick=1s\nconnect_timeout=20s\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("got %v", err)
	}
}

func TestBoundsAcceptEdges(t *testing.T) {
	text := "tick=100ms\nobserve_timeout=30s\nboot_timeout=10m\nconnect_timeout=10s\nassoc_timeout=5s\n" +
		"disconnect_grace=1h\nretry_interval=24h\nretry_interval_busy=24h\nhandoff_delay=1m\noff_hold=0s\n" +
		"backoff_min=1h\nbackoff_max=1h\nap_step_timeout=1m\nscan_timeout=1m\n"
	if _, err := Parse(strings.NewReader(text)); err != nil {
		t.Fatal(err)
	}
}

// keys are every key of docs/API.md §4, in its order.
func keys() []string {
	return []string{"tick", "observe_timeout", "boot_timeout", "connect_timeout", "assoc_timeout",
		"disconnect_grace", "retry_interval", "retry_interval_busy", "handoff_delay", "off_hold",
		"backoff_min", "backoff_max", "ap_step_timeout", "scan_timeout", "captive"}
}
