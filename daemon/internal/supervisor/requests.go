package supervisor

import (
	"context"
	"errors"

	"github.com/david0ha/raspap-airhatch/daemon/internal/publicerror"
	"github.com/david0ha/raspap-airhatch/daemon/internal/servicestart"
)

// ErrNotRunning means the loop did not take the request (it is stopping).
var ErrNotRunning = errors.New("supervisor is not running")

// do runs f on the loop goroutine and waits for it; the published status
// already reflects f when do returns.
func (s *Supervisor) do(ctx context.Context, f func()) error {
	done := make(chan struct{})
	request := func() {
		f()
		s.publish()
		close(done)
	}
	select {
	case s.requests <- request:
	case <-s.stopped:
		return ErrNotRunning
	case <-ctx.Done():
		return ErrNotRunning
	}
	<-done
	return nil
}

// OpenAP is the API "open_ap".
func (s *Supervisor) OpenAP(ctx context.Context) (string, error) {
	var msg string
	err := s.do(ctx, func() { s.state, msg = s.pol.OpenAP(s.state, s.clock.Now()) })
	return msg, err
}

// RetryClient is the API "retry_client".
func (s *Supervisor) RetryClient(ctx context.Context) (string, error) {
	var msg string
	var refused *publicerror.Error
	err := s.do(ctx, func() { s.state, msg, refused = s.pol.RetryClient(s.state, s.saved, s.clock.Now()) })
	if err != nil {
		return "", err
	}
	if refused != nil {
		return "", refused
	}
	return msg, nil
}

// Auto is the API "auto".
func (s *Supervisor) Auto(ctx context.Context) (string, error) {
	var msg string
	err := s.do(ctx, func() { s.state, msg = s.pol.Auto(s.state) })
	return msg, err
}

// ServiceStart is the API "servicestart": RaspAP's hotspot buttons.
func (s *Supervisor) ServiceStart(ctx context.Context, args []string) ([]string, error) {
	var line string
	err := s.do(ctx, func() {
		s.state, line = s.pol.ServiceStart(s.state, servicestart.Parse(args), s.clock.Now())
	})
	if err != nil {
		return nil, err
	}
	return []string{line}, nil
}
