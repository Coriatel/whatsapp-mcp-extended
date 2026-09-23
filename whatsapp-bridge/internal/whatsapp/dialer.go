package whatsapp

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"
)

// ipv6ProbeAddr is a well-known global IPv6 address (Google public DNS). We only
// need the kernel's routing verdict, so any globally-routable address works; a
// host with no IPv6 route fails this immediately with ENETUNREACH rather than
// waiting for a timeout.
const ipv6ProbeAddr = "[2001:4860:4860::8888]:53"

// ipv6ProbeTimeout bounds the one-off startup probe.
const ipv6ProbeTimeout = 2 * time.Second

// dialNetworkFor maps the IPv6 routing verdict onto the network passed to
// net.Dialer. "tcp" keeps Go's Happy Eyeballs (dual-stack) behaviour; "tcp4"
// takes IPv6 out of the picture entirely on a host that cannot route it.
func dialNetworkFor(ipv6Routable bool) string {
	if ipv6Routable {
		return "tcp"
	}
	return "tcp4"
}

// resolveDialNetwork decides the dial network. WA_DIAL_NETWORK=tcp4|tcp forces a
// value (the rollback lever, no rebuild needed); anything else probes.
func resolveDialNetwork(override string, probe func() bool) string {
	switch override {
	case "tcp4", "tcp6", "tcp":
		return override
	default:
		return dialNetworkFor(probe())
	}
}

// probeIPv6 reports whether a TCP connection over IPv6 can even be attempted
// from this host. It does not care whether the remote answers, only whether the
// kernel has a route.
func probeIPv6() bool {
	d := net.Dialer{Timeout: ipv6ProbeTimeout}
	conn, err := d.Dial("tcp6", ipv6ProbeAddr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// newHTTPClientForNetwork builds an http.Client whose transport dials over the
// given network. FallbackDelay is left at Go's default so dual-stack ("tcp")
// still races IPv4 against IPv6.
func newHTTPClientForNetwork(network string) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: base}
}

// installDialer pins the websocket dialer to a network family this host can
// actually route.
//
// Why this is needed: whatsmeow builds its websocket transport from
// http.DefaultTransport.Clone() (client.go NewClient) and passes it straight to
// websocket.Dial (socket/dialopts.go), so the dial inherits Go's default
// dual-stack behaviour with no way to express "this container has no IPv6
// route". When DNS hands back an AAAA record first and there is no IPv6 route,
// the dial error surfaced to the caller is the IPv6 one, which is what the
// 2026-09-23 outage logged.
func (c *Client) installDialer() {
	network := resolveDialNetwork(os.Getenv("WA_DIAL_NETWORK"), probeIPv6)
	if network == "tcp" {
		c.logger.Infof("DIAL_NETWORK=tcp (IPv6 routable, dual-stack enabled)")
		return
	}
	c.logger.Warnf("DIAL_NETWORK=%s (IPv6 not routable from this host, forcing IPv4 for the websocket)", network)
	httpClient := newHTTPClientForNetwork(network)
	c.Client.SetWebsocketHTTPClient(httpClient)
	c.Client.SetPreLoginHTTPClient(httpClient)
}
