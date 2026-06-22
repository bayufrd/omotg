package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type WhatsAppInboundPayload struct {
	Source     string                 `json:"source"`
	Service    string                 `json:"service"`
	Command    string                 `json:"command"`
	RawMessage string                 `json:"rawMessage"`
	User       WhatsAppInboundUser    `json:"user"`
	Message    WhatsAppInboundMessage `json:"message"`
	Context    WhatsAppInboundContext `json:"context"`
}

type WhatsAppInboundUser struct {
	WANumber string `json:"waNumber"`
	SenderPN string `json:"senderPn"`
	Name     string `json:"name"`
	ChatID   string `json:"chatId"`
	IsGroup  bool   `json:"isGroup"`
}

type WhatsAppInboundMessage struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Body      string `json:"body"`
}

type WhatsAppInboundContext struct {
	RemoteJID string `json:"remoteJid"`
}

type NormalizedWhatsAppMessage struct {
	ConversationKey string
	ReplyTarget     string
	CommandText     string
	PromptText      string
	RawText         string
	DisplayName     string
	ChatID          string
	WANumber        string
	SenderPN        string
	MessageID       string
	RemoteJID       string
	IsGroup         bool
}

func (p WhatsAppInboundPayload) Normalize() NormalizedWhatsAppMessage {
	command := strings.TrimSpace(p.Command)
	body := strings.TrimSpace(p.Message.Body)
	raw := strings.TrimSpace(p.RawMessage)
	prompt := command
	if prompt == "" {
		switch {
		case body != "":
			prompt = body
		case raw != "":
			prompt = raw
		}
	}

	key := strings.TrimSpace(p.User.WANumber)
	if p.User.IsGroup {
		key = strings.TrimSpace(p.User.ChatID)
	}
	if key == "" {
		key = strings.TrimSpace(p.Context.RemoteJID)
	}
	if key == "" {
		key = strings.TrimSpace(p.User.SenderPN)
	}

	replyTarget := strings.TrimSpace(p.User.WANumber)
	if replyTarget == "" {
		replyTarget = strings.TrimSpace(p.User.SenderPN)
	}

	return NormalizedWhatsAppMessage{
		ConversationKey: key,
		ReplyTarget:     replyTarget,
		CommandText:     command,
		PromptText:      prompt,
		RawText:         raw,
		DisplayName:     strings.TrimSpace(p.User.Name),
		ChatID:          strings.TrimSpace(p.User.ChatID),
		WANumber:        strings.TrimSpace(p.User.WANumber),
		SenderPN:        strings.TrimSpace(p.User.SenderPN),
		MessageID:       strings.TrimSpace(p.Message.ID),
		RemoteJID:       strings.TrimSpace(p.Context.RemoteJID),
		IsGroup:         p.User.IsGroup,
	}
}

func (p WhatsAppInboundPayload) Validate() error {
	if strings.TrimSpace(p.User.WANumber) == "" && strings.TrimSpace(p.User.SenderPN) == "" {
		return fmt.Errorf("missing user.waNumber or user.senderPn")
	}
	if strings.TrimSpace(p.User.ChatID) == "" && p.User.IsGroup {
		return fmt.Errorf("missing user.chatId for group message")
	}
	if strings.TrimSpace(p.Command) == "" && strings.TrimSpace(p.Message.Body) == "" && strings.TrimSpace(p.RawMessage) == "" {
		return fmt.Errorf("missing command, message.body, and rawMessage")
	}
	return nil
}

func ParseWhatsAppCommand(commandText, fallbackText string) ParsedCommand {
	commandText = strings.TrimSpace(commandText)
	if commandText != "" {
		lower := strings.ToLower(commandText)
		switch {
		case lower == "mulai session baru":
			return ParseMessage("/session new")
		case lower == "session baru":
			return ParseMessage("/session new")
		case lower == "bantuan":
			return ParseMessage("/help")
		case lower == "mulai":
			return ParseMessage("/start")
		case lower == "status":
			return ParseMessage("/status")
		case strings.HasPrefix(lower, "deploy "):
			return ParseMessage("/" + lower)
		case strings.HasPrefix(lower, "logs"):
			return ParseMessage("/" + lower)
		case strings.HasPrefix(lower, "session ") || lower == "session":
			return ParseMessage("/" + lower)
		}
		return ParsedCommand{Type: CmdFreeChat, RawText: commandText, Prompt: commandText}
	}
	return ParseMessage(fallbackText)
}

type WhatsAppSendPersonalRequest struct {
	Nomor string `json:"nomor,omitempty"`
	Pesan string `json:"pesan,omitempty"`

	Number   string `json:"number,omitempty"`
	Message  string `json:"message,omitempty"`
	Media    string `json:"media,omitempty"`
	Lampiran string `json:"lampiran,omitempty"`
}

type WhatsAppSender struct {
	BaseURL    string
	SendPath   string
	APIToken   string
	HTTPClient *http.Client
}

func NewWhatsAppSender(baseURL, sendPath, apiToken string) *WhatsAppSender {
	if sendPath == "" {
		sendPath = "/api/whatsapp/send-personal"
	}
	return &WhatsAppSender{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		SendPath:   sendPath,
		APIToken:   apiToken,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *WhatsAppSender) SendURL() string {
	if s.BaseURL == "" {
		return ""
	}
	path := s.SendPath
	if path == "" {
		path = "/api/whatsapp/send-personal"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return s.BaseURL + path
}

func (s *WhatsAppSender) SendText(ctx context.Context, number, message string) (string, error) {
	payload := WhatsAppSendPersonalRequest{
		Nomor:   number,
		Pesan:   message,
		Number:  number,
		Message: message,
	}
	return s.Send(ctx, payload)
}

func (s *WhatsAppSender) Send(ctx context.Context, payload WhatsAppSendPersonalRequest) (string, error) {
	if s.SendURL() == "" {
		return "", fmt.Errorf("whatsapp send url not configured")
	}
	if strings.TrimSpace(payload.Nomor) == "" && strings.TrimSpace(payload.Number) == "" {
		return "", fmt.Errorf("missing nomor or number")
	}
	if strings.TrimSpace(payload.Pesan) == "" && strings.TrimSpace(payload.Message) == "" {
		return "", fmt.Errorf("missing pesan or message")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.SendURL(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIToken)
	}

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whatsapp api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("whatsapp error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	number := payload.Nomor
	if number == "" {
		number = payload.Number
	}
	return fmt.Sprintf("Message sent to WhatsApp number %s", number), nil
}
