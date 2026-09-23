package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"time"

	"whatsapp-bridge/internal/database"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// GetChatName determines the appropriate name for a chat based on JID and other info
func (c *Client) GetChatName(messageStore *database.MessageStore, jid types.JID, chatJID string, conversation interface{}, sender string) string {
	// First, check if chat already exists in database with a name
	var existingName string
	err := messageStore.GetDB().QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&existingName)
	if err == nil && existingName != "" {
		// Chat exists with a name, use that
		c.logger.Infof("Using existing chat name for %s: %s", chatJID, existingName)
		return existingName
	}

	// Need to determine chat name
	var name string

	if jid.Server == "g.us" {
		// This is a group chat
		c.logger.Infof("Getting name for group: %s", chatJID)

		// Use conversation data if provided (from history sync)
		if conversation != nil {
			// Extract name from conversation if available
			// This uses type assertions to handle different possible types
			var displayName, convName *string
			// Try to extract the fields we care about regardless of the exact type
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()

				// Try to find DisplayName field
				if displayNameField := v.FieldByName("DisplayName"); displayNameField.IsValid() && displayNameField.Kind() == reflect.Ptr && !displayNameField.IsNil() {
					dn := displayNameField.Elem().String()
					displayName = &dn
				}

				// Try to find Name field
				if nameField := v.FieldByName("Name"); nameField.IsValid() && nameField.Kind() == reflect.Ptr && !nameField.IsNil() {
					n := nameField.Elem().String()
					convName = &n
				}
			}

			// Use the name we found
			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
			}
		}

		// If we didn't get a name, try group info
		if name == "" {
			groupInfo, err := c.Client.GetGroupInfo(context.Background(), jid)
			if err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				// Fallback name for groups
				name = fmt.Sprintf("Group %s", jid.User)
			}
		}

		c.logger.Infof("Using group name: %s", name)
	} else {
		// This is an individual contact
		c.logger.Infof("Getting name for contact: %s", chatJID)

		// Just use contact info (full name)
		contact, err := c.Store.Contacts.GetContact(context.Background(), jid)
		if err == nil && contact.FullName != "" {
			name = contact.FullName
		} else if sender != "" {
			// Fallback to sender
			name = sender
		} else {
			// Last fallback to JID
			name = jid.User
		}

		c.logger.Infof("Using contact name: %s", name)
	}

	return name
}

// HandleMessage processes regular incoming messages with media support and webhook processing
func (c *Client) HandleMessage(messageStore *database.MessageStore, webhookManager interface{}, msg *events.Message) {
	// Save message to database
	chatJID := msg.Info.Chat.String()
	sender := msg.Info.Sender.User

	// Get appropriate chat name (pass nil for conversation since we don't have one for regular messages)
	name := c.GetChatName(messageStore, msg.Info.Chat, chatJID, nil, sender)

	// Update chat in database with the message timestamp (keeps last message time updated)
	err := messageStore.StoreChat(chatJID, name, msg.Info.Timestamp)
	if err != nil {
		c.logger.Warnf("Failed to store chat: %v", err)
	}

	// Extract text content
	content := ExtractTextContent(msg.Message)

	// Extract media info
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := ExtractMediaInfo(msg.Message)

	// Skip if there's no content and no media
	if content == "" && mediaType == "" {
		return
	}

	// Get sender name (PushName from WhatsApp)
	senderName := msg.Info.PushName
	if senderName == "" {
		senderName = sender // fallback to JID
	}

	// Store message in database
	err = messageStore.StoreMessage(
		msg.Info.ID,
		chatJID,
		sender,
		senderName,
		content,
		msg.Info.Timestamp,
		msg.Info.IsFromMe,
		mediaType,
		filename,
		url,
		mediaKey,
		fileSHA256,
		fileEncSHA256,
		fileLength,
	)

	if err != nil {
		c.logger.Warnf("Failed to store message: %v", err)
	}

	// Process webhooks if manager is available
	if webhookManager != nil {
		// Cast to webhook manager and process message
		if wm, ok := webhookManager.(interface {
			ProcessMessage(client interface{}, msg *events.Message, chatName string)
		}); ok {
			wm.ProcessMessage(c, msg, name)
		}
	}
}

// HandleHistorySync processes history sync events
func (c *Client) HandleHistorySync(messageStore *database.MessageStore, historySync *events.HistorySync) {
	c.logger.Infof("Received history sync event with %d conversations", len(historySync.Data.Conversations))

	syncedCount := 0
	for _, conversation := range historySync.Data.Conversations {
		// Parse JID from the conversation
		if conversation.ID == nil {
			continue
		}

		chatJID := *conversation.ID

		// Try to parse the JID
		jid, err := types.ParseJID(chatJID)
		if err != nil {
			c.logger.Warnf("Failed to parse JID %s: %v", chatJID, err)
			continue
		}

		// Get appropriate chat name by passing the history sync conversation directly
		name := c.GetChatName(messageStore, jid, chatJID, conversation, "")

		// Process messages
		messages := conversation.Messages
		if len(messages) > 0 {
			// Update chat with latest message timestamp
			latestMsg := messages[0]
			if latestMsg == nil || latestMsg.Message == nil {
				continue
			}

			// Get timestamp from message info
			ts := latestMsg.Message.GetMessageTimestamp()
			if ts == 0 {
				continue
			}
			timestamp := time.Unix(int64(ts), 0)

			if err := messageStore.StoreChat(chatJID, name, timestamp); err != nil {
				c.logger.Warnf("Failed to store chat: %v", err)
			}

			// Store messages
			for _, msg := range messages {
				if msg == nil || msg.Message == nil {
					continue
				}

				// Extract text content
				var content string
				if msg.Message.Message != nil {
					if conv := msg.Message.Message.GetConversation(); conv != "" {
						content = conv
					} else if ext := msg.Message.Message.GetExtendedTextMessage(); ext != nil {
						content = ext.GetText()
					}
				}

				// Extract media info
				var mediaType, filename, url string
				var mediaKey, fileSHA256, fileEncSHA256 []byte
				var fileLength uint64

				if msg.Message.Message != nil {
					mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength = ExtractMediaInfo(msg.Message.Message)
				}

				// Log the message content for debugging
				c.logger.Infof("Message content: %v, Media Type: %v", content, mediaType)

				// Skip messages with no content and no media
				if content == "" && mediaType == "" {
					continue
				}

				// Determine sender
				var sender string
				isFromMe := false
				if msg.Message.Key != nil {
					if msg.Message.Key.FromMe != nil {
						isFromMe = *msg.Message.Key.FromMe
					}
					if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
						sender = *msg.Message.Key.Participant
					} else if isFromMe {
						sender = c.Store.ID.User
					} else {
						sender = jid.User
					}
				} else {
					sender = jid.User
				}

				// Store message
				msgID := ""
				if msg.Message.Key != nil && msg.Message.Key.ID != nil {
					msgID = *msg.Message.Key.ID
				}

				// Get message timestamp
				ts2 := msg.Message.GetMessageTimestamp()
				if ts2 == 0 {
					continue
				}
				timestamp := time.Unix(int64(ts2), 0)

				// For history sync, use sender as senderName fallback (PushName not directly available)
				senderName := sender

				err = messageStore.StoreMessage(
					msgID,
					chatJID,
					sender,
					senderName,
					content,
					timestamp,
					isFromMe,
					mediaType,
					filename,
					url,
					mediaKey,
					fileSHA256,
					fileEncSHA256,
					fileLength,
				)
				if err != nil {
					c.logger.Warnf("Failed to store history message: %v", err)
				} else {
					syncedCount++
					// Log successful message storage
					if mediaType != "" {
						c.logger.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
							timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, mediaType, filename, content)
					} else {
						c.logger.Infof("Stored message: [%s] %s -> %s: %s",
							timestamp.Format("2006-01-02 15:04:05"), sender, chatJID, content)
					}
				}
			}
		}
	}

	c.logger.Infof("History sync complete. Stored %d messages.", syncedCount)
}

// HandleReceipt forwards WhatsApp delivery/read receipts to the configured
// receipts webhook (transcriptor-api /api/v1/wa-receipts/webhook). It is a
// no-op unless RECEIPTS_WEBHOOK_URL is set, so a rebuilt bridge is behaviour-
// identical to before until the env var is configured. The backend matches by
// message_id (our sent message's WhatsApp id); unmatched receipts are harmless.
func (c *Client) HandleReceipt(receipt *events.Receipt) {
	// Any receipt proves the socket is carrying real traffic, independent of
	// whether the receipts webhook is configured.
	c.MarkReceipt()

	// S3 diagnostic (gated): dump the full receipt struct so we can see exactly
	// where the matchable message id lives for the empty-MessageIDs cases.
	if os.Getenv("RECEIPT_DEBUG") == "1" {
		c.logger.Infof("RECEIPT_DUMP type=%v nids=%d ids=%q sender=%s chat=%s isFromMe=%v isGroup=%v full=%+v",
			receipt.Type, len(receipt.MessageIDs), receipt.MessageIDs,
			receipt.Sender.String(), receipt.Chat.String(),
			receipt.IsFromMe, receipt.IsGroup, *receipt)
	}
	url := os.Getenv("RECEIPTS_WEBHOOK_URL")
	if url == "" {
		return
	}
	ownPN, ownLID := c.ownChatJIDs()
	receiptType, self, ok := classifyReceipt(receipt, ownPN, ownLID)
	if !ok {
		return
	}
	secret := os.Getenv("WA_WEBHOOK_SECRET")
	httpClient := &http.Client{Timeout: 10 * time.Second}
	ts := receipt.Timestamp.Unix()
	selfSuffix := ""
	if self {
		selfSuffix = " (self-chat)"
	}
	for _, id := range receipt.MessageIDs {
		payload := map[string]interface{}{
			"type":         "receipt",
			"receipt_type": receiptType,
			"message_id":   string(id),
			"from":         receipt.Sender.String(),
			"timestamp":    ts,
		}
		if self {
			payload["self"] = true
		}
		body, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			// HMAC-SHA256 over "<unix-ts>.<raw body>"; the API verifies with a
			// freshness window + replay cache (services/webhook_auth.py).
			sentAt := strconv.FormatInt(time.Now().Unix(), 10)
			req.Header.Set("X-Webhook-Timestamp", sentAt)
			req.Header.Set("X-Webhook-Signature", signReceipt(secret, sentAt, body))
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			c.logger.Warnf("Receipt webhook POST failed for %s: %v", id, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			// fire-and-forget: a refused receipt is lost, so make it loud (secret mismatch, clock skew, API down)
			c.logger.Warnf("Receipt webhook refused %s receipt for msg %s (status %d)", receiptType, id, resp.StatusCode)
			continue
		}
		c.logger.Infof("Forwarded %s%s receipt for msg %s (status %d)", receiptType, selfSuffix, id, resp.StatusCode)
	}
}

// ownChatJIDs returns the JIDs that identify this account's chat with itself:
// the phone-number JID and, for a migrated account, the LID. Either is zero
// when the store has not been populated (not logged in yet).
func (c *Client) ownChatJIDs() (pn, lid types.JID) {
	if c.Client == nil || c.Store == nil {
		return
	}
	if c.Store.ID != nil {
		pn = c.Store.ID.ToNonAD()
	}
	lid = c.Store.LID.ToNonAD()
	return
}

// classifyReceipt maps a whatsmeow receipt to the webhook receipt_type. ok is
// false for receipts that must not be forwarded.
//
// read-self is what WhatsApp emits when this account read a message on another
// of its own devices (whatsmeow types/presence.go:46-47). The owner canary
// sends to the bridge account's own number, so every canary message is a
// self-chat: opening it on the phone produces read-self, never read, and the
// receipt was dropped here - which is why those sends never bound a receipt.
// A self-chat read-self is forwarded as a "read" receipt flagged self; for any
// other chat read-self says nothing about the recipient and is still dropped.
func classifyReceipt(receipt *events.Receipt, ownPN, ownLID types.JID) (receiptType string, self bool, ok bool) {
	switch receipt.Type {
	case types.ReceiptTypeDelivered:
		return "delivered", false, true
	case types.ReceiptTypeRead:
		return "read", false, true
	case types.ReceiptTypeReadSelf:
		if isSelfChat(receipt.Chat, ownPN, ownLID) {
			return "read", true, true
		}
		return "", false, false
	default:
		return "", false, false
	}
}

// isSelfChat reports whether chat is this account's chat with itself.
func isSelfChat(chat, ownPN, ownLID types.JID) bool {
	for _, own := range []types.JID{ownPN, ownLID} {
		if !own.IsEmpty() && !chat.IsEmpty() && chat.User == own.User && chat.Server == own.Server {
			return true
		}
	}
	return false
}

// signReceipt returns "sha256=<hex(HMAC_SHA256(secret, ts + "." + body))>".
func signReceipt(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
