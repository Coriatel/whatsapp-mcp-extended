package whatsapp

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"whatsapp-bridge/internal/database"
	bridgeTypes "whatsapp-bridge/internal/types"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// allowedMediaDirs contains directories allowed for media access
var allowedMediaDirs = []string{
	"/app/media",
	"/app/store",
	"/tmp",
}

// validateMediaPath checks if the path is within allowed directories
func validateMediaPath(mediaPath string) error {
	if mediaPath == "" {
		return nil
	}

	// Clean and get absolute path
	cleanPath := filepath.Clean(mediaPath)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return fmt.Errorf("invalid media path: %v", err)
	}

	// Check for path traversal attempts
	if strings.Contains(mediaPath, "..") {
		return fmt.Errorf("path traversal not allowed")
	}

	// Allow if DISABLE_PATH_CHECK is set (for development)
	if os.Getenv("DISABLE_PATH_CHECK") == "true" {
		return nil
	}

	// Check if path is within allowed directories
	for _, allowedDir := range allowedMediaDirs {
		allowedAbs, err := filepath.Abs(allowedDir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, allowedAbs) {
			return nil
		}
	}

	return fmt.Errorf("media path outside allowed directories")
}

// SendMessage sends a WhatsApp message with optional media
func (c *Client) SendMessage(messageStore *database.MessageStore, recipient string, message string, mediaPath string, preview *bridgeTypes.LinkPreview) bridgeTypes.SendResult {
	if !c.IsConnected() {
		return bridgeTypes.SendResult{Success: false, Error: "Not connected to WhatsApp"}
	}

	// Create JID for recipient
	var recipientJID types.JID
	var err error

	// Check if recipient is a JID
	isJID := strings.Contains(recipient, "@")

	if isJID {
		// Parse the JID string
		recipientJID, err = types.ParseJID(recipient)
		if err != nil {
			return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("Error parsing JID: %v", err)}
		}
	} else {
		// Create JID from phone number
		recipientJID = types.JID{
			User:   recipient,
			Server: "s.whatsapp.net", // For personal chats
		}
	}

	msg := &waE2E.Message{}

	// Check if we have media to send
	if mediaPath != "" {
		// Validate media path (prevent path traversal)
		if err := validateMediaPath(mediaPath); err != nil {
			return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("Invalid media path: %v", err)}
		}

		// Read media file
		mediaData, err := os.ReadFile(mediaPath)
		if err != nil {
			return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("Error reading media file: %v", err)}
		}

		// Determine media type and mime type based on file extension
		fileExt := strings.ToLower(mediaPath[strings.LastIndex(mediaPath, ".")+1:])
		var mediaType whatsmeow.MediaType
		var mimeType string

		// Handle different media types
		switch fileExt {
		// Image types
		case "jpg", "jpeg":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/jpeg"
		case "png":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/png"
		case "gif":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/gif"
		case "webp":
			mediaType = whatsmeow.MediaImage
			mimeType = "image/webp"

		// Audio types
		case "ogg":
			mediaType = whatsmeow.MediaAudio
			mimeType = "audio/ogg; codecs=opus"

		// Video types
		case "mp4":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/mp4"
		case "avi":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/avi"
		case "mov":
			mediaType = whatsmeow.MediaVideo
			mimeType = "video/quicktime"

		// Document types — explicit MIME so WhatsApp shows them correctly
		// (without these, .docx etc. arrive as .bin on the recipient side).
		case "docx":
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case "doc":
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/msword"
		case "pdf":
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/pdf"
		case "xlsx":
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case "xls":
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/vnd.ms-excel"
		case "pptx":
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		case "txt":
			mediaType = whatsmeow.MediaDocument
			mimeType = "text/plain"

		// Fallback for any other file type
		default:
			mediaType = whatsmeow.MediaDocument
			mimeType = "application/octet-stream"
		}

		// Upload media to WhatsApp servers
		resp, err := c.Upload(context.Background(), mediaData, mediaType)
		if err != nil {
			return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("Error uploading media: %v", err)}
		}

		// Create the appropriate message type based on media type
		switch mediaType {
		case whatsmeow.MediaImage:
			msg.ImageMessage = &waE2E.ImageMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaAudio:
			// Handle ogg audio files
			var seconds uint32 = 30 // Default fallback
			var waveform []byte = nil

			// Try to analyze the ogg file
			if strings.Contains(mimeType, "ogg") {
				analyzedSeconds, analyzedWaveform, err := AnalyzeOggOpus(mediaData)
				if err == nil {
					seconds = analyzedSeconds
					waveform = analyzedWaveform
				} else {
					return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("Failed to analyze Ogg Opus file: %v", err)}
				}
			}

			msg.AudioMessage = &waE2E.AudioMessage{
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
				Seconds:       proto.Uint32(seconds),
				PTT:           proto.Bool(true),
				Waveform:      waveform,
			}
		case whatsmeow.MediaVideo:
			msg.VideoMessage = &waE2E.VideoMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaDocument:
			msg.DocumentMessage = &waE2E.DocumentMessage{
				Title:         proto.String(mediaPath[strings.LastIndex(mediaPath, "/")+1:]),
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		}
	} else {
		msg = buildTextMessage(message, preview)
	}

	// Send message
	sendResp, err := c.Client.SendMessage(context.Background(), recipientJID, msg)
	if err != nil && recipientJID.Server == types.DefaultUserServer {
		// WhatsApp's LID migration: the server rejects some PN-addressed sends
		// (observed: error 400 on LID-migrated accounts, 2026-07-09) while the
		// same message to the mapped @lid JID succeeds. Retry once via the
		// store's PN→LID mapping; sends that succeeded on PN are untouched.
		if lid, lerr := c.Store.LIDs.GetLIDForPN(context.Background(), recipientJID); lerr == nil && !lid.IsEmpty() {
			if lidResp, lidErr := c.Client.SendMessage(context.Background(), lid, msg); lidErr == nil {
				sendResp, err = lidResp, nil
			}
		}
	}
	if err != nil {
		return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("Error sending message: %v", err)}
	}

	_ = messageStore.StoreMessage(
		sendResp.ID, // Use the ID from SendResponse
		recipientJID.String(),
		c.Store.ID.User,       // Use the client's user ID as sender
		c.Store.ID.User,       // SenderName - use our own user ID for sent messages
		outgoingText(msg),     // text body (conversation or extended-text)
		sendResp.Timestamp,    // Use the Timestamp from SendResponse
		true,                  // IsFromMe is true since we are sending this message
		"",
		"",
		"",
		nil, // Replace "" with nil for []byte arguments
		nil, // Replace "" with nil for []byte arguments
		nil, // Replace "" with nil for []byte arguments
		0,
	)

	return bridgeTypes.SendResult{
		Success:   true,
		MessageID: string(sendResp.ID),
		Timestamp: sendResp.Timestamp,
	}
}

// SendReaction sends an emoji reaction to a message
func (c *Client) SendReaction(chatJID, messageID, emoji string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %v", err)
	}

	msgID := types.MessageID(messageID)
	senderJID := c.Store.ID.ToNonAD()

	msg := c.Client.BuildReaction(chat, senderJID, msgID, emoji)
	_, err = c.Client.SendMessage(context.Background(), chat, msg)
	if err != nil {
		return fmt.Errorf("failed to send reaction: %v", err)
	}

	return nil
}

// EditMessage edits a previously sent message
func (c *Client) EditMessage(chatJID, messageID, newContent string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %v", err)
	}

	msgID := types.MessageID(messageID)

	newMsg := &waE2E.Message{
		Conversation: proto.String(newContent),
	}
	msg := c.Client.BuildEdit(chat, msgID, newMsg)
	_, err = c.Client.SendMessage(context.Background(), chat, msg)
	if err != nil {
		return fmt.Errorf("failed to edit message: %v", err)
	}

	return nil
}

// DeleteMessage revokes/deletes a message
func (c *Client) DeleteMessage(chatJID, messageID, senderJID string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %v", err)
	}

	msgID := types.MessageID(messageID)

	var sender types.JID
	if senderJID == "" {
		sender = c.Store.ID.ToNonAD() // own message
	} else {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %v", err)
		}
	}

	msg := c.Client.BuildRevoke(chat, sender, msgID)
	_, err = c.Client.SendMessage(context.Background(), chat, msg)
	if err != nil {
		return fmt.Errorf("failed to delete message: %v", err)
	}

	return nil
}

// GetGroupInfo retrieves group metadata
func (c *Client) GetGroupInfo(groupJID string) (*types.GroupInfo, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}

	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return nil, fmt.Errorf("invalid group JID: %v", err)
	}

	return c.Client.GetGroupInfo(context.Background(), jid)
}

// MarkMessagesRead marks messages as read
func (c *Client) MarkMessagesRead(chatJID string, messageIDs []string, senderJID string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %v", err)
	}

	ids := make([]types.MessageID, len(messageIDs))
	for i, id := range messageIDs {
		ids[i] = types.MessageID(id)
	}

	var sender types.JID
	if senderJID != "" {
		sender, err = types.ParseJID(senderJID)
		if err != nil {
			return fmt.Errorf("invalid sender JID: %v", err)
		}
	}

	return c.Client.MarkRead(context.Background(), ids, time.Now(), chat, sender)
}

// Phase 2: Group Management

// CreateGroup creates a new WhatsApp group
func (c *Client) CreateGroup(name string, participants []string) (*types.GroupInfo, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}

	// Parse participant JIDs
	participantJIDs := make([]types.JID, len(participants))
	for i, p := range participants {
		jid, err := types.ParseJID(p)
		if err != nil {
			return nil, fmt.Errorf("invalid participant JID %s: %v", p, err)
		}
		participantJIDs[i] = jid
	}

	req := whatsmeow.ReqCreateGroup{
		Name:         name,
		Participants: participantJIDs,
	}

	return c.Client.CreateGroup(context.Background(), req)
}

// AddGroupParticipants adds members to a group
func (c *Client) AddGroupParticipants(groupJID string, participants []string) ([]types.GroupParticipant, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return nil, fmt.Errorf("invalid group JID: %v", err)
	}

	participantJIDs := make([]types.JID, len(participants))
	for i, p := range participants {
		jid, err := types.ParseJID(p)
		if err != nil {
			return nil, fmt.Errorf("invalid participant JID %s: %v", p, err)
		}
		participantJIDs[i] = jid
	}

	return c.Client.UpdateGroupParticipants(context.Background(), group, participantJIDs, whatsmeow.ParticipantChangeAdd)
}

// RemoveGroupParticipants removes members from a group
func (c *Client) RemoveGroupParticipants(groupJID string, participants []string) ([]types.GroupParticipant, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return nil, fmt.Errorf("invalid group JID: %v", err)
	}

	participantJIDs := make([]types.JID, len(participants))
	for i, p := range participants {
		jid, err := types.ParseJID(p)
		if err != nil {
			return nil, fmt.Errorf("invalid participant JID %s: %v", p, err)
		}
		participantJIDs[i] = jid
	}

	return c.Client.UpdateGroupParticipants(context.Background(), group, participantJIDs, whatsmeow.ParticipantChangeRemove)
}

// PromoteGroupParticipant promotes a participant to admin
func (c *Client) PromoteGroupParticipant(groupJID string, participant string) ([]types.GroupParticipant, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return nil, fmt.Errorf("invalid group JID: %v", err)
	}

	jid, err := types.ParseJID(participant)
	if err != nil {
		return nil, fmt.Errorf("invalid participant JID: %v", err)
	}

	return c.Client.UpdateGroupParticipants(context.Background(), group, []types.JID{jid}, whatsmeow.ParticipantChangePromote)
}

// DemoteGroupParticipant demotes an admin to regular participant
func (c *Client) DemoteGroupParticipant(groupJID string, participant string) ([]types.GroupParticipant, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return nil, fmt.Errorf("invalid group JID: %v", err)
	}

	jid, err := types.ParseJID(participant)
	if err != nil {
		return nil, fmt.Errorf("invalid participant JID: %v", err)
	}

	return c.Client.UpdateGroupParticipants(context.Background(), group, []types.JID{jid}, whatsmeow.ParticipantChangeDemote)
}

// LeaveGroup leaves a WhatsApp group
func (c *Client) LeaveGroup(groupJID string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return fmt.Errorf("invalid group JID: %v", err)
	}

	return c.Client.LeaveGroup(context.Background(), group)
}

// SetGroupName updates the group name
func (c *Client) SetGroupName(groupJID string, name string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return fmt.Errorf("invalid group JID: %v", err)
	}

	return c.Client.SetGroupName(context.Background(), group, name)
}

// SetGroupTopic updates the group description/topic
func (c *Client) SetGroupTopic(groupJID string, topic string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	group, err := types.ParseJID(groupJID)
	if err != nil {
		return fmt.Errorf("invalid group JID: %v", err)
	}

	return c.Client.SetGroupTopic(context.Background(), group, "", "", topic)
}

// Phase 3: Polls

// CreatePoll creates and sends a poll to a chat
func (c *Client) CreatePoll(chatJID string, question string, options []string, multiSelect bool) (bridgeTypes.SendResult, error) {
	if !c.IsConnected() {
		return bridgeTypes.SendResult{Success: false, Error: "not connected to WhatsApp"}, fmt.Errorf("not connected to WhatsApp")
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("invalid chat JID: %v", err)}, err
	}

	// Determine selectable count based on multiSelect
	selectableCount := 1
	if multiSelect {
		selectableCount = len(options)
	}

	// Build poll creation message
	pollMsg := c.Client.BuildPollCreation(question, options, selectableCount)

	// Send the poll
	resp, err := c.Client.SendMessage(context.Background(), chat, pollMsg)
	if err != nil {
		return bridgeTypes.SendResult{Success: false, Error: fmt.Sprintf("failed to send poll: %v", err)}, err
	}

	return bridgeTypes.SendResult{
		Success:   true,
		MessageID: string(resp.ID),
		Timestamp: resp.Timestamp,
	}, nil
}

// Phase 4: On-Demand History Request

// RequestChatHistory requests older messages for a specific chat.
// The response will come asynchronously via the HistorySync event handler.
// This requires knowing the oldest message in the chat to request messages before it.
func (c *Client) RequestChatHistory(chatJID string, oldestMsgID string, oldestMsgFromMe bool, oldestMsgTimestamp int64, count int) error {
	if !c.IsConnected() {
		return fmt.Errorf("not connected to WhatsApp")
	}

	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat JID: %v", err)
	}

	// Create MessageInfo for the oldest known message
	msgInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			IsFromMe: oldestMsgFromMe,
		},
		ID:        types.MessageID(oldestMsgID),
		Timestamp: time.UnixMilli(oldestMsgTimestamp),
	}

	// If this is a group chat, we need the sender
	if chat.Server == "g.us" && !oldestMsgFromMe {
		// For group chats, we'd need the sender JID
		// This is a limitation - we might need to store sender info
		msgInfo.MessageSource.Sender = chat // Use chat as placeholder
	} else {
		msgInfo.MessageSource.Sender = c.Store.ID.ToNonAD()
	}

	// Build the history sync request
	// Recommended count is 50 messages at a time
	if count <= 0 || count > 50 {
		count = 50
	}

	msg := c.Client.BuildHistorySyncRequest(msgInfo, count)

	// Send the request to the phone
	// The response comes as events.HistorySync with type ON_DEMAND
	_, err = c.Client.SendMessage(context.Background(), chat, msg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return fmt.Errorf("failed to send history request: %v", err)
	}

	return nil
}

// buildTextMessage constructs the outgoing text message. When a valid link
// preview is supplied it produces an ExtendedTextMessage (rich link/video
// card); otherwise — or when the preview is invalid — it falls back to a plain
// Conversation. Pure: no client, no network; unit-testable. A card is built
// only when the preview has a Title and its MatchedText is a literal substring
// of the body (whatsmeow MatchedText invariant).
func buildTextMessage(message string, p *bridgeTypes.LinkPreview) *waE2E.Message {
	msg := &waE2E.Message{}
	if p == nil || p.Title == "" || p.MatchedText == "" || !strings.Contains(message, p.MatchedText) {
		msg.Conversation = proto.String(message)
		return msg
	}
	ext := &waE2E.ExtendedTextMessage{
		Text:         proto.String(message),
		MatchedText:  proto.String(p.MatchedText),
		// NOTE: this whatsmeow version exposes no CanonicalURL field on
		// ExtendedTextMessage; MatchedText + Text are sufficient for the card.
		Title:        proto.String(p.Title),
	}
	if p.Description != "" {
		ext.Description = proto.String(p.Description)
	}
	if p.JPEGThumbnail != "" {
		if raw, err := base64.StdEncoding.DecodeString(p.JPEGThumbnail); err == nil && len(raw) > 0 {
			ext.JPEGThumbnail = raw
			if p.ThumbnailW > 0 {
				ext.ThumbnailWidth = proto.Uint32(p.ThumbnailW)
			}
			if p.ThumbnailH > 0 {
				ext.ThumbnailHeight = proto.Uint32(p.ThumbnailH)
			}
		}
	}
	if p.PreviewType == "video" {
		ext.PreviewType = waE2E.ExtendedTextMessage_VIDEO.Enum()
	}
	msg.ExtendedTextMessage = ext
	return msg
}

// outgoingText returns the human-readable body of an outgoing message for
// storage: the Conversation text, or the ExtendedTextMessage text when a link
// preview was used (GetConversation() is empty in that case). Media keeps the
// prior empty-content behavior.
func outgoingText(msg *waE2E.Message) string {
	if c := msg.GetConversation(); c != "" {
		return c
	}
	return msg.GetExtendedTextMessage().GetText()
}