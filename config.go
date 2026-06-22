package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	TelegramBotToken string
	WebhookURL       string
	SecretToken      string
	OpenCodeURL      string
	OpenCodePassword string
	WebhookPort      string
	MCPPort          string
	AllowedChatIDs   []int64
	SessionTimeout   int // seconds
	TLSCertFile      string
	TLSKeyFile       string
	WAInboundSecret  string
	WABaseURL        string
	WAAPIToken       string
	WASendPath       string
	WAAllowedChatIDs []string
	WAServiceSecret  string
}

func (c *Config) WhatsAppSendURL() string {
	if c.WABaseURL == "" {
		return ""
	}
	base := strings.TrimRight(c.WABaseURL, "/")
	path := c.WASendPath
	if path == "" {
		path = "/api/whatsapp/send-personal"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func (c *Config) HasWhatsAppConfig() bool {
	return c.WAInboundSecret != "" || c.WAServiceSecret != ""
}

func (c *Config) HasTelegramConfig() bool {
	return c.TelegramBotToken != "" && c.WebhookURL != "" && c.SecretToken != ""
}

func LoadConfig() (*Config, error) {
	home, _ := os.UserHomeDir()
	defaultCert := home + "/.config/omotg/webhook.crt"
	defaultKey := home + "/.config/omotg/webhook.key"

	cfg := &Config{
		OpenCodeURL:     envOrDefault("OPENCODE_SERVER_URL", "http://127.0.0.1:4096"),
		WebhookPort:     envOrDefault("OMOTG_WEBHOOK_PORT", "8443"),
		MCPPort:         envOrDefault("OMOTG_MCP_PORT", "9090"),
		SessionTimeout:  300,
		TLSCertFile:     envOrDefault("OMOTG_TLS_CERT_FILE", defaultCert),
		TLSKeyFile:      envOrDefault("OMOTG_TLS_KEY_FILE", defaultKey),
		WAInboundSecret: os.Getenv("OMOTG_WA_INBOUND_SECRET"),
		WABaseURL:       envOrDefault("WHATSAPP_BASE_URL", "http://127.0.0.1:8090"),
		WAAPIToken:      os.Getenv("WHATSAPP_API_TOKEN"),
		WASendPath:      envOrDefault("WHATSAPP_SEND_PATH", "/api/whatsapp/send-personal"),
		WAServiceSecret: os.Getenv("OMOTG_WA_SERVICE_SECRET"),
	}

	cfg.TelegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	cfg.WebhookURL = os.Getenv("TELEGRAM_WEBHOOK_URL")
	cfg.SecretToken = os.Getenv("TELEGRAM_SECRET_TOKEN")

	var missing []string
	if cfg.OpenCodePassword = os.Getenv("OPENCODE_SERVER_PASSWORD"); cfg.OpenCodePassword == "" {
		missing = append(missing, "OPENCODE_SERVER_PASSWORD")
	}
	if !cfg.HasWhatsAppConfig() {
		if cfg.TelegramBotToken == "" {
			missing = append(missing, "TELEGRAM_BOT_TOKEN")
		}
		if cfg.WebhookURL == "" {
			missing = append(missing, "TELEGRAM_WEBHOOK_URL")
		}
		if cfg.SecretToken == "" {
			missing = append(missing, "TELEGRAM_SECRET_TOKEN")
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	if ids := os.Getenv("OMOTG_ALLOWED_CHAT_IDS"); ids != "" {
		parts := strings.Split(ids, ",")
		cfg.AllowedChatIDs = make([]int64, 0, len(parts))
		for _, p := range parts {
			id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid chat ID %q: %w", p, err)
			}
			cfg.AllowedChatIDs = append(cfg.AllowedChatIDs, id)
		}
	}

	if ids := os.Getenv("OMOTG_WA_ALLOWED_CHAT_IDS"); ids != "" {
		parts := strings.Split(ids, ",")
		cfg.WAAllowedChatIDs = make([]string, 0, len(parts))
		for _, p := range parts {
			id := strings.TrimSpace(p)
			if id == "" {
				continue
			}
			cfg.WAAllowedChatIDs = append(cfg.WAAllowedChatIDs, id)
		}
	}

	if t := os.Getenv("OMOTG_SESSION_TIMEOUT"); t != "" {
		val, err := strconv.Atoi(t)
		if err != nil || val <= 0 {
			return nil, fmt.Errorf("invalid OMOTG_SESSION_TIMEOUT: %q", t)
		}
		cfg.SessionTimeout = val
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
