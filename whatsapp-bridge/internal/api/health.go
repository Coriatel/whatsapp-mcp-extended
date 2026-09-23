package api

import (
	"time"

	"whatsapp-bridge/internal/whatsapp"
)

// buildHealthPayload renders a connection snapshot as the /api/health body.
// Pure so the contract can be tested without a live WhatsApp client.
func buildHealthPayload(s whatsapp.ConnSnapshot, now time.Time) map[string]interface{} {
	resp := map[string]interface{}{
		"connected":          s.Connected,
		"state":              s.State,
		"uptime":             now.Sub(s.StartedAt).Round(time.Second).String(),
		"reconnect_errs":     s.AutoReconnectErrors,
		"reconnect_attempts": s.ReconnectAttempts,
	}
	if !s.LastConnected.IsZero() {
		resp["last_connected"] = s.LastConnected.Format(time.RFC3339)
	}
	if !s.DisconnectedSince.IsZero() {
		resp["disconnected_since"] = s.DisconnectedSince.Format(time.RFC3339)
		resp["disconnected_for"] = now.Sub(s.DisconnectedSince).Round(time.Second).String()
	}
	if !s.LastSuccessfulSend.IsZero() {
		resp["last_successful_send"] = s.LastSuccessfulSend.Format(time.RFC3339)
	}
	if !s.LastReceipt.IsZero() {
		resp["last_receipt"] = s.LastReceipt.Format(time.RFC3339)
	}
	if !s.ReconnectLastAttemptAt.IsZero() {
		resp["reconnect_last_attempt_at"] = s.ReconnectLastAttemptAt.Format(time.RFC3339)
	}
	if s.ReconnectFailureReason != "" {
		resp["reconnect_failure_reason"] = s.ReconnectFailureReason
	}
	return resp
}
