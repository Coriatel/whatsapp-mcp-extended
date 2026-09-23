package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"whatsapp-bridge/internal/whatsapp"
)

func TestBuildHealthPayloadFields(t *testing.T) {
	now := time.Date(2026, 9, 23, 7, 0, 0, 0, time.UTC)
	started := now.Add(-2*time.Hour - 48*time.Minute)
	lastConn := now.Add(-2 * time.Hour)
	discSince := now.Add(-2 * time.Hour)

	tests := []struct {
		name   string
		snap   whatsapp.ConnSnapshot
		want   map[string]interface{}
		absent []string
	}{
		{
			name: "healthy connection keeps the legacy fields",
			snap: whatsapp.ConnSnapshot{
				Connected:     true,
				State:         whatsapp.StateConnected,
				StartedAt:     started,
				LastConnected: lastConn,
			},
			want: map[string]interface{}{
				"connected":          true,
				"state":              "connected",
				"uptime":             "2h48m0s",
				"reconnect_errs":     0,
				"reconnect_attempts": 0,
				"last_connected":     "2026-09-23T05:00:00Z",
			},
			absent: []string{"disconnected_since", "reconnect_failure_reason", "last_receipt"},
		},
		{
			name: "the 2026-09-23 outage is now fully described",
			snap: whatsapp.ConnSnapshot{
				Connected:              false,
				State:                  whatsapp.StateDisconnected,
				StartedAt:              started,
				LastConnected:          lastConn,
				DisconnectedSince:      discSince,
				LastSuccessfulSend:     now.Add(-3 * time.Hour),
				LastReceipt:            now.Add(-150 * time.Minute),
				ReconnectAttempts:      7,
				ReconnectFailureReason: "dial tcp [2a03:2880:f23d:c7::167]:443: connect: network is unreachable",
				ReconnectLastAttemptAt: now.Add(-30 * time.Second),
			},
			want: map[string]interface{}{
				"connected":                 false,
				"state":                     "disconnected",
				"reconnect_attempts":        7,
				"disconnected_since":        "2026-09-23T05:00:00Z",
				"disconnected_for":          "2h0m0s",
				"last_successful_send":      "2026-09-23T04:00:00Z",
				"last_receipt":              "2026-09-23T04:30:00Z",
				"reconnect_last_attempt_at": "2026-09-23T06:59:30Z",
			},
		},
		{
			name: "logged out is reported, not hidden behind disconnected",
			snap: whatsapp.ConnSnapshot{State: whatsapp.StateLoggedOut, StartedAt: started},
			want: map[string]interface{}{"state": "logged_out", "connected": false},
		},
		{
			name:   "fresh process before the first connect",
			snap:   whatsapp.ConnSnapshot{State: whatsapp.StateUnknown, StartedAt: now},
			want:   map[string]interface{}{"state": "unknown", "uptime": "0s"},
			absent: []string{"last_connected", "disconnected_since"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildHealthPayload(tt.snap, now)
			for k, want := range tt.want {
				v, ok := got[k]
				if !ok {
					t.Errorf("field %q missing from payload", k)
					continue
				}
				if v != want {
					t.Errorf("field %q = %v (%T), want %v (%T)", k, v, v, want, want)
				}
			}
			for _, k := range tt.absent {
				if _, ok := got[k]; ok {
					t.Errorf("field %q should be omitted, got %v", k, got[k])
				}
			}
			if _, err := json.Marshal(got); err != nil {
				t.Errorf("payload is not JSON-serialisable: %v", err)
			}
		})
	}
}

// TestHealthPayloadContainsEveryRequiredField locks the /api/health contract so
// a future refactor cannot silently drop a monitored field.
func TestHealthPayloadContainsEveryRequiredField(t *testing.T) {
	now := time.Now()
	snap := whatsapp.ConnSnapshot{
		Connected:              false,
		State:                  whatsapp.StateDisconnected,
		StartedAt:              now.Add(-time.Hour),
		LastConnected:          now.Add(-time.Minute),
		DisconnectedSince:      now.Add(-time.Minute),
		LastSuccessfulSend:     now.Add(-2 * time.Minute),
		LastReceipt:            now.Add(-3 * time.Minute),
		AutoReconnectErrors:    2,
		ReconnectAttempts:      4,
		ReconnectFailureReason: "boom",
		ReconnectLastAttemptAt: now,
	}
	got := buildHealthPayload(snap, now)
	required := []string{
		// legacy fields the Docker healthcheck and monitoring already read
		"connected", "last_connected", "reconnect_errs", "uptime",
		// new fields
		"state", "disconnected_since", "last_successful_send", "last_receipt",
		"reconnect_attempts", "reconnect_failure_reason", "reconnect_last_attempt_at",
	}
	for _, k := range required {
		if _, ok := got[k]; !ok {
			t.Errorf("required field %q missing from /api/health", k)
		}
	}
}

func TestHealthPayloadNeverLeaksASecret(t *testing.T) {
	now := time.Now()
	snap := whatsapp.ConnSnapshot{
		State:                  whatsapp.StateDisconnected,
		StartedAt:              now,
		ReconnectFailureReason: "dial failed",
	}
	blob, err := json.Marshal(buildHealthPayload(snap, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"X-API-Key", "api_key", "secret", "token", "Authorization"} {
		if strings.Contains(strings.ToLower(string(blob)), strings.ToLower(needle)) {
			t.Errorf("health payload contains %q: %s", needle, blob)
		}
	}
}
