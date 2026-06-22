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
)

type WhatsAppBotConfig struct {
	InboundSecret  string
	ServiceSecret  string
	AllowedChatIDs []string
	SessionTimeout time.Duration
}

type WhatsAppBot struct {
	config   *WhatsAppBotConfig
	ocClient *OCClient
	sessions *SessionMap
	sender   *WhatsAppSender
}

func NewWhatsAppBot(cfg *WhatsAppBotConfig, ocClient *OCClient, sessions *SessionMap, sender *WhatsAppSender) *WhatsAppBot {
	return &WhatsAppBot{
		config:   cfg,
		ocClient: ocClient,
		sessions: sessions,
		sender:   sender,
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

	if cmd.Type == CmdStatus || cmd.Type == CmdDeploy || cmd.Type == CmdLogs {
		ctx, cancel := context.WithTimeout(r.Context(), b.config.SessionTimeout)
		defer cancel()
		result, err := b.ocClient.SendMessage(ctx, sessionID, buildWhatsAppPrompt(normalized, sessionID, cmd.Prompt))
		if err != nil {
			slog.Error("whatsapp sync OpenCode request failed", "error", err)
			b.writeReply(w, http.StatusOK, fmt.Sprintf("Gagal: %s", err))
			return
		}
		slog.Info("whatsapp sync OpenCode request finished", "session_id", sessionID)
		b.writeReply(w, http.StatusOK, result)
		return
	}

	go b.processAsyncPrompt(context.Background(), normalized, sessionID, cmd.Prompt)
	b.writeReply(w, http.StatusOK, ack)
}

func (b *WhatsAppBot) processAsyncPrompt(ctx context.Context, msg NormalizedWhatsAppMessage, sessionID, prompt string) {
	ctx, cancel := context.WithTimeout(ctx, b.config.SessionTimeout)
	defer cancel()

	slog.Info("whatsapp OpenCode request start", "session_id", sessionID, "conversation_key", msg.ConversationKey)
	b.sessions.Renew(sessionID)
	result, err := b.ocClient.SendMessage(ctx, sessionID, buildWhatsAppPrompt(msg, sessionID, prompt))
	if err != nil {
		slog.Error("whatsapp OpenCode request failed", "error", err, "session_id", sessionID)
		_, sendErr := b.sender.SendText(context.Background(), msg.ReplyTarget, fmt.Sprintf("❌ Gagal memproses pesan: %s", err))
		if sendErr != nil {
			slog.Error("whatsapp outbound send failed", "error", sendErr, "target", msg.ReplyTarget)
		}
		return
	}
	if strings.TrimSpace(result) == "" {
		result = "OK"
	}
	_, sendErr := b.sender.SendText(context.Background(), msg.ReplyTarget, result)
	if sendErr != nil {
		slog.Error("whatsapp outbound send failed", "error", sendErr, "target", msg.ReplyTarget)
		return
	}
	slog.Info("whatsapp outbound send success", "target", msg.ReplyTarget, "session_id", sessionID)
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
		return fmt.Sprintf("Session %s dihapus.", cmd.SessionArg), nil
	case SessNew:
		sessionID, err := b.ocClient.CreateSession(ctx)
		if err != nil {
			return "", err
		}
		b.sessions.Store(sessionID, conversationID, 0, 0)
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
