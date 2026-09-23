package whatsapp

import (
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestBackoffFor(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, 5 * time.Second},
		{0, 5 * time.Second},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 60 * time.Second},
		{5, 60 * time.Second},
		{99, 60 * time.Second},
	}
	for _, tt := range tests {
		if got := backoffFor(tt.attempt); got != tt.want {
			t.Errorf("backoffFor(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestJitterForBounds(t *testing.T) {
	base := 20 * time.Second
	tests := []struct {
		name string
		r    float64
		want time.Duration
	}{
		{"floor is -20%", 0, 16 * time.Second},
		{"midpoint is unchanged", 0.5, 20 * time.Second},
		{"ceiling is +20%", 1, 24 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jitterFor(base, tt.r); got != tt.want {
				t.Errorf("jitterFor(%v, %v) = %v, want %v", base, tt.r, got, tt.want)
			}
		})
	}

	// Every value the real rand.Float64 can produce stays inside +/-20%.
	for _, d := range backoffSchedule {
		for _, r := range []float64{0, 0.01, 0.25, 0.5, 0.75, 0.999999} {
			got := jitterFor(d, r)
			if got < time.Duration(float64(d)*0.8) || got > time.Duration(float64(d)*1.2) {
				t.Errorf("jitterFor(%v, %v) = %v, outside +/-20%%", d, r, got)
			}
		}
	}
}

// fakeClock drives the supervisor without sleeping.
type fakeClock struct {
	now   time.Time
	slept []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
}

// newTestSupervisor wires a supervisor with no real clock, network or entropy.
func newTestSupervisor(clock *fakeClock, connect func() error, connected, loggedOut func() bool, window time.Duration) (supervisor, *[]int) {
	var attempts []int
	s := supervisor{
		connect:   connect,
		connected: connected,
		loggedOut: loggedOut,
		sleep:     clock.Sleep,
		now:       clock.Now,
		rnd:       func() float64 { return 0.5 }, // no jitter, deterministic
		window:    window,
		onAttempt: func(attempt int, wait time.Duration, err error) {
			attempts = append(attempts, attempt)
		},
	}
	return s, &attempts
}

func TestSupervisorStopsOnSuccess(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	connect := func() error {
		calls++
		if calls < 3 {
			return errors.New("dial failed")
		}
		return nil
	}
	s, attempts := newTestSupervisor(clock, connect, func() bool { return false }, func() bool { return false }, 10*time.Minute)

	if got := s.run(); got != outcomeConnected {
		t.Fatalf("run() = %q, want %q", got, outcomeConnected)
	}
	if calls != 3 {
		t.Errorf("connect called %d times, want 3", calls)
	}
	if len(*attempts) != 3 {
		t.Errorf("recorded %d attempts, want 3", len(*attempts))
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second}
	if len(clock.slept) != len(want) {
		t.Fatalf("slept %v, want %v", clock.slept, want)
	}
	for i := range want {
		if clock.slept[i] != want[i] {
			t.Errorf("sleep %d = %v, want %v", i, clock.slept[i], want[i])
		}
	}
}

func TestSupervisorAlreadyConnectedCountsAsSuccess(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	s, _ := newTestSupervisor(clock, func() error { return whatsmeow.ErrAlreadyConnected },
		func() bool { return false }, func() bool { return false }, 10*time.Minute)
	if got := s.run(); got != outcomeConnected {
		t.Fatalf("run() = %q, want %q", got, outcomeConnected)
	}
}

func TestSupervisorReturnsEarlyWhenSocketRestoredElsewhere(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	s, _ := newTestSupervisor(clock, func() error { calls++; return nil },
		func() bool { return true }, func() bool { return false }, 10*time.Minute)
	if got := s.run(); got != outcomeConnected {
		t.Fatalf("run() = %q, want %q", got, outcomeConnected)
	}
	if calls != 0 {
		t.Errorf("connect called %d times, want 0 (whatsmeow already reconnected)", calls)
	}
}

func TestSupervisorStopsOnWindowExhaustion(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	s, _ := newTestSupervisor(clock, func() error { calls++; return errors.New("network is unreachable") },
		func() bool { return false }, func() bool { return false }, 1*time.Minute)

	if got := s.run(); got != outcomeExhausted {
		t.Fatalf("run() = %q, want %q", got, outcomeExhausted)
	}
	// 5+10+20 = 35s slept; the next wait (40s) would pass the 60s deadline.
	if total := clock.now.Sub(time.Unix(0, 0)); total != 35*time.Second {
		t.Errorf("slept %v in total, want 35s", total)
	}
	if calls != 4 {
		t.Errorf("connect called %d times, want 4", calls)
	}
}

func TestSupervisorStopsOnLoggedOut(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	s, _ := newTestSupervisor(clock, func() error { calls++; return nil },
		func() bool { return false }, func() bool { return true }, 10*time.Minute)
	if got := s.run(); got != outcomeLoggedOut {
		t.Fatalf("run() = %q, want %q", got, outcomeLoggedOut)
	}
	if calls != 0 {
		t.Errorf("connect called %d times, want 0 (must never re-pair)", calls)
	}
}

func TestSupervisorTreatsNotLoggedInErrorAsLoggedOut(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	s, _ := newTestSupervisor(clock, func() error { return whatsmeow.ErrNotLoggedIn },
		func() bool { return false }, func() bool { return false }, 10*time.Minute)
	if got := s.run(); got != outcomeLoggedOut {
		t.Fatalf("run() = %q, want %q", got, outcomeLoggedOut)
	}
}

func TestSuperviseReconnectIsSingleFlight(t *testing.T) {
	c := &Client{}
	c.connMu.Lock()
	c.supervising = true
	c.connMu.Unlock()

	// A second supervisor must not start; with no whatsmeow client embedded, a
	// started goroutine would panic, so returning cleanly is the assertion.
	c.SuperviseReconnect("test")

	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if !c.supervising {
		t.Error("supervising flag was cleared by the second call")
	}
	if !c.disconnectedAt.IsZero() {
		t.Error("second call mutated disconnectedAt")
	}
}

func TestReconnectWindowEnv(t *testing.T) {
	tests := []struct {
		name, env string
		want      time.Duration
	}{
		{"unset uses default", "", defaultReconnectWindow},
		{"valid duration", "15m", 15 * time.Minute},
		{"garbage falls back", "not-a-duration", defaultReconnectWindow},
		{"zero falls back", "0s", defaultReconnectWindow},
		{"negative falls back", "-5m", defaultReconnectWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WA_RECONNECT_WINDOW", tt.env)
			if got := ReconnectWindow(); got != tt.want {
				t.Errorf("ReconnectWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExitOnExhaustedEnv(t *testing.T) {
	tests := []struct {
		name, env string
		want      bool
	}{
		{"unset defaults to exit", "", true},
		{"0 opts out", "0", false},
		{"false opts out", "false", false},
		{"1 opts in", "1", true},
		{"garbage keeps default", "maybe", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WA_EXIT_ON_RECONNECT_EXHAUSTED", tt.env)
			if got := ExitOnReconnectExhausted(); got != tt.want {
				t.Errorf("ExitOnReconnectExhausted() = %v, want %v", got, tt.want)
			}
		})
	}
}

// newPairedTestClient builds a Client that looks paired to the reconnect code
// but never touches the network: both side effects go through the seams.
func newPairedTestClient(t *testing.T) *Client {
	t.Helper()
	jid := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	return &Client{
		Client: &whatsmeow.Client{Store: &store.Device{ID: &jid}},
		logger: waLog.Noop,
	}
}

func TestKeepAliveShouldReconnect(t *testing.T) {
	tests := []struct {
		errCount int
		want     bool
	}{
		{0, false}, {1, false}, {2, false}, {3, true}, {4, true}, {99, true},
	}
	for _, tt := range tests {
		if got := keepAliveShouldReconnect(tt.errCount); got != tt.want {
			t.Errorf("keepAliveShouldReconnect(%d) = %v, want %v", tt.errCount, got, tt.want)
		}
	}
}

// TestOnKeepAliveTimeout pins the 2026-09-23 incident path. Reverting the
// MarkDisconnected or SuperviseReconnect wiring must fail here.
func TestOnKeepAliveTimeout(t *testing.T) {
	tests := []struct {
		name           string
		errCount       int
		wantDisconnect bool
		wantSupervisor bool
	}{
		{"one timeout is tolerated", 1, false, false},
		{"two timeouts are tolerated", 2, false, false},
		{"three timeouts force a reconnect", 3, true, true},
		{"more than three still reconnects", 7, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPairedTestClient(t)

			var disconnected bool
			c.disconnectFn = func() { disconnected = true }

			connectCalled := make(chan struct{})
			release := make(chan struct{})
			c.connectFn = func() error {
				close(connectCalled)
				<-release // hold the supervisor open so the assertions are not racy
				return nil
			}
			defer close(release)

			c.OnKeepAliveTimeout(tt.errCount)

			if disconnected != tt.wantDisconnect {
				t.Errorf("Disconnect called = %v, want %v", disconnected, tt.wantDisconnect)
			}

			if !tt.wantSupervisor {
				c.connMu.RLock()
				defer c.connMu.RUnlock()
				if !c.disconnectedAt.IsZero() {
					t.Error("outage was recorded for a tolerated timeout")
				}
				if c.supervising {
					t.Error("supervisor started for a tolerated timeout")
				}
				return
			}

			select {
			case <-connectCalled:
			case <-time.After(2 * time.Second):
				t.Fatal("supervisor never attempted a reconnect")
			}

			c.connMu.RLock()
			defer c.connMu.RUnlock()
			if c.disconnectedAt.IsZero() {
				t.Error("MarkDisconnected was not called, so the outage stays invisible to /api/health")
			}
			if !c.supervising {
				t.Error("supervisor is not running")
			}
		})
	}
}

// TestSupervisorStopsWhenDeviceStoreIsCleared covers the post-logout guard: a
// client with no device ID must not keep dialling.
// TestKeepAliveTimeoutDuringAnExistingOutage pins MarkDisconnected: a second
// supervisor is a no-op, so without the explicit call disconnected_since would
// never be stamped for a timeout that arrives mid-outage.
func TestKeepAliveTimeoutDuringAnExistingOutage(t *testing.T) {
	c := newPairedTestClient(t)
	c.disconnectFn = func() {}
	c.connectFn = func() error { return nil }

	// A supervisor is already running and the outage has not been stamped.
	c.connMu.Lock()
	c.supervising = true
	c.connMu.Unlock()

	c.OnKeepAliveTimeout(3)

	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.disconnectedAt.IsZero() {
		t.Error("MarkDisconnected was not called, so disconnected_since stays empty during the outage")
	}
}

// TestSupervisorStopsWhenDeviceStoreIsCleared covers the post-logout guard: a
// client with no device ID must not keep dialling.
func TestSupervisorStopsWhenDeviceStoreIsCleared(t *testing.T) {
	c := &Client{Client: &whatsmeow.Client{Store: &store.Device{}}, logger: waLog.Noop}
	if c.hasDeviceID() {
		t.Fatal("hasDeviceID true for a store with no ID")
	}
	c.connectFn = func() error {
		t.Error("supervisor dialled with no paired device")
		return nil
	}
	c.disconnectFn = func() {}
	c.OnKeepAliveTimeout(3)

	deadline := time.After(2 * time.Second)
	for {
		c.connMu.RLock()
		done := !c.supervising
		lo := c.loggedOut
		c.connMu.RUnlock()
		if done {
			if !lo {
				t.Error("supervisor finished without marking the session logged out")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("supervisor did not finish")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestHasDeviceID(t *testing.T) {
	jid := types.JID{User: "1", Server: types.DefaultUserServer}
	tests := []struct {
		name string
		c    *Client
		want bool
	}{
		{"no whatsmeow client", &Client{}, false},
		{"no store", &Client{Client: &whatsmeow.Client{}}, false},
		{"store with no id", &Client{Client: &whatsmeow.Client{Store: &store.Device{}}}, false},
		{"paired", &Client{Client: &whatsmeow.Client{Store: &store.Device{ID: &jid}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.hasDeviceID(); got != tt.want {
				t.Errorf("hasDeviceID() = %v, want %v", got, tt.want)
			}
		})
	}
}
