// Package scan keeps RaspAP's nearby-network list useful on the hotspot: it
// scans before the AP opens and seeds RaspAP's scan cache with the result.
package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/atomicfile"
	"github.com/david0ha/raspap-airhatch/daemon/internal/runner"
)

// Run scans as a station and returns iw's raw output.
func Run(ctx context.Context, r runner.Runner, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := r.Run(ctx, "ip", "link", "set", "wlan0", "up"); err != nil {
		return nil, err
	}
	return r.Run(ctx, "iw", "dev", "wlan0", "scan")
}

// Seeder writes Data under RaspAP's cache key for the scan, which is
// nearby_wifi_stations_<mtime of wpa_supplicant.conf> (U:WiFiManager.php:83-104).
// While Dir exists RaspAP's writeCache never writes (U:includes/functions.php:455-462).
type Seeder struct {
	Dir      string
	WpaConf  string
	UID, GID int
	Data     []byte
	lastKey  string
}

// Refresh writes the key when the conf's mtime changed since the last write.
// A key RaspAP's "Rescan" deleted stays deleted until the next change.
func (s *Seeder) Refresh() error {
	info, err := os.Stat(s.WpaConf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	key := fmt.Sprintf("nearby_wifi_stations_%d", info.ModTime().Unix())
	if key == s.lastKey {
		return nil
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(s.Dir, key), s.Data, atomicfile.Options{Mode: 0644, UID: s.UID, GID: s.GID}); err != nil {
		return err
	}
	if s.lastKey != "" {
		_ = os.Remove(filepath.Join(s.Dir, s.lastKey))
	}
	s.lastKey = key
	return nil
}

func (s *Seeder) ensureDir() error {
	if _, err := os.Stat(s.Dir); err == nil {
		return nil
	}
	if err := os.Mkdir(s.Dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := os.Chown(s.Dir, s.UID, s.GID); err != nil {
		return err
	}
	return os.Chmod(s.Dir, 0755)
}
