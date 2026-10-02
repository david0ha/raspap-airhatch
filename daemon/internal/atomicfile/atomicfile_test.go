package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new\n"), Options{Mode: 0600, UID: -1, GID: -1}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "new\n" || info.Mode().Perm() != 0600 {
		t.Fatalf("content %q mode %v", data, info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

func TestPreserveKeepsOwnerAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	opts, err := Preserve(path)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != 0640 || opts.UID != os.Getuid() || opts.GID != os.Getgid() {
		t.Fatalf("options %+v", opts)
	}
	if err := Write(path, []byte("x"), opts); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestZeroModeKeepsDefaultMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := Write(path, []byte("x"), Options{UID: -1, GID: -1}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("CreateTemp default mode expected, got %v", info.Mode())
	}
}

func TestWriteFailsWithoutDirectory(t *testing.T) {
	if err := Write(filepath.Join(t.TempDir(), "missing", "f"), nil, Options{Mode: 0644, UID: -1, GID: -1}); err == nil {
		t.Fatal("expected an error")
	}
}
