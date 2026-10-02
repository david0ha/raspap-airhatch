package raspapconf

import (
	"os"
	"path/filepath"
	"testing"
)

// The format RaspAP writes (U:includes/admin.php:97-136): user, hash, the
// non-privileged bit.
func TestReadAuth(t *testing.T) {
	// One level under the system temp directory, so its mode is the only one
	// that matters (t.TempDir nests in a 0700 directory).
	dir, err := os.MkdirTemp("", "auth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "raspap.auth")
	hash := "$2y$10$abcdefghijklmnopqrstuuJ9gU7j5bm4bvZB5ZQWyJw8U0nJ0j0aW"
	if err := os.WriteFile(path, []byte("admin\n"+hash+"\n0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	if got, err := ReadAuth(path, uid, gid); err != nil || got != hash {
		t.Fatalf("%q %v", got, err)
	}
	// Group-readable is enough for a member of the file's group.
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuth(path, uid+1, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuth(path, uid+1, gid+1); err == nil {
		t.Fatal("read for a user outside owner and group")
	}
}
