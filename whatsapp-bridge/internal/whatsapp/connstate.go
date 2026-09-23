package whatsapp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
)

// Connection state values reported by /api/health and /api/connection.
const (
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
	StateLoggedOut    = "logged_out"
	StateUnknown      = "unknown"
)

// Failure classes reported as reconnect_failure_reason. /api/health is
// unauthenticated, so the field carries a stable token from this closed set
// rather than a raw error string, which can embed URLs, headers and hostnames.
// The full error stays in the logs.
const (
	FailureDialUnreachable = "dial_unreachable"
	FailureDNS             = "dns"
	FailureTimeout         = "timeout"
	FailureTLS             = "tls"
	FailureWebsocket       = "websocket"
	FailureLoggedOut       = "logged_out"
	FailureUnknown         = "unknown"
)

// classifyFailure maps a reconnect error onto one of the failure tokens.
// Ordered most-specific first; the whatsmeow dial wrapper is checked late
// because it wraps every other cause.
func classifyFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, whatsmeow.ErrNotLoggedIn) {
		return FailureLoggedOut
	}

	var recordErr tls.RecordHeaderError
	var authErr x509.UnknownAuthorityError
	var certErr x509.CertificateInvalidError
	var hostErr x509.HostnameError
	if errors.As(err, &recordErr) || errors.As(err, &authErr) || errors.As(err, &certErr) || errors.As(err, &hostErr) {
		return FailureTLS
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return FailureDNS
	}

	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return FailureTimeout
	}

	var addrErr *net.AddrError
	if errors.As(err, &addrErr) {
		// "no suitable address found": the family we are pinned to was absent.
		return FailureDialUnreachable
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ECONNREFUSED, syscall.ENETDOWN, syscall.ECONNRESET:
			return FailureDialUnreachable
		}
	}

	if errors.Is(err, socket.ErrDialFailed) {
		return FailureWebsocket
	}
	return FailureUnknown
}

// ConnSnapshot is an immutable view of the client's connection health, taken
// under the state mutex so the HTTP handlers never read torn values.
type ConnSnapshot struct {
	Connected              bool
	State                  string
	StartedAt              time.Time
	LastConnected          time.Time
	DisconnectedSince      time.Time
	LastSuccessfulSend     time.Time
	LastReceipt            time.Time
	AutoReconnectErrors    int
	ReconnectAttempts      int
	ReconnectFailureReason string // a classifyFailure token, never a raw error
	ReconnectLastAttemptAt time.Time
}

// connState derives the reported state from the flags. Split out from Snapshot
// so it can be tested without a whatsmeow client.
func connState(connected, loggedOut, everConnected bool) string {
	switch {
	case loggedOut:
		return StateLoggedOut
	case connected:
		return StateConnected
	case everConnected:
		return StateDisconnected
	default:
		return StateUnknown
	}
}

// Snapshot returns the current connection health.
// Snapshot must never call into the whatsmeow client: a dial in progress holds
// socketLock for its whole duration, and /api/health has to answer during
// exactly that window.
func (c *Client) Snapshot() ConnSnapshot {
	connected := c.connected.Load()
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return ConnSnapshot{
		Connected:              connected,
		State:                  connState(connected, c.loggedOut, !c.lastConnectedAt.IsZero()),
		StartedAt:              c.startedAt,
		LastConnected:          c.lastConnectedAt,
		DisconnectedSince:      c.disconnectedAt,
		LastSuccessfulSend:     c.lastSuccessfulSend,
		LastReceipt:            c.lastReceiptAt,
		AutoReconnectErrors:    c.autoReconnectErrors,
		ReconnectAttempts:      c.reconnectAttempts,
		ReconnectFailureReason: c.reconnectFailure,
		ReconnectLastAttemptAt: c.reconnectLastAttempt,
	}
}

// MarkSuccessfulSend records that a message left the bridge successfully.
func (c *Client) MarkSuccessfulSend() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.lastSuccessfulSend = time.Now()
}

// MarkReceipt records that WhatsApp delivered a receipt to us. Together with
// MarkSuccessfulSend this distinguishes "socket is up" from "traffic flows".
func (c *Client) MarkReceipt() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.lastReceiptAt = time.Now()
}

// MarkLoggedOut records that WhatsApp invalidated the session. This is an owner
// gate: no amount of reconnecting fixes it, only re-pairing does.
func (c *Client) MarkLoggedOut() {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.loggedOut = true
}

// recordAttempt stores per-attempt state for /api/health.
func (c *Client) recordAttempt(attempt int, err error) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.reconnectAttempts = attempt
	c.reconnectLastAttempt = time.Now()
	if err != nil {
		c.reconnectFailure = classifyFailure(err)
	} else {
		c.reconnectFailure = ""
	}
}
