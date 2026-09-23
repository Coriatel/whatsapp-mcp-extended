package whatsapp

import "time"

// Connection state values reported by /api/health and /api/connection.
const (
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
	StateLoggedOut    = "logged_out"
	StateUnknown      = "unknown"
)

// maxReasonLen bounds reconnect_failure_reason so a verbose dial error (which
// can embed URLs and headers) cannot bloat the health payload.
const maxReasonLen = 200

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
	ReconnectFailureReason string
	ReconnectLastAttemptAt time.Time
}

// truncateReason clips an error string to maxReasonLen runes.
func truncateReason(s string) string {
	r := []rune(s)
	if len(r) <= maxReasonLen {
		return s
	}
	return string(r[:maxReasonLen])
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
func (c *Client) Snapshot() ConnSnapshot {
	connected := c.IsConnected()
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
		c.reconnectFailure = truncateReason(err.Error())
	} else {
		c.reconnectFailure = ""
	}
}
