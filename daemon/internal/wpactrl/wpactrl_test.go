package wpactrl

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// server is a scripted control socket: it answers each request with the
// replies the test queues for that command.
type server struct {
	conn    *net.UnixConn
	path    string
	replies map[string][]string
	got     chan string
}

func newServer(t *testing.T, replies map[string][]string) *server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wlan0")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{conn: conn, path: path, replies: replies, got: make(chan string, 16)}
	go s.serve()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *server) serve() {
	buf := make([]byte, 4096)
	for {
		n, addr, err := s.conn.ReadFromUnix(buf)
		if err != nil {
			return
		}
		cmd := string(buf[:n])
		s.got <- cmd
		for _, reply := range s.replies[cmd] {
			_, _ = s.conn.WriteToUnix([]byte(reply), addr)
		}
	}
}

func TestRequestSkipsUnsolicitedMessages(t *testing.T) {
	s := newServer(t, map[string][]string{"STATUS": {"<3>CTRL-EVENT-SCAN-STARTED ", "wpa_state=COMPLETED\nssid=Home\n"}})
	local := t.TempDir()
	c, err := Dial(s.path, local)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := c.Request(context.Background(), "STATUS")
	if err != nil || reply != "wpa_state=COMPLETED\nssid=Home\n" {
		t.Fatalf("%q %v", reply, err)
	}
	entries, _ := os.ReadDir(local)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "wpa_ctrl_") {
		t.Fatalf("local socket %v", entries)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(local); len(entries) != 0 {
		t.Fatalf("local socket left behind: %v", entries)
	}
}

func TestAttachAndReceiveEvents(t *testing.T) {
	s := newServer(t, map[string][]string{"ATTACH": {"OK\n", "<3>CTRL-EVENT-SSID-TEMP-DISABLED id=0 ssid=\"Home\" auth_failures=1 duration=10 reason=WRONG_KEY"}})
	c, err := Dial(s.path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Attach(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := c.Receive(ctx)
	if err != nil || !strings.Contains(event, "reason=WRONG_KEY") {
		t.Fatalf("%q %v", event, err)
	}
}

func TestAttachRejected(t *testing.T) {
	s := newServer(t, map[string][]string{"ATTACH": {"FAIL\n"}})
	c, err := Dial(s.path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Attach(context.Background()); err == nil {
		t.Fatal("FAIL accepted")
	}
}

func TestRequestHonoursContext(t *testing.T) {
	s := newServer(t, nil)
	c, err := Dial(s.path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Request(ctx, "PING"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel2)
	if _, err := c.Request(ctx2, "PING"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestDialFailsWithoutServer(t *testing.T) {
	local := t.TempDir()
	if _, err := Dial(filepath.Join(t.TempDir(), "absent"), local); err == nil {
		t.Fatal("dial succeeded")
	}
	if entries, _ := os.ReadDir(local); len(entries) != 0 {
		t.Fatalf("local socket left behind: %v", entries)
	}
}

func TestCommandHelper(t *testing.T) {
	s := newServer(t, map[string][]string{"PING": {"PONG\n"}})
	reply, err := Command(context.Background(), s.path, t.TempDir(), "PING")
	if err != nil || reply != "PONG\n" {
		t.Fatalf("%q %v", reply, err)
	}
}
