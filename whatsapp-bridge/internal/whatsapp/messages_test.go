package whatsapp

import (
	"bytes"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	bridgeTypes "whatsapp-bridge/internal/types"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestValidateMediaPath(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantErr     bool
		errContains string
	}{
		// Valid paths
		{"empty path", "", false, ""},
		{"allowed /app/media", "/app/media/file.jpg", false, ""},
		{"allowed /app/store", "/app/store/data.db", false, ""},
		{"allowed /tmp", "/tmp/upload.png", false, ""},

		// Path traversal attempts (should fail)
		{"path traversal ../", "/app/media/../etc/passwd", true, "traversal"},
		{"path traversal multiple", "/app/media/../../etc/passwd", true, "traversal"},
		{"path traversal in middle", "/app/../../../etc/passwd", true, "traversal"},

		// Outside allowed directories (should fail when DISABLE_PATH_CHECK is not set)
		{"outside allowed /etc", "/etc/passwd", true, "outside allowed"},
		{"outside allowed /home", "/home/user/file.txt", true, "outside allowed"},
		{"outside allowed /var", "/var/log/syslog", true, "outside allowed"},
	}

	// Ensure DISABLE_PATH_CHECK is not set for these tests
	os.Unsetenv("DISABLE_PATH_CHECK")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMediaPath(tt.path)

			if tt.wantErr {
				if err == nil {
					t.Errorf("validateMediaPath(%s) = nil, want error containing %q", tt.path, tt.errContains)
					return
				}
				if tt.errContains != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errContains)) {
					t.Errorf("validateMediaPath(%s) error = %v, want error containing %q", tt.path, err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("validateMediaPath(%s) = %v, want nil", tt.path, err)
				}
			}
		})
	}
}

func TestValidateMediaPath_DisableCheck(t *testing.T) {
	// Save and restore env var
	original := os.Getenv("DISABLE_PATH_CHECK")
	defer os.Setenv("DISABLE_PATH_CHECK", original)

	// Enable path check bypass
	os.Setenv("DISABLE_PATH_CHECK", "true")

	// Should now allow paths outside allowed directories
	// Note: Path traversal attempts still blocked
	err := validateMediaPath("/home/user/file.txt")
	if err != nil {
		t.Errorf("With DISABLE_PATH_CHECK=true, validateMediaPath should allow external paths, got: %v", err)
	}
}

func TestValidateMediaPath_TraversalAlwaysBlocked(t *testing.T) {
	// Save and restore env var
	original := os.Getenv("DISABLE_PATH_CHECK")
	defer os.Setenv("DISABLE_PATH_CHECK", original)

	// Even with DISABLE_PATH_CHECK=true, path traversal should be blocked
	os.Setenv("DISABLE_PATH_CHECK", "true")

	err := validateMediaPath("/app/media/../../../etc/passwd")
	if err == nil {
		t.Error("Path traversal should be blocked even with DISABLE_PATH_CHECK=true")
	}
}

// ── S4 link-preview: buildTextMessage / outgoingText (pure) ──

func TestBuildTextMessage_NoPreviewPlainText(t *testing.T) {
	msg := buildTextMessage("hello https://x.com", nil)
	if msg.GetConversation() != "hello https://x.com" {
		t.Errorf("expected plain Conversation, got %q", msg.GetConversation())
	}
	if msg.GetExtendedTextMessage() != nil {
		t.Error("expected no ExtendedTextMessage when preview is nil")
	}
}

func TestBuildTextMessage_StandardLink(t *testing.T) {
	p := &bridgeTypes.LinkPreview{
		MatchedText:  "https://example.com/p",
		CanonicalURL: "https://example.com/p",
		Title:        "Web Page",
		Description:  "desc",
		PreviewType:  "link",
	}
	msg := buildTextMessage("see https://example.com/p", p)
	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		t.Fatal("expected ExtendedTextMessage")
	}
	if ext.GetText() != "see https://example.com/p" {
		t.Errorf("text=%q", ext.GetText())
	}
	if ext.GetMatchedText() != "https://example.com/p" {
		t.Errorf("matched=%q", ext.GetMatchedText())
	}
	if ext.GetTitle() != "Web Page" {
		t.Errorf("title=%q", ext.GetTitle())
	}
	if ext.GetDescription() != "desc" {
		t.Errorf("desc=%q", ext.GetDescription())
	}
	if ext.GetPreviewType() != waE2E.ExtendedTextMessage_NONE {
		t.Errorf("previewType=%v want NONE", ext.GetPreviewType())
	}
	if msg.GetConversation() != "" {
		t.Error("should not set Conversation when a card is built")
	}
}

func TestBuildTextMessage_VideoPreviewType(t *testing.T) {
	for _, url := range []string{"https://youtu.be/abc", "https://vimeo.com/123"} {
		p := &bridgeTypes.LinkPreview{MatchedText: url, CanonicalURL: url, Title: "V", PreviewType: "video"}
		ext := buildTextMessage("watch "+url, p).GetExtendedTextMessage()
		if ext == nil {
			t.Fatalf("expected ExtendedTextMessage for %s", url)
		}
		if ext.GetPreviewType() != waE2E.ExtendedTextMessage_VIDEO {
			t.Errorf("%s previewType=%v want VIDEO", url, ext.GetPreviewType())
		}
	}
}

func TestBuildTextMessage_MatchedTextNotInBodyFallsBack(t *testing.T) {
	p := &bridgeTypes.LinkPreview{MatchedText: "https://other.com", CanonicalURL: "https://other.com", Title: "X", PreviewType: "link"}
	msg := buildTextMessage("body without that url https://present.com", p)
	if msg.GetExtendedTextMessage() != nil {
		t.Error("expected fallback to plain text when matched_text not a substring")
	}
	if msg.GetConversation() != "body without that url https://present.com" {
		t.Errorf("expected plain Conversation, got %q", msg.GetConversation())
	}
}

func TestBuildTextMessage_EmptyTitleFallsBack(t *testing.T) {
	p := &bridgeTypes.LinkPreview{MatchedText: "https://x.com", CanonicalURL: "https://x.com", Title: "", PreviewType: "link"}
	msg := buildTextMessage("see https://x.com", p)
	if msg.GetExtendedTextMessage() != nil {
		t.Error("expected fallback when title empty")
	}
	if msg.GetConversation() != "see https://x.com" {
		t.Errorf("conv=%q", msg.GetConversation())
	}
}

func TestBuildTextMessage_ValidThumbnailEmbedded(t *testing.T) {
	raw := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0x02}
	p := &bridgeTypes.LinkPreview{
		MatchedText: "https://x.com", CanonicalURL: "https://x.com", Title: "T",
		JPEGThumbnail: base64.StdEncoding.EncodeToString(raw),
		ThumbnailW:    320, ThumbnailH: 180, PreviewType: "link",
	}
	ext := buildTextMessage("see https://x.com", p).GetExtendedTextMessage()
	if ext == nil {
		t.Fatal("expected ext")
	}
	if !bytes.Equal(ext.GetJPEGThumbnail(), raw) {
		t.Errorf("thumbnail bytes mismatch: %v", ext.GetJPEGThumbnail())
	}
	if ext.GetThumbnailWidth() != 320 || ext.GetThumbnailHeight() != 180 {
		t.Errorf("dims=%dx%d", ext.GetThumbnailWidth(), ext.GetThumbnailHeight())
	}
}

func TestBuildTextMessage_InvalidThumbnailStillSendsCard(t *testing.T) {
	p := &bridgeTypes.LinkPreview{
		MatchedText: "https://x.com", CanonicalURL: "https://x.com", Title: "T",
		JPEGThumbnail: "!!!not-base64!!!", PreviewType: "link",
	}
	ext := buildTextMessage("see https://x.com", p).GetExtendedTextMessage()
	if ext == nil {
		t.Fatal("expected card even with bad thumbnail")
	}
	if ext.GetJPEGThumbnail() != nil {
		t.Errorf("expected nil thumbnail on bad base64, got %v", ext.GetJPEGThumbnail())
	}
	if ext.GetTitle() != "T" {
		t.Error("card fields should still be set")
	}
}

func TestOutgoingText(t *testing.T) {
	if got := outgoingText(buildTextMessage("hello", nil)); got != "hello" {
		t.Errorf("plain outgoingText=%q", got)
	}
	p := &bridgeTypes.LinkPreview{MatchedText: "https://x.com", CanonicalURL: "https://x.com", Title: "T"}
	if got := outgoingText(buildTextMessage("body https://x.com", p)); got != "body https://x.com" {
		t.Errorf("card outgoingText=%q", got)
	}
}
