// Package atomicfile replaces files without exposing partially written contents.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Options is the metadata of the replacement. UID and GID of -1 keep the
// temporary file's owner (the caller). A zero Mode keeps the mode the
// filesystem gives new files, for filesystems such as vfat that refuse chmod.
type Options struct {
	Mode os.FileMode
	UID  int
	GID  int
}

// Preserve returns the owner and mode of the existing file at path.
func Preserve(path string) (Options, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Options{}, fmt.Errorf("stat %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Options{}, fmt.Errorf("stat %s: no owner information", path)
	}
	return Options{Mode: info.Mode().Perm(), UID: int(stat.Uid), GID: int(stat.Gid)}, nil
}

// Write syncs a temporary file in the same directory, then renames it over
// path and syncs the directory.
func Write(path string, data []byte, options Options) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".airhatch-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if options.UID != -1 || options.GID != -1 {
		if err := file.Chown(options.UID, options.GID); err != nil {
			return fmt.Errorf("set owner for %s: %w", path, err)
		}
	}
	if options.Mode != 0 {
		if err := file.Chmod(options.Mode); err != nil {
			return fmt.Errorf("set mode for %s: %w", path, err)
		}
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
