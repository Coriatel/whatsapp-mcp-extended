package whatsapp

import (
	"errors"
	"math/rand"
	"os"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow"
)

// defaultReconnectWindow bounds how long the supervisor keeps retrying before
// escalating to a process exit. Override with WA_RECONNECT_WINDOW (e.g. "15m").
const defaultReconnectWindow = 10 * time.Minute

// exitCodeReconnectExhausted is the exit status used when the window runs out,
// so an operator can tell this apart from a crash in `docker inspect`.
const exitCodeReconnectExhausted = 3

// backoffSchedule is the unjittered wait before attempt n+1 (n is 0-based).
// 5s, 10s, 20s, 40s, then capped at 60s.
var backoffSchedule = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second}

// backoffFor returns the base wait after the given 0-based attempt index.
func backoffFor(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(backoffSchedule) {
		return backoffSchedule[len(backoffSchedule)-1]
	}
	return backoffSchedule[attempt]
}

// jitterFor applies +/-20% jitter. r must be in [0,1); r=0.5 means no change.
func jitterFor(d time.Duration, r float64) time.Duration {
	factor := 0.8 + 0.4*r
	return time.Duration(float64(d) * factor)
}

// Supervisor outcomes.
const (
	outcomeConnected = "connected"
	outcomeLoggedOut = "logged_out"
	outcomeExhausted = "exhausted"
)

// supervisor is the bounded reconnect loop with every side effect injected, so
// the schedule and the exit conditions are testable without a network or a clock.
type supervisor struct {
	connect   func() error
	connected func() bool
	loggedOut func() bool
	sleep     func(time.Duration)
	now       func() time.Time
	rnd       func() float64
	window    time.Duration
	// onAttempt is called once per attempt with the 1-based attempt number, the
	// wait that will follow a failure, and the failure (nil on success).
	onAttempt func(attempt int, wait time.Duration, err error)
}

// run retries until connected, logged out, or the window elapses.
func (s supervisor) run() string {
	deadline := s.now().Add(s.window)
	for attempt := 0; ; attempt++ {
		if s.loggedOut() {
			return outcomeLoggedOut
		}
		if s.connected() {
			return outcomeConnected
		}
		err := s.connect()
		if err == nil || errors.Is(err, whatsmeow.ErrAlreadyConnected) {
			s.onAttempt(attempt+1, 0, nil)
			return outcomeConnected
		}
		if errors.Is(err, whatsmeow.ErrNotLoggedIn) {
			s.onAttempt(attempt+1, 0, err)
			return outcomeLoggedOut
		}
		wait := jitterFor(backoffFor(attempt), s.rnd())
		s.onAttempt(attempt+1, wait, err)
		if !s.now().Add(wait).Before(deadline) {
			return outcomeExhausted
		}
		s.sleep(wait)
	}
}

// keepAliveFailureThreshold is how many consecutive keepalive timeouts force a
// reconnect. whatsmeow keeps counting past this; we act once.
const keepAliveFailureThreshold = 3

// keepAliveShouldReconnect reports whether this timeout warrants a reconnect.
func keepAliveShouldReconnect(errCount int) bool {
	return errCount >= keepAliveFailureThreshold
}

// OnKeepAliveTimeout handles events.KeepAliveTimeout. This is the exact path
// that caused the 2026-09-23 two-hour outage, so it lives here under test
// rather than inline in main().
//
// Client.Disconnect() sets whatsmeow's expectedDisconnect flag, so whatsmeow
// emits no Disconnected event and starts no autoreconnect (client.go
// onDisconnect). The supervisor is the only thing that retries this path.
// MarkDisconnected is called here and not left to the supervisor because a
// second supervisor is a no-op, so a timeout arriving during an existing
// outage would otherwise never stamp disconnected_since.
func (c *Client) OnKeepAliveTimeout(errCount int) {
	if !keepAliveShouldReconnect(errCount) {
		return
	}
	c.logger.Errorf("KeepAlive: %d consecutive failures, forcing disconnect+reconnect", errCount)
	c.doDisconnect()
	c.MarkDisconnected()
	c.SuperviseReconnect("keepalive_timeout")
}

// doConnect and doDisconnect route through the test seams when set.
func (c *Client) doConnect() error {
	if c.connectFn != nil {
		return c.connectFn()
	}
	return c.Client.Connect()
}

func (c *Client) doDisconnect() {
	if c.disconnectFn != nil {
		c.disconnectFn()
		return
	}
	c.Client.Disconnect()
}

// hasDeviceID reports whether a paired device is still in the store. Once it is
// gone, reconnecting cannot succeed and retrying would only burn attempts.
func (c *Client) hasDeviceID() bool {
	return c.Client != nil && c.Client.Store != nil && c.Client.Store.ID != nil
}

// ReconnectWindow reads WA_RECONNECT_WINDOW, falling back to the default.
func ReconnectWindow() time.Duration {
	if v := os.Getenv("WA_RECONNECT_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultReconnectWindow
}

// ExitOnReconnectExhausted reports whether the process may exit itself when
// recovery fails. Opt out with WA_EXIT_ON_RECONNECT_EXHAUSTED=0, which disables
// both the supervisor's exit and main()'s watchdog.
func ExitOnReconnectExhausted() bool {
	if v := os.Getenv("WA_EXIT_ON_RECONNECT_EXHAUSTED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return true
}

// SuperviseReconnect starts the bounded reconnect loop in the background.
//
// It exists because whatsmeow's own autoreconnect does not cover every path: a
// disconnect the bridge itself forces (Client.Disconnect, e.g. the keepalive
// watchdog) sets expectedDisconnect, and whatsmeow then deliberately skips both
// the Disconnected event and autoReconnect (client.go onDisconnect). Before this
// supervisor, such a disconnect got exactly one reconnect attempt and, if that
// attempt failed, the bridge stayed down silently.
//
// Only one supervisor runs at a time; a second call while one is running is a
// no-op. It never clears the device store, so it can never trigger re-pairing.
func (c *Client) SuperviseReconnect(reason string) {
	c.connMu.Lock()
	if c.supervising {
		c.connMu.Unlock()
		return
	}
	c.supervising = true
	if c.disconnectedAt.IsZero() {
		c.disconnectedAt = time.Now()
	}
	c.connMu.Unlock()

	window := ReconnectWindow()
	c.logger.Warnf("RECONNECT_SUPERVISOR start (reason=%s, window=%v)", reason, window)

	go func() {
		defer func() {
			c.connMu.Lock()
			c.supervising = false
			c.connMu.Unlock()
		}()

		s := supervisor{
			connect:   c.doConnect,
			connected: c.IsConnected,
			loggedOut: func() bool {
				c.connMu.RLock()
				lo := c.loggedOut
				c.connMu.RUnlock()
				// A cleared device store is terminal in the same way a LoggedOut
				// event is: only re-pairing fixes it.
				return lo || !c.hasDeviceID()
			},
			sleep:  time.Sleep,
			now:    time.Now,
			rnd:    rand.Float64,
			window: window,
			onAttempt: func(attempt int, wait time.Duration, err error) {
				c.recordAttempt(attempt, err)
				if err == nil {
					c.logger.Infof("RECONNECT_ATTEMPT %d ok", attempt)
					return
				}
				c.logger.Warnf("RECONNECT_ATTEMPT %d failed, next in %v: %v", attempt, wait.Round(time.Second), err)
			},
		}

		switch s.run() {
		case outcomeConnected:
			c.logger.Infof("RECONNECT_SUPERVISOR recovered")
		case outcomeLoggedOut:
			c.MarkLoggedOut()
			c.logger.Errorf("RECONNECT_LOGGED_OUT: device session is invalid, re-pairing required (owner action)")
		case outcomeExhausted:
			c.logger.Errorf("RECONNECT_WINDOW_EXHAUSTED after %v (reason=%s)", window, reason)
			if ExitOnReconnectExhausted() {
				os.Exit(exitCodeReconnectExhausted)
			}
			c.logger.Errorf("RECONNECT_WINDOW_EXHAUSTED: exit disabled, bridge stays disconnected")
		}
	}()
}
