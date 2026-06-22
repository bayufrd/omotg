package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhatsAppInboundPayloadNormalize(t *testing.T) {
	payload := WhatsAppInboundPayload{
		Command:    "Mulai session baru",
		RawMessage: ". Mulai session baru",
		User: WhatsAppInboundUser{
			WANumber: "628123",
			SenderPN: "628123",
			Name:     "Dokument User",
			ChatID:   "1203630@g.us",
			IsGroup:  true,
		},
		Message: WhatsAppInboundMessage{
			ID:   "msg-1",
			Body: "ignored body",
		},
		Context: WhatsAppInboundContext{RemoteJID: "1203630@g.us"},
	}

	normalized := payload.Normalize()
	if normalized.ConversationKey != "1203630@g.us" {
		t.Fatalf("ConversationKey = %q", normalized.ConversationKey)
	}
	if normalized.ReplyTarget != "628123" {
		t.Fatalf("ReplyTarget = %q", normalized.ReplyTarget)
	}
	if normalized.CommandText != "Mulai session baru" {
		t.Fatalf("CommandText = %q", normalized.CommandText)
	}
	if normalized.PromptText != "Mulai session baru" {
		t.Fatalf("PromptText = %q", normalized.PromptText)
	}
}

func TestWhatsAppInboundPayloadNormalizeFallback(t *testing.T) {
	payload := WhatsAppInboundPayload{
		RawMessage: ". halo",
		User: WhatsAppInboundUser{
			WANumber: "628123",
		},
		Message: WhatsAppInboundMessage{Body: "halo dari body"},
	}

	normalized := payload.Normalize()
	if normalized.PromptText != "halo dari body" {
		t.Fatalf("PromptText = %q", normalized.PromptText)
	}
	if normalized.ConversationKey != "628123" {
		t.Fatalf("ConversationKey = %q", normalized.ConversationKey)
	}
}

func TestWhatsAppInboundPayloadValidate(t *testing.T) {
	payload := WhatsAppInboundPayload{
		User:    WhatsAppInboundUser{WANumber: "628123"},
		Message: WhatsAppInboundMessage{Body: "halo"},
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestWhatsAppInboundPayloadValidateMissingIdentifiers(t *testing.T) {
	payload := WhatsAppInboundPayload{
		Message: WhatsAppInboundMessage{Body: "halo"},
	}
	if err := payload.Validate(); err == nil {
		t.Fatal("Validate() expected error")
	}
}

func TestParseWhatsAppCommand(t *testing.T) {
	tests := []struct {
		name        string
		commandText string
		fallback    string
		wantType    CommandType
		wantSession SessionAction
		wantPrompt  string
	}{
		{name: "natural language session new", commandText: "Mulai session baru", wantType: CmdSession, wantSession: SessNew},
		{name: "help alias", commandText: "bantuan", wantType: CmdHelp},
		{name: "status", commandText: "status", wantType: CmdStatus},
		{name: "fallback body", fallback: "/logs 10", wantType: CmdLogs},
		{name: "free prompt", commandText: "tolong cek deployment", wantType: CmdFreeChat, wantPrompt: "tolong cek deployment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWhatsAppCommand(tt.commandText, tt.fallback)
			if got.Type != tt.wantType {
				t.Fatalf("Type = %v, want %v", got.Type, tt.wantType)
			}
			if got.SessionAct != tt.wantSession {
				t.Fatalf("SessionAct = %v, want %v", got.SessionAct, tt.wantSession)
			}
			if tt.wantPrompt != "" && got.Prompt != tt.wantPrompt {
				t.Fatalf("Prompt = %q, want %q", got.Prompt, tt.wantPrompt)
			}
		})
	}
}

func TestWhatsAppSenderSendText(t *testing.T) {
	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	sender := NewWhatsAppSender(server.URL, "/api/whatsapp/send-personal", "wa-token")
	result, err := sender.SendText(context.Background(), "628123", "Halo")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if result != "Message sent to WhatsApp number 628123" {
		t.Fatalf("result = %q", result)
	}
	if gotAuth != "Bearer wa-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"nomor":"628123"`) {
		t.Fatalf("request body missing nomor: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"pesan":"Halo"`) {
		t.Fatalf("request body missing pesan: %s", gotBody)
	}
}

func TestWhatsAppSenderSendError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()

	sender := NewWhatsAppSender(server.URL, "/api/whatsapp/send-personal", "")
	_, err := sender.SendText(context.Background(), "628123", "Halo")
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("SendText() error = %v", err)
	}
}
