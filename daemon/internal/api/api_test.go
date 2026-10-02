package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
)

type fakeBackend struct {
	mu    sync.Mutex
	calls []string
	saved int
	fail  error
}

func (f *fakeBackend) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeBackend) Status() any { return map[string]string{"state": "ap"} }

func (f *fakeBackend) OpenAP(context.Context) (string, error) {
	f.record("open_ap")
	return "Opening the hotspot.", f.fail
}

func (f *fakeBackend) RetryClient(context.Context) (string, error) {
	f.record("retry_client")
	if f.saved == 0 {
		return "", publicerror.New(publicerror.NoSavedNetwork, "")
	}
	return "Trying the saved Wi-Fi networks again.", nil
}

func (f *fakeBackend) Auto(context.Context) (string, error) {
	f.record("auto")
	return "Back to automatic.", nil
}

func (f *fakeBackend) ServiceStart(_ context.Context, args []string) ([]string, error) {
	f.record("servicestart " + strings.Join(args, " "))
	return []string{"airhatch: opening the hotspot."}, nil
}

func serve(t *testing.T, roles map[uint32]Role, backend Backend) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "api.sock")
	srv, err := Listen(Options{Path: path, GID: os.Getgid(), Roles: roles, Backend: backend,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return path
}

func me(role Role) map[uint32]Role { return map[uint32]Role{uint32(os.Getuid()): role} }

// raw sends bytes and returns the reply line.
func raw(t *testing.T, path, request string) string {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(reply)
}

func call(t *testing.T, path string, req Request) Reply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := Call(ctx, path, req)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func errorCode(r Reply) string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

func TestSocketMode(t *testing.T) {
	path := serve(t, me(RoleRoot), &fakeBackend{})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0660 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestUIDMatrix(t *testing.T) {
	ops := []Request{{Op: "status"}, {Op: "open_ap"}, {Op: "retry_client"}, {Op: "auto"},
		{Op: "servicestart", Args: []string{"--interface", "wlan0", "--seconds", "1"}}}
	for _, c := range []struct {
		name    string
		roles   map[uint32]Role
		allowed map[string]bool
	}{
		{"root", me(RoleRoot), map[string]bool{"status": true, "open_ap": true, "retry_client": true, "auto": true, "servicestart": true}},
		{"www-data", me(RoleWeb), map[string]bool{"status": true, "open_ap": true, "retry_client": true, "auto": true}},
		{"other", map[uint32]Role{}, map[string]bool{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := serve(t, c.roles, &fakeBackend{saved: 1})
			for _, req := range ops {
				reply := call(t, path, req)
				if reply.OK != c.allowed[req.Op] {
					t.Errorf("%s: ok=%v error=%+v", req.Op, reply.OK, reply.Error)
				}
				if !reply.OK && errorCode(reply) != CodeForbidden {
					t.Errorf("%s: code %s", req.Op, errorCode(reply))
				}
			}
		})
	}
}

func TestReplies(t *testing.T) {
	backend := &fakeBackend{saved: 1}
	path := serve(t, me(RoleRoot), backend)
	if got := raw(t, path, `{"op":"status"}`+"\n"); got != `{"ok":true,"message":"","status":{"state":"ap"}}`+"\n" {
		t.Fatalf("%q", got)
	}
	if got := raw(t, path, `{"op":"open_ap"}`+"\n"); got != `{"ok":true,"message":"Opening the hotspot.","status":{"state":"ap"}}`+"\n" {
		t.Fatalf("%q", got)
	}
	got := raw(t, path, `{"op":"servicestart","args":["--interface","wlan0","--seconds","1"]}`+"\n")
	if got != `{"ok":true,"lines":["airhatch: opening the hotspot."]}`+"\n" {
		t.Fatalf("%q", got)
	}
	if strings.Join(backend.calls, ",") != "open_ap,servicestart --interface wlan0 --seconds 1" {
		t.Fatal(backend.calls)
	}
}

func TestErrorCodes(t *testing.T) {
	backend := &fakeBackend{}
	path := serve(t, me(RoleRoot), backend)
	cases := map[string]string{
		"not json":       "hello\n",
		"unknown field":  `{"op":"status","verbose":true}` + "\n",
		"wrong type":     `{"op":5}` + "\n",
		"missing op":     `{}` + "\n",
		"two values":     `{"op":"status"} {"op":"auto"}` + "\n",
		"no newline":     `{"op":"status"}`,
		"oversize":       `{"op":"status","args":["` + strings.Repeat("a", 4100) + `"]}` + "\n",
		"unknown op":     `{"op":"reboot"}` + "\n",
		"nothing saved":  `{"op":"retry_client"}` + "\n",
		"backend failed": `{"op":"open_ap"}` + "\n",
	}
	want := map[string]string{"unknown op": CodeUnknownOp, "nothing saved": CodeNoSavedNetwork, "backend failed": CodeInternal}
	backend.fail = errors.New("supervisor is not running")
	for name, request := range cases {
		line := raw(t, path, request)
		var reply Reply
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("%s: %q", name, line)
		}
		code := want[name]
		if code == "" {
			code = CodeBadRequest
		}
		if reply.OK || errorCode(reply) != code || reply.Error.Message == "" {
			t.Errorf("%s: %s", name, line)
		}
	}
	nothing := raw(t, path, `{"op":"retry_client"}`+"\n")
	if !strings.Contains(nothing, publicerror.New(publicerror.NoSavedNetwork, "").Message) {
		t.Fatal(nothing)
	}
}

func TestExactly4096BytesIsAccepted(t *testing.T) {
	path := serve(t, me(RoleRoot), &fakeBackend{})
	prefix := `{"op":"status","args":["`
	suffix := `"]}` + "\n"
	request := prefix + strings.Repeat("a", maxRequest-len(prefix)-len(suffix)) + suffix
	if len(request) != maxRequest {
		t.Fatal(len(request))
	}
	if got := raw(t, path, request); !strings.HasPrefix(got, `{"ok":true`) {
		t.Fatal(got)
	}
}

func TestSlowClientTimesOut(t *testing.T) {
	path := serve(t, me(RoleRoot), &fakeBackend{})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(`{"op":`))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	reply, _ := io.ReadAll(conn)
	if !bytes.Contains(reply, []byte(CodeBadRequest)) || time.Since(start) > 4*time.Second {
		t.Fatalf("%q after %v", reply, time.Since(start))
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	path := serve(t, me(RoleRoot), &fakeBackend{})
	srv, err := Listen(Options{Path: path, GID: os.Getgid(), Roles: me(RoleRoot), Backend: &fakeBackend{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	srv.listener.Close()
}
