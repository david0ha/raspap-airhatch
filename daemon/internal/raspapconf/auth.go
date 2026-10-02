package raspapconf

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ReadAuth returns the admin hash of /etc/raspap/raspap.auth as RaspAP's
// PHP reads it as uid:gid (www-data). The file must be a regular file that
// uid:gid can read through searchable directories, with a user line and a
// hash line; otherwise PHP falls back to the built-in admin/secret
// (U:src/RaspAP/Auth/HTTPAuth.php:102-110).
func ReadAuth(path string, uid, gid int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a regular file", path)
	}
	if !permits(info, uid, gid, 4) {
		return "", fmt.Errorf("%s: not readable by %d:%d", path, uid, gid)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			return "", err
		}
		if !permits(info, uid, gid, 1) {
			return "", fmt.Errorf("%s: not searchable by %d:%d", dir, uid, gid)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	user, rest, _ := strings.Cut(string(data), "\n")
	hash, _, _ := strings.Cut(rest, "\n")
	if strings.TrimSpace(user) == "" || strings.TrimSpace(hash) == "" {
		return "", errors.New(path + ": no admin user and hash")
	}
	return strings.TrimSpace(hash), nil
}

// permits checks one permission bit (4 read, 1 search) for uid:gid, by the
// owner, group or other class that applies to them.
func permits(info os.FileInfo, uid, gid int, bit os.FileMode) bool {
	perm := info.Mode().Perm()
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !ok:
		return false
	case int(st.Uid) == uid:
		return perm>>6&bit != 0
	case int(st.Gid) == gid:
		return perm>>3&bit != 0
	}
	return perm&bit != 0
}
