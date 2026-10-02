package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

func TestRunBringsLinkUpThenScans(t *testing.T) {
	fake := runner.NewFake(
		runner.Step{Name: "ip", Args: []string{"link", "set", "wlan0", "up"}},
		runner.Step{Name: "iw", Args: []string{"dev", "wlan0", "scan"}, Output: []byte("BSS 00:11:22:33:44:55(on wlan0)\n\tSSID: Home\n")},
	)
	out, err := Run(context.Background(), fake, time.Second)
	if err != nil || string(out) != "BSS 00:11:22:33:44:55(on wlan0)\n\tSSID: Home\n" {
		t.Fatalf("%q %v", out, err)
	}
	failing := runner.NewFake(
		runner.Step{Name: "ip", Args: []string{"link", "set", "wlan0", "up"}},
		runner.Step{Name: "iw", Args: []string{"dev", "wlan0", "scan"}, Err: errors.New("busy")},
	)
	if _, err := Run(context.Background(), failing, time.Second); err == nil {
		t.Fatal("scan error swallowed")
	}
}

func TestSeedWritesKeyOfConfMtime(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "wpa_supplicant.conf")
	cache := filepath.Join(dir, "raspap")
	if err := os.WriteFile(conf, nil, 0600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1759300000, 0)
	_ = os.Chtimes(conf, mtime, mtime)
	s := &Seeder{Dir: cache, WpaConf: conf, UID: os.Getuid(), GID: os.Getgid(), Data: []byte("scan text")}
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(cache, "nearby_wifi_stations_1759300000")
	data, err := os.ReadFile(first)
	if err != nil || string(data) != "scan text" {
		t.Fatalf("%q %v", data, err)
	}
	dirInfo, _ := os.Stat(cache)
	fileInfo, _ := os.Stat(first)
	if dirInfo.Mode().Perm() != 0755 || fileInfo.Mode().Perm() != 0644 {
		t.Fatalf("modes %v %v", dirInfo.Mode(), fileInfo.Mode())
	}

	// RaspAP's Rescan deletes the key; the seed is not rewritten for the same mtime.
	_ = os.Remove(first)
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("seed rewritten after Rescan")
	}

	// A save changes the mtime: the new key is written and the old one dropped.
	_ = os.WriteFile(first, []byte("old"), 0644)
	later := mtime.Add(time.Minute)
	_ = os.Chtimes(conf, later, later)
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(cache, fmt.Sprintf("nearby_wifi_stations_%d", later.Unix()))
	if data, _ := os.ReadFile(second); string(data) != "scan text" {
		t.Fatalf("new key %q", data)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("old key kept")
	}
}

func TestSeedSkipsMissingConf(t *testing.T) {
	dir := t.TempDir()
	s := &Seeder{Dir: filepath.Join(dir, "raspap"), WpaConf: filepath.Join(dir, "absent"), UID: -1, GID: -1}
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatal("cache dir created without a conf")
	}
}
