package whatsapp

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDialNetworkFor(t *testing.T) {
	tests := []struct {
		name         string
		ipv6Routable bool
		want         string
	}{
		{"IPv6 routable keeps dual-stack", true, "tcp"},
		{"IPv6 unroutable forces IPv4", false, "tcp4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dialNetworkFor(tt.ipv6Routable); got != tt.want {
				t.Errorf("dialNetworkFor(%v) = %q, want %q", tt.ipv6Routable, got, tt.want)
			}
		})
	}
}

func TestResolveDialNetwork(t *testing.T) {
	tests := []struct {
		name      string
		override  string
		probeOK   bool
		wantProbe bool
		want      string
	}{
		{"tcp6 dial fails -> tcp4", "", false, true, "tcp4"},
		{"tcp6 dial works -> tcp", "", true, true, "tcp"},
		{"explicit tcp4 skips the probe", "tcp4", true, false, "tcp4"},
		{"explicit tcp skips the probe", "tcp", false, false, "tcp"},
		{"explicit tcp6 skips the probe", "tcp6", false, false, "tcp6"},
		{"unknown override falls back to the probe", "banana", false, true, "tcp4"},
		{"auto probes", "auto", true, true, "tcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probed := false
			probe := func() bool { probed = true; return tt.probeOK }
			got := resolveDialNetwork(tt.override, probe)
			if got != tt.want {
				t.Errorf("resolveDialNetwork(%q) = %q, want %q", tt.override, got, tt.want)
			}
			if probed != tt.wantProbe {
				t.Errorf("probe called = %v, want %v", probed, tt.wantProbe)
			}
		})
	}
}

// TestNewHTTPClientPinsTheNetwork proves the transport ignores the network
// http asks for and dials the pinned family instead. A tcp6-pinned dialer must
// refuse a loopback IPv4 address that a "tcp" dialer accepts.
func TestNewHTTPClientPinsTheNetwork(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no IPv4 loopback available: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	dialWith := func(network string) error {
		tr, ok := newHTTPClientForNetwork(network).Transport.(*http.Transport)
		if !ok {
			t.Fatalf("transport is %T, want *http.Transport", newHTTPClientForNetwork(network).Transport)
		}
		if tr.DialContext == nil {
			t.Fatal("DialContext was not overridden")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// "tcp" is what net/http would pass; the pinned network must win.
		conn, err := tr.DialContext(ctx, "tcp", ln.Addr().String())
		if err == nil {
			conn.Close()
		}
		return err
	}

	if err := dialWith("tcp4"); err != nil {
		t.Errorf("tcp4-pinned dialer failed on an IPv4 address: %v", err)
	}
	if err := dialWith("tcp6"); err == nil {
		t.Error("tcp6-pinned dialer accepted an IPv4 address, so the network is not pinned")
	}
}
