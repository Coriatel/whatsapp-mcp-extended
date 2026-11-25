package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestOwnRead(t *testing.T) {
	cases := []struct {
		kind   types.ReceiptType
		fromMe bool
		want   bool
	}{
		{types.ReceiptTypeReadSelf, true, true},
		{types.ReceiptTypeRead, true, true},
		{types.ReceiptTypeRead, false, false},
		{types.ReceiptTypeDelivered, true, false},
	}
	for _, c := range cases {
		r := &events.Receipt{Type: c.kind}
		r.IsFromMe = c.fromMe
		if got := ownRead(r); got != c.want {
			t.Errorf("ownRead(%s, fromMe=%v) = %v, want %v", c.kind, c.fromMe, got, c.want)
		}
	}
}
