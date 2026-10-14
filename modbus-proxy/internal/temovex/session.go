package temovex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Session keeps one EXOline-TCP connection for the process lifetime and
// redials after a transport failure. Requests are serialized: the controller
// exchange is one frame out, then one frame in.
type Session struct {
	address     string
	dialTimeout time.Duration
	readTimeout time.Duration

	mu     chan struct{}
	client *Client
}

// NewSession dials address lazily on the first read.
// dialTimeout bounds the TCP connect. readTimeout bounds each frame exchange
// when the caller's context has no sooner deadline.
func NewSession(address string, dialTimeout, readTimeout time.Duration) *Session {
	return &Session{
		address:     address,
		dialTimeout: dialTimeout,
		readTimeout: readTimeout,
		mu:          make(chan struct{}, 1),
	}
}

// Close drops the current connection. A later read dials again.
func (s *Session) Close() error {
	if err := s.lock(context.Background()); err != nil {
		return err
	}
	defer s.unlock()
	s.closeLocked()
	return nil
}

// Temperature reads one exporter temperature.
func (s *Session) Temperature(ctx context.Context, name string) (Reading, error) {
	if _, ok := lookup(name); !ok {
		return Reading{}, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	var reading Reading
	err := s.call(ctx, func(c *Client) error {
		got, err := c.Temperature(ctx, name)
		if err != nil {
			return err
		}
		reading = got
		return nil
	})
	return reading, err
}

// Temperatures reads setpoint, exhaust, extract, outdoor, and supply air,
// in that order.
func (s *Session) Temperatures(ctx context.Context) ([]Reading, error) {
	var readings []Reading
	err := s.call(ctx, func(c *Client) error {
		got, err := c.Temperatures(ctx)
		if err != nil {
			return err
		}
		readings = got
		return nil
	})
	return readings, err
}

// call runs fn on the open connection and, after a transport failure, dials
// once more. A controller error is a complete answer, so it is not retried.
func (s *Session) call(ctx context.Context, fn func(*Client) error) error {
	err := s.attempt(ctx, fn)
	if err == nil || !retryable(ctx, err) {
		return err
	}
	return s.attempt(ctx, fn)
}

func retryable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var controller *ControllerError
	if errors.As(err, &controller) {
		return false
	}
	if errors.Is(err, ErrUnknown) {
		return false
	}
	return true
}

func (s *Session) attempt(ctx context.Context, fn func(*Client) error) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if s.client == nil {
		if err := s.dial(ctx); err != nil {
			return err
		}
	}
	if err := fn(s.client); err != nil {
		var controller *ControllerError
		if !errors.As(err, &controller) {
			s.closeLocked()
		}
		return err
	}
	return nil
}

func (s *Session) dial(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", s.address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", s.address, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	s.client = &Client{
		Conn:        conn,
		Reader:      bufio.NewReader(conn),
		readTimeout: s.readTimeout,
	}
	return nil
}

func (s *Session) closeLocked() {
	if s.client == nil {
		return
	}
	if s.client.Conn != nil {
		_ = s.client.Conn.Close()
	}
	s.client = nil
}

// mu is a one-permit semaphore so a cancelled request can stop waiting
// instead of blocking behind an in-flight exchange.
func (s *Session) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.mu <- struct{}{}:
		return nil
	}
}

func (s *Session) unlock() {
	<-s.mu
}
