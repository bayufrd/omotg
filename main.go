package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"omotg/bot"
	"omotg/db"
	"omotg/mcp"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	slog.Info("omotg starting",
		"webhook_port", cfg.WebhookPort,
		"mcp_port", cfg.MCPPort,
		"provider", cfg.ProviderKind,
		"provider_base_url", cfg.ProviderBaseURL,
		"provider_model", cfg.ProviderModel,
		"session_timeout", cfg.SessionTimeout,
	)

	// Open SQLite database
	var database *sql.DB
	if cfg.DatabasePath != "" || os.Getenv("OMOTG_DATABASE_ENABLED") != "false" {
		database, err = db.Open(db.Config{Path: cfg.DatabasePath})
		if err != nil {
			slog.Error("database open", "error", err)
			os.Exit(1)
		}
		defer database.Close()
		slog.Info("database connected", "path", cfg.DatabasePath)
	} else {
		slog.Info("database disabled")
	}

	providerClient := bot.NewSessionClient(bot.ProviderConfig{
		Kind:     bot.ProviderKind(cfg.ProviderKind),
		BaseURL:  cfg.ProviderBaseURL,
		APIToken: cfg.ProviderAPIToken,
		Model:    cfg.ProviderModel,
		Password: cfg.OpenCodePassword,
	})

	// Create session map.
	sessions := bot.NewSessionMap()

	// Initialize database stores if database is enabled
	var sessionStore *db.SessionStore
	var messageStore *db.MessageStore
	if database != nil {
		sessionStore = db.NewSessionStore(database)
		messageStore = db.NewMessageStore(database)

		// Load existing sessions from database
		ctx := context.Background()
		for _, channel := range []string{"telegram", "whatsapp"} {
			dbSessions, err := sessionStore.ListSessionsByChannel(ctx, channel)
			if err != nil {
				slog.Error("failed to list sessions from db", "channel", channel, "error", err)
			} else if len(dbSessions) > 0 {
				// Sort sessions so oldest is processed first, newest becomes current
				for i := len(dbSessions) - 1; i >= 0; i-- {
					s := dbSessions[i]
					chatID, err := strconv.ParseInt(s.ChatID, 10, 64)
					if err != nil {
						slog.Warn("invalid chat_id in db session", "chat_id", s.ChatID, "error", err)
						continue
					}
					var threadID int64
					if s.ThreadID != nil && *s.ThreadID != "" {
						threadID, err = strconv.ParseInt(*s.ThreadID, 10, 64)
						if err != nil {
							slog.Warn("invalid thread_id in db session", "thread_id", *s.ThreadID, "error", err)
							continue
						}
					}
					sessions.Store(s.ID, chatID, threadID, 0)
				}
				slog.Info("loaded sessions from db", "channel", channel, "count", len(dbSessions))
			}
		}
	}

	var bh *bot.Bot
	if cfg.HasTelegramConfig() {
		// Create TopicClient for forum topics.
		topicClient := bot.NewTopicClient(cfg.TelegramBotToken)

		// Create bot handler.
		botCfg := &bot.BotConfig{
			SecretToken:    cfg.SecretToken,
			AllowedChatIDs: cfg.AllowedChatIDs,
			SessionTimeout: time.Duration(cfg.SessionTimeout) * time.Second,
			BotToken:       cfg.TelegramBotToken,
		}
		bh = bot.NewBot(botCfg, providerClient, sessions, topicClient, sessionStore, messageStore)

		if len(cfg.AllowedChatIDs) == 0 {
			slog.Warn("no AllowedChatIDs configured — ALL Telegram chats are allowed")
		}

		// Register Telegram webhook on startup.
		if err := registerWebhook(cfg.TelegramBotToken, cfg.WebhookURL, cfg.SecretToken, cfg.TLSCertFile); err != nil {
			slog.Warn("webhook registration failed (will retry)", "error", err)
		} else {
			slog.Info("webhook registered", "url", cfg.WebhookURL)
		}
	} else {
		slog.Info("telegram runtime disabled")
	}

	waSender := bot.NewWhatsAppSender(cfg.WABaseURL, cfg.WASendPath, cfg.WAAPIToken)
	waBotCfg := &bot.WhatsAppBotConfig{
		InboundSecret:  cfg.WAInboundSecret,
		ServiceSecret:  cfg.WAServiceSecret,
		AllowedChatIDs: cfg.WAAllowedChatIDs,
		SessionTimeout: time.Duration(cfg.SessionTimeout) * time.Second,
	}
	waBot := bot.NewWhatsAppBot(waBotCfg, providerClient, sessions, waSender, sessionStore, messageStore)

	// Create MCP server and register channel tools.
	mcpBaseURL := "http://127.0.0.1:" + cfg.MCPPort
	mcpServer := mcp.New(mcpBaseURL)
	if cfg.HasTelegramConfig() {
		telegramSender := mcp.NewTelegramSender(cfg.TelegramBotToken)
		mcp.RegisterTelegramTools(mcpServer, telegramSender)
	}
	mcp.RegisterWhatsAppTools(mcpServer, waSender)

	// Start session cleanup goroutine.
	go sessions.StartCleanup(context.Background(), 5*time.Minute)

	// --- Webhook HTTP server ---
	webhookMux := http.NewServeMux()
	if bh != nil {
		webhookMux.HandleFunc("POST /webhook", bh.HandleWebhook)
	}
	if cfg.HasWhatsAppConfig() {
		webhookMux.HandleFunc("POST /internal/wa/inbound", waBot.HandleInbound)
		webhookMux.HandleFunc("POST /internal/wa/send", waBot.HandleManualSend)
		slog.Info("whatsapp runtime enabled", "send_url", cfg.WhatsAppSendURL())
	} else {
		slog.Info("whatsapp runtime disabled")
	}
	webhookMux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	whServer := &http.Server{
		Addr:         ":" + cfg.WebhookPort,
		Handler:      webhookMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// --- MCP SSE server ---
	// The MCP Server.Handler() returns a mux serving GET /mcp/sse and POST /mcp/message.
	mcpServer_ := &http.Server{
		Addr:         "127.0.0.1:" + cfg.MCPPort,
		Handler:      mcpServer.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE long-lived connection
		IdleTimeout:  60 * time.Second,
	}

	// Start servers.
	errCh := make(chan error, 2)

	go func() {
		slog.Info("webhook server listening (TLS)", "addr", whServer.Addr)
		if err := whServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("webhook: %w", err)
		}
	}()

	go func() {
		slog.Info("MCP server listening", "addr", mcpServer_.Addr)
		if err := mcpServer_.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("mcp: %w", err)
		}
	}()

	// Wait for interrupt or server error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("shutting down", "signal", sig)
	case err := <-errCh:
		slog.Error("server error", "error", err)
	}

	// Graceful shutdown with 5s timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := whServer.Shutdown(ctx); err != nil {
		slog.Error("webhook shutdown", "error", err)
	}
	if err := mcpServer_.Shutdown(ctx); err != nil {
		slog.Error("MCP shutdown", "error", err)
	}
	slog.Info("omotg stopped")
}

func registerWebhook(token, url, secret, certFile string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/setWebhook", token)

	var body io.Reader
	var contentType string

	if certFile != "" {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		w.WriteField("url", url)
		w.WriteField("allowed_updates", `["message","channel_post"]`)
		if secret != "" {
			w.WriteField("secret_token", secret)
		}
		certPath, _ := filepath.Abs(certFile)
		f, err := os.Open(certPath)
		if err != nil {
			return fmt.Errorf("open cert %s: %w", certPath, err)
		}
		defer f.Close()
		fw, err := w.CreateFormFile("certificate", filepath.Base(certPath))
		if err != nil {
			return fmt.Errorf("create form file: %w", err)
		}
		if _, err := io.Copy(fw, f); err != nil {
			return fmt.Errorf("copy cert: %w", err)
		}
		w.Close()
		body = &buf
		contentType = w.FormDataContentType()
	} else {
		payload := map[string]interface{}{
			"url":             url,
			"allowed_updates": []string{"message", "channel_post"},
		}
		if secret != "" {
			payload["secret_token"] = secret
		}
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
		contentType = "application/json"
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(apiURL, contentType, body)
	if err != nil {
		return fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !result.Ok {
		return fmt.Errorf("telegram error: %s", result.Description)
	}
	return nil
}
