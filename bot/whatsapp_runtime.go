package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"omotg/db"
)

type WhatsAppBotConfig struct {
	InboundSecret  string
	ServiceSecret  string
	AllowedChatIDs []string
	SessionTimeout time.Duration
}

type WhatsAppBot struct {
	config   *WhatsAppBotConfig
	ocClient SessionClient
	sessions *SessionMap
	sender   *WhatsAppSender
	sessionStore *db.SessionStore
	messageStore *db.MessageStore
}

func NewWhatsAppBot(cfg *WhatsAppBotConfig, ocClient SessionClient, sessions *SessionMap, sender *WhatsAppSender, sessionStore *db.SessionStore, messageStore *db.MessageStore) *WhatsAppBot {
	return &WhatsAppBot{
		config:   cfg,
		ocClient: ocClient,
		sessions: sessions,
		sender:   sender,
		sessionStore: sessionStore,
		messageStore: messageStore,
	}
}

type whatsappReplyEnvelope struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Reply struct {
			Message string `json:"message"`
		} `json:"reply"`
		WhatsAppReply struct {
			Message string `json:"message"`
		} `json:"whatsappReply"`
	} `json:"data"`
}

type WhatsAppManualSendRequest struct {
	Number  string `json:"number"`
	Message string `json:"message"`
}

func (r WhatsAppManualSendRequest) Validate() error {
	if strings.TrimSpace(r.Number) == "" {
		return fmt.Errorf("missing number")
	}
	if strings.TrimSpace(r.Message) == "" {
		return fmt.Errorf("missing message")
	}
	return nil
}

func (b *WhatsAppBot) HandleInbound(w http.ResponseWriter, r *http.Request) {
	slog.Info("whatsapp inbound request received", "remote_addr", r.RemoteAddr)
	if !b.authorized(r) {
		slog.Warn("whatsapp inbound auth failed")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	slog.Info("whatsapp inbound auth success")

	var payload WhatsAppInboundPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		slog.Error("whatsapp inbound decode failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := payload.Validate(); err != nil {
		slog.Warn("whatsapp inbound validate failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	normalized := payload.Normalize()
	if !b.isAllowed(normalized) {
		slog.Warn("whatsapp inbound identifier not allowed", "conversation_key", normalized.ConversationKey, "chat_id", normalized.ChatID, "wa_number", normalized.WANumber)
		b.writeReply(w, http.StatusOK, "Maaf, kamu tidak punya akses.")
		return
	}

	cmd := ParseWhatsAppCommand(normalized.CommandText, normalized.PromptText)
	conversationID := hashConversationKey(normalized.ConversationKey)
	slog.Info("whatsapp session resolved input",
		"conversation_key", normalized.ConversationKey,
		"conversation_id", conversationID,
		"is_group", normalized.IsGroup,
		"command_type", cmd.Type,
	)

	switch cmd.Type {
	case CmdStart:
		b.writeReply(w, http.StatusOK, "Halo! OMOTG WhatsApp bridge siap. Kirim help untuk bantuan.")
		return
	case CmdHelp:
		b.writeReply(w, http.StatusOK, WhatsAppHelpText())
		return
	case CmdSession:
		text, err := b.handleSessionCommand(r.Context(), conversationID, cmd)
		if err != nil {
			slog.Error("whatsapp session command failed", "error", err)
			b.writeReply(w, http.StatusInternalServerError, "Gagal memproses session.")
			return
		}
		b.writeReply(w, http.StatusOK, text)
		return
	case CmdUnknown:
		b.writeReply(w, http.StatusOK, "Perintah tidak dikenal. Kirim help untuk bantuan.")
		return
	case CmdStatus, CmdDeploy, CmdLogs, CmdFreeChat:
		// continue below
	}

	sessionID, isNew, err := b.resolveConversationSession(r.Context(), conversationID)
	if err != nil {
		slog.Error("whatsapp resolve session failed", "error", err)
		b.writeReply(w, http.StatusInternalServerError, "Gagal membuat session.")
		return
	}
	ack := "Permintaan diproses. Hasil akan dikirim ke WhatsApp."
	if isNew {
		ack = fmt.Sprintf("Session baru dibuat: %s. Permintaan diproses.", sessionID)
	}
	if cmd.Type == CmdDeploy {
		ack = fmt.Sprintf("Menjalankan: %s", cmd.RawText)
	}

	// Log inbound message
	b.logMessage(r.Context(), sessionID, "inbound", normalized.MessageID, normalized.PromptText, "received", nil)

	if cmd.Type == CmdStatus || cmd.Type == CmdDeploy || cmd.Type == CmdLogs {
		ctx, cancel := context.WithTimeout(r.Context(), b.config.SessionTimeout)
		defer cancel()
		result, err := b.ocClient.SendMessage(ctx, sessionID, buildWhatsAppPrompt(normalized, sessionID, cmd.Prompt))
		if err != nil {
			slog.Error("whatsapp sync OpenCode request failed", "error", err)
			errMsg := fmt.Sprintf("Gagal: %s", err)
			b.logMessage(ctx, sessionID, "outbound", "out-"+normalized.MessageID, errMsg, "failed", err)
			b.writeReply(w, http.StatusOK, errMsg)
			return
		}
		slog.Info("whatsapp sync OpenCode request finished", "session_id", sessionID)
		b.logMessage(ctx, sessionID, "outbound", "out-"+normalized.MessageID, result, "sent", nil)
		b.writeReply(w, http.StatusOK, result)
		return
	}

	go b.processAsyncPrompt(context.Background(), normalized, sessionID, cmd.Prompt)
	b.writeReply(w, http.StatusOK, ack)
}

func (b *WhatsAppBot) HandleManualSend(w http.ResponseWriter, r *http.Request) {
	slog.Info("whatsapp manual send request received", "remote_addr", r.RemoteAddr)
	if !b.authorized(r) {
		slog.Warn("whatsapp manual send auth failed")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req WhatsAppManualSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("whatsapp manual send decode failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
		slog.Warn("whatsapp manual send validate failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := b.sender.SendText(ctx, req.Number, req.Message)
	if err != nil {
		slog.Error("whatsapp manual send failed", "error", err, "number", req.Number)
		b.writeReply(w, http.StatusBadGateway, fmt.Sprintf("Gagal kirim pesan: %s", err))
		return
	}
	slog.Info("whatsapp manual send success", "number", req.Number)
	b.writeReply(w, http.StatusOK, result)
}

func (b *WhatsAppBot) processAsyncPrompt(ctx context.Context, msg NormalizedWhatsAppMessage, sessionID, prompt string) {
	ctx, cancel := context.WithTimeout(ctx, b.config.SessionTimeout)
	defer cancel()

	slog.Info("whatsapp OpenCode request start", "session_id", sessionID, "conversation_key", msg.ConversationKey)
	b.sessions.Renew(sessionID)
	result, err := b.ocClient.SendMessage(ctx, sessionID, buildWhatsAppPrompt(msg, sessionID, prompt))
	if err != nil {
		slog.Error("whatsapp OpenCode request failed", "error", err, "session_id", sessionID)
		errMsg := fmt.Sprintf("❌ Gagal memproses pesan: %s", err)
		_, sendErr := b.sender.SendText(context.Background(), msg.ReplyTarget, errMsg)
		if sendErr != nil {
			slog.Error("whatsapp outbound send failed", "error", sendErr, "target", msg.ReplyTarget)
			b.logMessage(context.Background(), sessionID, "outbound", "out-"+msg.MessageID, errMsg, "failed", sendErr)
		} else {
			b.logMessage(context.Background(), sessionID, "outbound", "out-"+msg.MessageID, errMsg, "sent", nil)
		}
		return
	}
	if strings.TrimSpace(result) == "" {
		result = "OK"
	}
	_, sendErr := b.sender.SendText(context.Background(), msg.ReplyTarget, result)
	if sendErr != nil {
		slog.Error("whatsapp outbound send failed", "error", sendErr, "target", msg.ReplyTarget)
		b.logMessage(context.Background(), sessionID, "outbound", "out-"+msg.MessageID, result, "failed", sendErr)
		return
	}
	slog.Info("whatsapp outbound send success", "target", msg.ReplyTarget, "session_id", sessionID)
	b.logMessage(context.Background(), sessionID, "outbound", "out-"+msg.MessageID, result, "sent", nil)
}

// logMessage logs a message to the database if messageStore is enabled
func (b *WhatsAppBot) logMessage(ctx context.Context, sessionID, direction, messageID, content, status string, errVal error) {
	if b.messageStore == nil {
		return
	}
	var errStr *string
	if errVal != nil {
		s := errVal.Error()
		errStr = &s
	}
	msg := &db.Message{
		SessionID: sessionID,
		Channel:   "whatsapp",
		Direction: direction,
		MessageID: messageID,
		Content:   content,
		Status:    status,
		Error:     errStr,
		CreatedAt: time.Now(),
	}
	if err := b.messageStore.SaveMessage(ctx, msg); err != nil {
		slog.Error("failed to save message to db", "error", err)
	}
}

// saveSession persists a session to the database if sessionStore is enabled
func (b *WhatsAppBot) saveSession(ctx context.Context, sessionID string, conversationID int64) {
	if b.sessionStore == nil {
		return
	}
	dbSess := &db.Session{
		ID:                sessionID,
		Channel:           "whatsapp",
		ChatID:            fmt.Sprintf("%d", conversationID),
		ProviderSessionID: sessionID,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := b.sessionStore.CreateSession(ctx, dbSess); err != nil {
		slog.Error("failed to save session to db", "error", err)
	}
}

// deleteSession removes a session from the database if sessionStore is enabled
func (b *WhatsAppBot) deleteSession(ctx context.Context, sessionID string) {
	if b.sessionStore == nil {
		return
	}
	if err := b.sessionStore.DeleteSession(ctx, sessionID); err != nil {
		slog.Error("failed to delete session from db", "error", err)
	}
}

func (b *WhatsAppBot) resolveConversationSession(ctx context.Context, conversationID int64) (string, bool, error) {
	if entry := b.sessions.GetCurrentSession(conversationID); entry != nil {
		slog.Info("whatsapp session reused", "conversation_id", conversationID, "session_id", entry.SessionID)
		return entry.SessionID, false, nil
	}
	sessionID, err := b.ocClient.CreateSession(ctx)
	if err != nil {
		return "", false, err
	}
	b.sessions.Store(sessionID, conversationID, 0, 0)
	b.saveSession(ctx, sessionID, conversationID)
	slog.Info("whatsapp session created", "conversation_id", conversationID, "session_id", sessionID)
	return sessionID, true, nil
}

func (b *WhatsAppBot) handleSessionCommand(ctx context.Context, conversationID int64, cmd ParsedCommand) (string, error) {
	switch cmd.SessionAct {
	case SessList:
		sessions := b.sessions.ListChatSessions(conversationID)
		if len(sessions) == 0 {
			return "Belum ada session untuk percakapan ini.", nil
		}
		lines := []string{"Daftar session:"}
		for i, entry := range sessions {
			marker := "-"
			if current := b.sessions.GetCurrentSession(conversationID); current != nil && current.SessionID == entry.SessionID {
				marker = "*"
			}
			lines = append(lines, fmt.Sprintf("%s %d. %s", marker, i+1, entry.SessionID))
		}
		return strings.Join(lines, "\n"), nil
	case SessSwitch:
		if cmd.SessionArg == "" {
			return "Gunakan: session switch <id>", nil
		}
		entry, ok := b.sessions.Load(cmd.SessionArg)
		if !ok || entry.ChatID != conversationID {
			return "Session tidak ditemukan.", nil
		}
		b.sessions.SetCurrentSession(conversationID, cmd.SessionArg)
		return fmt.Sprintf("Session aktif diganti ke %s", cmd.SessionArg), nil
	case SessDelete:
		if cmd.SessionArg == "" {
			return "Gunakan: session delete <id>", nil
		}
		entry, ok := b.sessions.Load(cmd.SessionArg)
		if !ok || entry.ChatID != conversationID {
			return "Session tidak ditemukan.", nil
		}
		if err := b.ocClient.DeleteSession(ctx, cmd.SessionArg); err != nil {
			return "", err
		}
		b.sessions.Delete(cmd.SessionArg)
		b.deleteSession(ctx, cmd.SessionArg)
		return fmt.Sprintf("Session %s dihapus.", cmd.SessionArg), nil
	case SessNew:
		sessionID, err := b.ocClient.CreateSession(ctx)
		if err != nil {
			return "", err
		}
		b.sessions.Store(sessionID, conversationID, 0, 0)
		b.saveSession(ctx, sessionID, conversationID)
		if strings.TrimSpace(cmd.Prompt) == "" {
			return fmt.Sprintf("Session baru dibuat: %s", sessionID), nil
		}
		result, err := b.ocClient.SendMessage(ctx, sessionID, cmd.Prompt)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(result) == "" {
			result = "OK"
		}
		return fmt.Sprintf("Session baru dibuat: %s\n\n%s", sessionID, result), nil
	default:
		entry := b.sessions.GetCurrentSession(conversationID)
		if entry == nil {
			return "Tidak ada session aktif. Kirim session new atau kirim pesan biasa.", nil
		}
		return fmt.Sprintf("Session aktif: %s", entry.SessionID), nil
	}
}

func (b *WhatsAppBot) authorized(r *http.Request) bool {
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	serviceSecret := strings.TrimSpace(r.Header.Get("x-service-secret"))
	if b.config.InboundSecret != "" && bearer == b.config.InboundSecret {
		return true
	}
	if b.config.ServiceSecret != "" && serviceSecret == b.config.ServiceSecret {
		return true
	}
	return false
}

func (b *WhatsAppBot) isAllowed(msg NormalizedWhatsAppMessage) bool {
	if len(b.config.AllowedChatIDs) == 0 {
		return true
	}
	candidates := []string{msg.ChatID, msg.WANumber, msg.ConversationKey, msg.RemoteJID, msg.SenderPN}
	for _, allowed := range b.config.AllowedChatIDs {
		for _, candidate := range candidates {
			if allowed != "" && allowed == candidate {
				return true
			}
		}
	}
	return false
}

func (b *WhatsAppBot) writeReply(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := whatsappReplyEnvelope{Success: status < 400, Message: http.StatusText(status)}
	if resp.Message == "" {
		resp.Message = "OK"
	}
	resp.Data.Reply.Message = message
	resp.Data.WhatsAppReply.Message = message
	_ = json.NewEncoder(w).Encode(resp)
}

func buildWhatsAppPrompt(msg NormalizedWhatsAppMessage, sessionID, prompt string) string {
	return fmt.Sprintf("%s\n\nHere is chat context for this WhatsApp message:\n conversation_key: %s\n chat_id: %s\n wa_number: %s\n sender_pn: %s\n session_id: %s\n is_group: %t\n display_name: %s",
		prompt,
		msg.ConversationKey,
		msg.ChatID,
		msg.WANumber,
		msg.SenderPN,
		sessionID,
		msg.IsGroup,
		msg.DisplayName,
	)
}

func hashConversationKey(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func WhatsAppHelpText() string {
	lines := []string{
		"Available commands:",
		"help — Show this help message",
		"start — Start bot",
		"status — Check server status",
		"deploy <env> — Deploy application",
		"logs [N] — Show last N log lines",
		"session — Show current session",
		"session new [text] — Create new session",
		"session list — List sessions",
		"session switch <id> — Switch session",
		"session delete <id> — Delete session",
	}
	sort.Strings(lines[1:])
	return strings.Join(lines, "\n")
}
