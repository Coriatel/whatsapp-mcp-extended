package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// The bridge account. Digits are fake; only the JID shape matters.
var (
	ownPN  = types.NewJID("972500000001", types.DefaultUserServer)
	ownLID = types.NewJID("111111111111111", types.HiddenUserServer)
)

func receiptOn(chat types.JID, t types.ReceiptType) *events.Receipt {
	r := &events.Receipt{Type: t}
	r.Chat = chat
	return r
}

func TestClassifyReceipt(t *testing.T) {
	other := types.NewJID("972500000002", types.DefaultUserServer)

	cases := []struct {
		name     string
		receipt  *events.Receipt
		wantType string
		wantSelf bool
		wantOK   bool
	}{
		{"self-chat read-self forwarded as read", receiptOn(ownPN, types.ReceiptTypeReadSelf), "read", true, true},
		{"self-chat read-self on LID forwarded as read", receiptOn(ownLID, types.ReceiptTypeReadSelf), "read", true, true},
		{"other-chat read-self dropped", receiptOn(other, types.ReceiptTypeReadSelf), "", false, false},
		{"delivered unchanged", receiptOn(other, types.ReceiptTypeDelivered), "delivered", false, true},
		{"self-chat delivered unchanged", receiptOn(ownPN, types.ReceiptTypeDelivered), "delivered", false, true},
		{"read unchanged", receiptOn(other, types.ReceiptTypeRead), "read", false, true},
		{"sender receipt dropped", receiptOn(other, types.ReceiptTypeSender), "", false, false},
		{"played receipt dropped", receiptOn(ownPN, types.ReceiptTypePlayed), "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotSelf, gotOK := classifyReceipt(tc.receipt, ownPN, ownLID)
			if gotType != tc.wantType || gotSelf != tc.wantSelf || gotOK != tc.wantOK {
				t.Fatalf("classifyReceipt = (%q, %v, %v), want (%q, %v, %v)",
					gotType, gotSelf, gotOK, tc.wantType, tc.wantSelf, tc.wantOK)
			}
		})
	}
}

// Not logged in: no own JID is known, so nothing may be treated as a self-chat.
func TestClassifyReceiptNoOwnJID(t *testing.T) {
	var zero types.JID
	if _, _, ok := classifyReceipt(receiptOn(zero, types.ReceiptTypeReadSelf), zero, zero); ok {
		t.Fatal("read-self forwarded with no own JID known")
	}
	if _, _, ok := classifyReceipt(receiptOn(ownPN, types.ReceiptTypeReadSelf), zero, zero); ok {
		t.Fatal("read-self forwarded before the store knew the account JID")
	}
}
