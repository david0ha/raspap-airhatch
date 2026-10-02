// Package wpactrl speaks the wpa_supplicant/hostapd control-socket protocol:
// unix datagrams from a client socket bound in a local directory.
package wpactrl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// defaultTimeout bounds a request whose context has no deadline, as
// wpa_ctrl_request does.
const defaultTimeout = 10 * time.Second

var counter atomic.Uint64

// Conn is one client socket connected to a control interface.
type Conn struct {
	conn  *net.UnixConn
	local string
}

// Dial binds a client socket in localDir and connects it to remote.
func Dial(remote, localDir string) (*Conn, error) {
	local := filepath.Join(localDir, fmt.Sprintf("wpa_ctrl_%d-%d", os.Getpid(), counter.Add(1)))
	_ = os.Remove(local)
	conn, err := net.DialUnix("unixgram",
		&net.UnixAddr{Name: local, Net: "unixgram"},
		&net.UnixAddr{Name: remote, Net: "unixgram"})
	if err != nil {
		_ = os.Remove(local)
		return nil, err
	}
	return &Conn{conn: conn, local: local}, nil
}

// Command is Dial, one Request and Close.
func Command(ctx context.Context, remote, localDir, cmd string) (string, error) {
	c, err := Dial(remote, localDir)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.Request(ctx, cmd)
}

// Request sends cmd and returns its reply, skipping unsolicited "<…>" events.
func (c *Conn) Request(ctx context.Context, cmd string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	if err := c.send(ctx, cmd); err != nil {
		return "", err
	}
	for {
		reply, err := c.Receive(ctx)
		if err != nil {
			return "", fmt.Errorf("%s: %w", cmd, err)
		}
		if !strings.HasPrefix(reply, "<") {
			return reply, nil
		}
	}
}

// Attach subscribes this socket to events; read them with Receive.
func (c *Conn) Attach(ctx context.Context) error {
	reply, err := c.Request(ctx, "ATTACH")
	if err != nil {
		return err
	}
	if reply != "OK\n" {
		return fmt.Errorf("ATTACH: %q", strings.TrimSpace(reply))
	}
	return nil
}

// Receive returns the next datagram, or the context's error.
func (c *Conn) Receive(ctx context.Context) (string, error) {
	stop := c.deadlineFrom(ctx)
	defer stop()
	buf := make([]byte, 8192)
	n, err := c.conn.Read(buf)
	if err != nil {
		return "", contextError(ctx, err)
	}
	return string(buf[:n]), nil
}

func (c *Conn) send(ctx context.Context, cmd string) error {
	stop := c.deadlineFrom(ctx)
	defer stop()
	if _, err := c.conn.Write([]byte(cmd)); err != nil {
		return contextError(ctx, err)
	}
	return nil
}

// contextError reports a socket deadline as the context error that set it.
// The socket can expire a moment before the context's own timer fires.
func contextError(ctx context.Context, err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

// deadlineFrom applies ctx's deadline to the socket and expires it at once
// when ctx is cancelled.
func (c *Conn) deadlineFrom(ctx context.Context) func() {
	deadline, _ := ctx.Deadline()
	_ = c.conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetDeadline(time.Now()) })
	return func() {
		stop()
		_ = c.conn.SetDeadline(time.Time{})
	}
}

// Close closes the socket and removes its path.
func (c *Conn) Close() error {
	return errors.Join(c.conn.Close(), os.Remove(c.local))
}
