// Package standin answers RaspAP's wpa_cli commands on wlan0's control
// socket while wlan0 is the hotspot, so RaspAP's WiFi page lists the saved
// networks instead of starting wpa_supplicant on the AP (docs/DESIGN.md §4).
package standin

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/raspapconf"
)

// Options configures a stand-in. Networks returns the saved networks, read
// fresh from RaspAP's file; Triggers receives the SSID of SELECT_NETWORK.
type Options struct {
	Path     string
	GID      int
	MAC      string
	Networks func() []raspapconf.Network
	Triggers chan<- string
}

// Server is one bound stand-in socket.
type Server struct {
	opts Options
	conn *net.UnixConn
	ino  uint64
	done chan struct{}

	mu      sync.Mutex
	scratch map[int]string // ADD_NETWORK entries: id → SSID, never written anywhere
}

// Start binds the socket (directory and socket 0770, group GID) and serves it.
func Start(opts Options) (*Server, error) {
	dir := filepath.Dir(opts.Path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0770); err != nil {
			return nil, err
		}
		if err := setGroupMode(dir, opts.GID); err != nil {
			return nil, err
		}
	}
	if err := RemoveStale(opts.Path); err != nil {
		return nil, err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: opts.Path, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(opts.Path)
	if err == nil {
		err = setGroupMode(opts.Path, opts.GID)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	s := &Server{opts: opts, conn: conn, ino: inode(info), done: make(chan struct{}), scratch: map[int]string{}}
	go s.serve()
	return s, nil
}

func setGroupMode(path string, gid int) error {
	if err := os.Chown(path, -1, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0770)
}

// RemoveStale deletes path only when it is a socket nobody serves; a
// missing path is fine.
func RemoveStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	probe, err := net.DialTimeout("unixgram", path, time.Second)
	if err == nil {
		probe.Close()
		return fmt.Errorf("%s is served by another process", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	return os.Remove(path)
}

func inode(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}

// Gone reports that the socket path was removed or replaced (RaspAP's
// "Reinitialize" deletes it), so the caller should rebind.
func (s *Server) Gone() bool {
	info, err := os.Lstat(s.opts.Path)
	return err != nil || inode(info) != s.ino
}

// Close stops serving and removes the path if it is still this socket.
func (s *Server) Close() error {
	err := s.conn.Close()
	<-s.done
	if !s.Gone() {
		err = errors.Join(err, os.Remove(s.opts.Path))
	}
	return err
}

func (s *Server) serve() {
	defer close(s.done)
	buf := make([]byte, 4096)
	for {
		n, addr, err := s.conn.ReadFromUnix(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if addr == nil {
			continue
		}
		_, _ = s.conn.WriteToUnix([]byte(s.reply(string(buf[:n]))), addr)
	}
}

const (
	ok      = "OK\n"
	fail    = "FAIL\n"
	unknown = "UNKNOWN COMMAND\n"
)

func (s *Server) reply(cmd string) string {
	name, args, _ := strings.Cut(cmd, " ")
	switch name {
	case "PING":
		return "PONG\n"
	case "STATUS":
		return "wpa_state=DISCONNECTED\naddress=" + s.opts.MAC + "\n"
	case "LIST_NETWORKS":
		return s.listNetworks()
	case "ADD_NETWORK":
		return s.addNetwork()
	case "SET_NETWORK":
		return s.setNetwork(args)
	case "REMOVE_NETWORK":
		s.mu.Lock()
		delete(s.scratch, atoiOr(args, -1))
		s.mu.Unlock()
		return ok
	case "SELECT_NETWORK":
		return s.selectNetwork(args)
	case "ENABLE_NETWORK", "DISABLE_NETWORK", "DISCONNECT", "REASSOCIATE", "SCAN", "RECONFIGURE", "TERMINATE":
		return ok
	case "SCAN_RESULTS":
		return "bssid / frequency / signal level / flags / ssid\n"
	}
	return unknown
}

func (s *Server) listNetworks() string {
	var b strings.Builder
	b.WriteString("network id / ssid / bssid / flags\n")
	for id, n := range s.opts.Networks() {
		flags := ""
		if n.Disabled {
			flags = "[DISABLED]"
		}
		fmt.Fprintf(&b, "%d\t%s\tany\t%s\n", id, raspapconf.EncodeText(n.SSID), flags)
	}
	return b.String()
}

func (s *Server) addNetwork() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := len(s.opts.Networks())
	for {
		if _, used := s.scratch[id]; !used {
			break
		}
		id++
	}
	s.scratch[id] = ""
	return strconv.Itoa(id) + "\n"
}

// setNetwork accepts every field but keeps only a scratch entry's SSID; a
// psk is dropped with the request buffer.
func (s *Server) setNetwork(args string) string {
	fields := strings.SplitN(args, " ", 3)
	if len(fields) != 3 {
		return fail
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := atoiOr(fields[0], -1)
	if _, isScratch := s.scratch[id]; !isScratch {
		if id < 0 || id >= len(s.opts.Networks()) {
			return fail
		}
		return ok
	}
	if fields[1] == "ssid" {
		ssid, valid := raspapconf.DecodeSSID(fields[2])
		if !valid {
			return fail
		}
		s.scratch[id] = ssid
	}
	return ok
}

func (s *Server) selectNetwork(args string) string {
	id := atoiOr(args, -1)
	ssid, found := s.ssidOf(id)
	if !found {
		return fail
	}
	select {
	case s.opts.Triggers <- ssid:
	default:
	}
	return ok
}

func (s *Server) ssidOf(id int) (string, bool) {
	s.mu.Lock()
	ssid, isScratch := s.scratch[id]
	s.mu.Unlock()
	if isScratch {
		return ssid, true
	}
	networks := s.opts.Networks()
	if id < 0 || id >= len(networks) {
		return "", false
	}
	return networks[id].SSID, true
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return n
}
