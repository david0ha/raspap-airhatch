// Package api serves airhatchd's control socket: one NDJSON request and one
// reply per connection, callers identified by SO_PEERCRED (docs/API.md §3).
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
)

const (
	maxRequest  = 4096
	drainLimit  = 1 << 20
	readTimeout = 2 * time.Second
	opTimeout   = 10 * time.Second
)

// Error codes of the socket API.
const (
	CodeBadRequest     = "bad_request"
	CodeUnknownOp      = "unknown_op"
	CodeForbidden      = "forbidden"
	CodeNoSavedNetwork = "no_saved_network"
	CodeInternal       = "internal"
)

// Role is what a peer uid may do.
type Role int

const (
	// RoleWeb may call every op except servicestart.
	RoleWeb Role = iota + 1
	// RoleRoot may call every op.
	RoleRoot
)

// DefaultRoles maps root and www-data.
func DefaultRoles() map[uint32]Role { return map[uint32]Role{0: RoleRoot, 33: RoleWeb} }

// Backend carries out the ops.
type Backend interface {
	// Status is marshalled as the reply's "status".
	Status() any
	OpenAP(ctx context.Context) (string, error)
	RetryClient(ctx context.Context) (string, error)
	Auto(ctx context.Context) (string, error)
	ServiceStart(ctx context.Context, args []string) ([]string, error)
}

// Request is one API request.
type Request struct {
	Op   string   `json:"op"`
	Args []string `json:"args,omitempty"`
}

// ErrorBody is a failed reply's "error".
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Reply is any reply, as the client decodes it.
type Reply struct {
	OK      bool            `json:"ok"`
	Message string          `json:"message"`
	Status  json.RawMessage `json:"status,omitempty"`
	Lines   []string        `json:"lines,omitempty"`
	Error   *ErrorBody      `json:"error,omitempty"`
}

type okReply struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Status  any    `json:"status"`
}

type linesReply struct {
	OK    bool     `json:"ok"`
	Lines []string `json:"lines"`
}

type errorReply struct {
	OK    bool      `json:"ok"`
	Error ErrorBody `json:"error"`
}

func failure(code, message string) errorReply {
	return errorReply{Error: ErrorBody{Code: code, Message: message}}
}

// Options configure the server.
type Options struct {
	Path    string
	GID     int
	Roles   map[uint32]Role
	Backend Backend
	Log     *slog.Logger
}

// Server owns the listening socket.
type Server struct {
	opts     Options
	listener *net.UnixListener
}

// Listen binds the socket (mode 0660, group GID), replacing a previous one.
func Listen(o Options) (*Server, error) {
	if err := os.Remove(o.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: o.Path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chown(o.Path, -1, o.GID); err != nil {
		listener.Close()
		return nil, fmt.Errorf("own API socket: %w", err)
	}
	if err := os.Chmod(o.Path, 0660); err != nil {
		listener.Close()
		return nil, fmt.Errorf("protect API socket: %w", err)
	}
	return &Server{opts: o, listener: listener}, nil
}

// Serve answers connections until ctx ends, then waits for open ones.
func (s *Server) Serve(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := context.AfterFunc(ctx, func() { s.listener.Close() })
	defer stop()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(ctx context.Context, conn *net.UnixConn) {
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	role := s.opts.Roles[peerUID(conn)]
	line, readErr := readLine(conn)
	var reply any
	switch {
	case role == 0:
		reply = failure(CodeForbidden, "This user may not use airhatchd.")
	case readErr != nil:
		reply = failure(CodeBadRequest, readErr.Error())
	default:
		opCtx, cancel := context.WithTimeout(ctx, opTimeout)
		reply = s.dispatch(opCtx, role, line)
		cancel()
	}
	data, err := json.Marshal(reply)
	if err != nil {
		data, _ = json.Marshal(failure(CodeInternal, "The reply could not be encoded."))
	}
	_ = conn.SetWriteDeadline(time.Now().Add(readTimeout))
	if _, err := conn.Write(append(data, '\n')); err != nil {
		s.opts.Log.Warn("API reply", "error", err)
		return
	}
	// Unread input (an oversize request) would make the close reset the
	// connection before the client reads the reply, so the rest is drained.
	_ = conn.CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, drainLimit))
}

// readLine reads one request line of at most maxRequest bytes.
func readLine(conn io.Reader) ([]byte, error) {
	line, err := bufio.NewReader(io.LimitReader(conn, maxRequest+1)).ReadBytes('\n')
	switch {
	case len(line) > maxRequest:
		return nil, fmt.Errorf("request larger than %d bytes", maxRequest)
	case err != nil:
		return nil, errors.New("request must be one JSON line")
	}
	return line, nil
}

func peerUID(conn *net.UnixConn) uint32 {
	raw, err := conn.SyscallConn()
	if err != nil {
		return noUID
	}
	uid := uint32(noUID)
	_ = raw.Control(func(fd uintptr) {
		if cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED); err == nil {
			uid = cred.Uid
		}
	})
	return uid
}

// noUID is never in a role map: the kernel's overflow uid.
const noUID = 65534

func decode(line []byte) (Request, error) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("malformed request: %v", err)
	}
	if dec.More() {
		return Request{}, errors.New("malformed request: more than one JSON value")
	}
	if req.Op == "" {
		return Request{}, errors.New("missing op")
	}
	return req, nil
}

func (s *Server) dispatch(ctx context.Context, role Role, line []byte) any {
	req, err := decode(line)
	if err != nil {
		return failure(CodeBadRequest, err.Error())
	}
	b := s.opts.Backend
	var message string
	switch req.Op {
	case "status":
	case "open_ap":
		message, err = b.OpenAP(ctx)
	case "retry_client":
		message, err = b.RetryClient(ctx)
	case "auto":
		message, err = b.Auto(ctx)
	case "servicestart":
		if role != RoleRoot {
			return failure(CodeForbidden, "Only root may start or stop the hotspot this way.")
		}
		lines, err := b.ServiceStart(ctx, req.Args)
		if err != nil {
			return s.internal(err)
		}
		return linesReply{OK: true, Lines: lines}
	default:
		return failure(CodeUnknownOp, fmt.Sprintf("Unknown op %q.", req.Op))
	}
	if public := publicerror.As(err); public != nil && public.Code == publicerror.NoSavedNetwork {
		return failure(CodeNoSavedNetwork, public.Message)
	}
	if err != nil {
		return s.internal(err)
	}
	return okReply{OK: true, Message: message, Status: b.Status()}
}

func (s *Server) internal(err error) errorReply {
	s.opts.Log.Warn("API request failed", "error", err)
	return failure(CodeInternal, "airhatchd could not handle the request.")
}

// Call sends one request to the socket at path and returns the reply.
func Call(ctx context.Context, path string, req Request) (Reply, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return Reply{}, err
	}
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return Reply{}, fmt.Errorf("read reply: %w", err)
	}
	return reply, nil
}
