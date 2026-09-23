package whatsapp

import (
	"strings"
	"testing"
	"time"
)

func TestConnState(t *testing.T) {
	tests := []struct {
		name                                string
		connected, loggedOut, everConnected bool
		want                                string
	}{
		{"fresh process, never connected", false, false, false, StateUnknown},
		{"connected", true, false, true, StateConnected},
		{"dropped after a good connection", false, false, true, StateDisconnected},
		{"logged out wins over connected", true, true, true, StateLoggedOut},
		{"logged out while down", false, true, true, StateLoggedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connState(tt.connected, tt.loggedOut, tt.everConnected); got != tt.want {
				t.Errorf("connState(%v,%v,%v) = %q, want %q", tt.connected, tt.loggedOut, tt.everConnected, got, tt.want)
			}
		})
	}
}

func TestTruncateReason(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"short passes through", "boom", 4},
		{"exactly at the cap", strings.Repeat("a", maxReasonLen), maxReasonLen},
		{"over the cap is clipped", strings.Repeat("a", maxReasonLen+500), maxReasonLen},
		{"multibyte is clipped by rune", strings.Repeat("ש", maxReasonLen+10), maxReasonLen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateReason(tt.in)
			if len([]rune(got)) != tt.want {
				t.Errorf("truncateReason() length = %d runes, want %d", len([]rune(got)), tt.want)
			}
		})
	}
}

// TestStateTransitions covers the lifecycle the incident exposed: a disconnect
// must stamp disconnected_since, and a recovery must clear the whole outage.
func TestStateTransitions(t *testing.T) {
	c := &Client{startedAt: time.Now()}

	c.MarkConnected()
	if c.lastConnectedAt.IsZero() {
		t.Fatal("MarkConnected did not set lastConnectedAt")
	}
	if !c.disconnectedAt.IsZero() {
		t.Error("MarkConnected left disconnectedAt set")
	}

	c.MarkDisconnected()
	first := c.disconnectedAt
	if first.IsZero() {
		t.Fatal("MarkDisconnected did not set disconnectedAt")
	}

	// A second disconnect inside the same outage must not move the start time.
	c.MarkDisconnected()
	if !c.disconnectedAt.Equal(first) {
		t.Error("repeated MarkDisconnected moved the outage start")
	}

	c.recordAttempt(3, errTest{})
	if c.reconnectAttempts != 3 || c.reconnectFailure == "" {
		t.Fatalf("recordAttempt did not store state: attempts=%d reason=%q", c.reconnectAttempts, c.reconnectFailure)
	}

	c.MarkLoggedOut()
	if !c.loggedOut {
		t.Error("MarkLoggedOut did not set the flag")
	}

	c.MarkConnected()
	if !c.disconnectedAt.IsZero() {
		t.Error("recovery did not clear disconnectedAt")
	}
	if c.reconnectAttempts != 0 {
		t.Errorf("recovery left reconnectAttempts = %d, want 0", c.reconnectAttempts)
	}
	if c.reconnectFailure != "" {
		t.Errorf("recovery left reconnectFailure = %q, want empty", c.reconnectFailure)
	}
	if c.loggedOut {
		t.Error("recovery left loggedOut set")
	}
	if c.autoReconnectErrors != 0 {
		t.Errorf("recovery left autoReconnectErrors = %d, want 0", c.autoReconnectErrors)
	}
}

func TestMarkSuccessfulSendAndReceipt(t *testing.T) {
	c := &Client{}
	if !c.lastSuccessfulSend.IsZero() || !c.lastReceiptAt.IsZero() {
		t.Fatal("zero value client already has liveness timestamps")
	}
	c.MarkSuccessfulSend()
	c.MarkReceipt()
	if c.lastSuccessfulSend.IsZero() {
		t.Error("MarkSuccessfulSend did not record a timestamp")
	}
	if c.lastReceiptAt.IsZero() {
		t.Error("MarkReceipt did not record a timestamp")
	}
}

type errTest struct{}

func (errTest) Error() string { return "dial tcp [2a03::1]:443: connect: network is unreachable" }
