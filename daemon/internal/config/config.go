// Package config parses /etc/airhatch/airhatchd.conf (docs/API.md §4).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Config holds the daemon's timers and the captive switch.
type Config struct {
	Tick              time.Duration
	ObserveTimeout    time.Duration
	BootTimeout       time.Duration
	ConnectTimeout    time.Duration
	AssocTimeout      time.Duration
	DisconnectGrace   time.Duration
	RetryInterval     time.Duration
	RetryIntervalBusy time.Duration
	HandoffDelay      time.Duration
	OffHold           time.Duration
	BackoffMin        time.Duration
	BackoffMax        time.Duration
	APStepTimeout     time.Duration
	ScanTimeout       time.Duration
	Captive           bool
}

// Default is the configuration used when the file is missing.
func Default() Config {
	return Config{
		Tick:              time.Second,
		ObserveTimeout:    5 * time.Second,
		BootTimeout:       45 * time.Second,
		ConnectTimeout:    45 * time.Second,
		AssocTimeout:      30 * time.Second,
		DisconnectGrace:   time.Minute,
		RetryInterval:     5 * time.Minute,
		RetryIntervalBusy: 15 * time.Minute,
		HandoffDelay:      5 * time.Second,
		OffHold:           15 * time.Minute,
		BackoffMin:        5 * time.Second,
		BackoffMax:        2 * time.Minute,
		APStepTimeout:     10 * time.Second,
		ScanTimeout:       10 * time.Second,
		Captive:           true,
	}
}

// Load reads path; a missing file means Default.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	c, err := Parse(file)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads key=value lines over Default. Unknown or duplicate keys, bad
// values and values out of bounds are errors that name their line.
func Parse(r io.Reader) (Config, error) {
	c := Default()
	lines := map[string]int{}
	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		text := scanner.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return Config{}, fmt.Errorf("line %d: expected key=value", n)
		}
		if _, seen := lines[key]; seen {
			return Config{}, fmt.Errorf("line %d: duplicate key %q", n, key)
		}
		if err := c.set(key, value); err != nil {
			return Config{}, fmt.Errorf("line %d: %w", n, err)
		}
		lines[key] = n
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	for _, b := range c.bounds() {
		if b.ok {
			continue
		}
		line, set := lines[b.key]
		if !set {
			line = lines[b.other]
		}
		return Config{}, fmt.Errorf("line %d: %s must be %s", line, b.key, b.rule)
	}
	return c, nil
}

func (c *Config) durations() map[string]*time.Duration {
	return map[string]*time.Duration{
		"tick": &c.Tick, "observe_timeout": &c.ObserveTimeout, "boot_timeout": &c.BootTimeout,
		"connect_timeout": &c.ConnectTimeout, "assoc_timeout": &c.AssocTimeout,
		"disconnect_grace": &c.DisconnectGrace, "retry_interval": &c.RetryInterval,
		"retry_interval_busy": &c.RetryIntervalBusy, "handoff_delay": &c.HandoffDelay,
		"off_hold": &c.OffHold, "backoff_min": &c.BackoffMin, "backoff_max": &c.BackoffMax,
		"ap_step_timeout": &c.APStepTimeout, "scan_timeout": &c.ScanTimeout,
	}
}

func (c *Config) set(key, value string) error {
	if key == "captive" {
		switch value {
		case "true":
			c.Captive = true
		case "false":
			c.Captive = false
		default:
			return fmt.Errorf("captive: expected true or false, got %q", value)
		}
		return nil
	}
	field, ok := c.durations()[key]
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*field = d
	return nil
}

// bound is one range rule. other names the key a relative rule compares
// with, so an error can point at that line when key itself is a default.
type bound struct {
	key, other, rule string
	ok               bool
}

func within(d, lo, hi time.Duration) bool { return d >= lo && d <= hi }

func (c Config) bounds() []bound {
	return []bound{
		{"tick", "", "100ms-10s", within(c.Tick, 100*time.Millisecond, 10*time.Second)},
		{"observe_timeout", "", "1s-30s", within(c.ObserveTimeout, time.Second, 30*time.Second)},
		{"boot_timeout", "", "10s-10m", within(c.BootTimeout, 10*time.Second, 10*time.Minute)},
		{"connect_timeout", "", "10s-10m", within(c.ConnectTimeout, 10*time.Second, 10*time.Minute)},
		{"assoc_timeout", "connect_timeout", "5s-connect_timeout", within(c.AssocTimeout, 5*time.Second, c.ConnectTimeout)},
		{"disconnect_grace", "", "0s-1h", within(c.DisconnectGrace, 0, time.Hour)},
		{"retry_interval", "", "30s-24h", within(c.RetryInterval, 30*time.Second, 24*time.Hour)},
		{"retry_interval_busy", "retry_interval", "at least retry_interval", c.RetryIntervalBusy >= c.RetryInterval},
		{"handoff_delay", "", "0s-1m", within(c.HandoffDelay, 0, time.Minute)},
		{"off_hold", "", "0s-24h", within(c.OffHold, 0, 24*time.Hour)},
		{"backoff_max", "", "at most 1h", c.BackoffMax <= time.Hour},
		{"backoff_min", "backoff_max", "1s-backoff_max", within(c.BackoffMin, time.Second, c.BackoffMax)},
		{"ap_step_timeout", "", "1s-1m", within(c.APStepTimeout, time.Second, time.Minute)},
		{"scan_timeout", "", "0s-1m", within(c.ScanTimeout, 0, time.Minute)},
	}
}
