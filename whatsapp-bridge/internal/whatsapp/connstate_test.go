package whatsapp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
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

func TestClassifyFailure(t *testing.T) {
	opErr := func(inner error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: inner}
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil is empty", nil, ""},
		{"the incident error", opErr(&os.SyscallError{Syscall: "connect", Err: syscall.ENETUNREACH}), FailureDialUnreachable},
		{"host unreachable", opErr(&os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}), FailureDialUnreachable},
		{"connection refused", opErr(&os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}), FailureDialUnreachable},
		{"no suitable address after the IPv4 pin", opErr(&net.AddrError{Err: "no suitable address found", Addr: "aaaaonly"}), FailureDialUnreachable},
		{"dns lookup failure", &net.DNSError{Err: "no such host", Name: "web.whatsapp.com"}, FailureDNS},
		{"context deadline", context.DeadlineExceeded, FailureTimeout},
		{"io deadline", os.ErrDeadlineExceeded, FailureTimeout},
		{"tls record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, FailureTLS},
		{"unknown certificate authority", x509.UnknownAuthorityError{}, FailureTLS},
		{"whatsmeow dial wrapper alone", fmt.Errorf("%w: handshake", socket.ErrDialFailed), FailureWebsocket},
		{"logged out", whatsmeow.ErrNotLoggedIn, FailureLoggedOut},
		{"anything else", errors.New("something odd"), FailureUnknown},
		{"wrapped incident error keeps its class", fmt.Errorf("%w: %w", socket.ErrDialFailed, opErr(&os.SyscallError{Syscall: "connect", Err: syscall.ENETUNREACH})), FailureDialUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyFailure(tt.err); got != tt.want {
				t.Errorf("classifyFailure(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyFailureNeverLeaksTheError guards the reason why this function
// exists: /api/health is unauthenticated.
func TestClassifyFailureNeverLeaksTheError(t *testing.T) {
	secretish := errors.New(`Get "https://web.whatsapp.com/ws/chat?token=SUPERSECRET": dial tcp [2a03::1]:443: connect: network is unreachable`)
	got := classifyFailure(secretish)
	if strings.Contains(got, "SUPERSECRET") || strings.Contains(got, "web.whatsapp.com") {
		t.Fatalf("classifyFailure leaked the error: %q", got)
	}
	if got != FailureUnknown {
		t.Errorf("classifyFailure() = %q, want %q", got, FailureUnknown)
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
	if c.reconnectAttempts != 3 {
		t.Fatalf("recordAttempt stored attempts=%d, want 3", c.reconnectAttempts)
	}
	if c.reconnectFailure != FailureUnknown {
		t.Fatalf("recordAttempt stored reason=%q, want %q", c.reconnectFailure, FailureUnknown)
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
