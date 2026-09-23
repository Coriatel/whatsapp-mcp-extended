package whatsapp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// TestSnapshotTracksTheConnectionFlag covers the state the handlers read.
func TestSnapshotTracksTheConnectionFlag(t *testing.T) {
	tests := []struct {
		name string
		mark func(*Client)
		want bool
	}{
		{"fresh client is not connected", func(*Client) {}, false},
		{"MarkConnected", func(c *Client) { c.MarkConnected() }, true},
		{"MarkDisconnected", func(c *Client) { c.MarkDisconnected() }, false},
		{"reconnect after an outage", func(c *Client) { c.MarkConnected(); c.MarkDisconnected(); c.MarkConnected() }, true},
		{"outage after a good connection", func(c *Client) { c.MarkConnected(); c.MarkDisconnected() }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No whatsmeow client at all: Snapshot must not need one.
			c := &Client{startedAt: time.Now()}
			tt.mark(c)
			if got := c.Snapshot().Connected; got != tt.want {
				t.Errorf("Snapshot().Connected = %v, want %v", got, tt.want)
			}
		})
	}
}

// blackholeDialer blocks every dial until release is closed, reproducing the
// container netns where outbound 443 is DROPped.
type blackholeDialer struct {
	once    sync.Once
	dialing chan struct{}
	release chan struct{}
}

func (b *blackholeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	b.once.Do(func() { close(b.dialing) })
	<-b.release
	return nil, errors.New("blackhole")
}

// TestSnapshotDoesNotBlockWhileDialing is the regression test for the stall:
// whatsmeow's ConnectContext holds socketLock for the whole dial, so anything
// that reads IsConnected from an HTTP handler hangs until the dial times out.
func TestSnapshotDoesNotBlockWhileDialing(t *testing.T) {
	bh := &blackholeDialer{dialing: make(chan struct{}), release: make(chan struct{})}
	httpClient := &http.Client{Transport: &http.Transport{DialContext: bh.DialContext}}

	wm := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	wm.EnableAutoReconnect = false
	wm.InitialAutoReconnect = false
	wm.SetPreLoginHTTPClient(httpClient)
	wm.SetWebsocketHTTPClient(httpClient)

	c := &Client{Client: wm, logger: waLog.Noop, startedAt: time.Now()}
	c.MarkConnected()

	connectReturned := make(chan struct{})
	go func() {
		defer close(connectReturned)
		_ = wm.Connect()
	}()

	select {
	case <-bh.dialing:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial never started, so nothing holds socketLock")
	}
	defer func() {
		close(bh.release)
		<-connectReturned
	}()

	// Control: prove socketLock really is held, or this test proves nothing.
	whatsmeowPath := make(chan bool, 1)
	go func() { whatsmeowPath <- wm.IsConnected() }()
	select {
	case <-whatsmeowPath:
		t.Fatal("whatsmeow IsConnected returned while a dial was in flight; the lock premise no longer holds")
	case <-time.After(100 * time.Millisecond):
	}

	// The actual assertion: our snapshot answers anyway.
	snap := make(chan ConnSnapshot, 1)
	go func() { snap <- c.Snapshot() }()
	select {
	case s := <-snap:
		if !s.Connected {
			t.Error("Snapshot lost the connected flag")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Snapshot blocked while a dial was in flight; /api/health would time out")
	}
}
