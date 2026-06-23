package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ProviderKind string

const (
	ProviderOpenCode         ProviderKind = "opencode"
	ProviderOpenAICompatible ProviderKind = "openai-compatible"
)

type ProviderConfig struct {
	Kind     ProviderKind
	BaseURL  string
	APIToken string
	Model    string
	Password string
}

func (c ProviderConfig) NormalizedKind() ProviderKind {
	switch strings.ToLower(strings.TrimSpace(string(c.Kind))) {
	case "", string(ProviderOpenCode):
		return ProviderOpenCode
	case "openai-compatible", "openai", "9router":
		return ProviderOpenAICompatible
	default:
		return ProviderKind(strings.ToLower(strings.TrimSpace(string(c.Kind))))
	}
}

func (c ProviderConfig) DisplayName() string {
	switch c.NormalizedKind() {
	case ProviderOpenAICompatible:
		return "openai-compatible"
	default:
		return "opencode"
	}
}

func NewSessionClient(cfg ProviderConfig) SessionClient {
	switch cfg.NormalizedKind() {
	case ProviderOpenAICompatible:
		return NewOpenAICompatibleClient(cfg.BaseURL, cfg.APIToken, cfg.Model)
	default:
		return NewOCClient(cfg.BaseURL, cfg.Password)
	}
}

type OpenAICompatibleClient struct {
	baseURL    string
	apiToken   string
	model      string
	httpClient *http.Client
	counter    atomic.Uint64
	mu         sync.RWMutex
	history    map[string][]openAIMessage
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func NewOpenAICompatibleClient(baseURL, apiToken, model string) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiToken:   apiToken,
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		history:    make(map[string][]openAIMessage),
	}
}

func (c *OpenAICompatibleClient) CreateSession(ctx context.Context) (string, error) {
	id := fmt.Sprintf("oaic-%d", c.counter.Add(1))
	c.mu.Lock()
	c.history[id] = nil
	c.mu.Unlock()
	return id, nil
}

func (c *OpenAICompatibleClient) DeleteSession(ctx context.Context, sessionID string) error {
	c.mu.Lock()
	delete(c.history, sessionID)
	c.mu.Unlock()
	return nil
}

func (c *OpenAICompatibleClient) SendMessage(ctx context.Context, sessionID, prompt string) (string, error) {
	if c.baseURL == "" {
		return "", fmt.Errorf("openai-compatible base url not configured")
	}
	if c.apiToken == "" {
		return "", fmt.Errorf("openai-compatible api token not configured")
	}
	if c.model == "" {
		return "", fmt.Errorf("openai-compatible model not configured")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("empty prompt")
	}

	c.mu.Lock()
	messages := append([]openAIMessage{}, c.history[sessionID]...)
	messages = append(messages, openAIMessage{Role: "user", Content: prompt})
	c.mu.Unlock()

	body := openAIChatRequest{Model: c.model, Messages: messages, Stream: false}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return "", fmt.Errorf("encode chat completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", &buf)
	if err != nil {
		return "", fmt.Errorf("create chat completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat completion: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read chat completion response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("chat completion: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out openAIChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode chat completion response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("chat completion: empty choices")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("chat completion: empty message content")
	}

	c.mu.Lock()
	c.history[sessionID] = append(messages, openAIMessage{Role: "assistant", Content: text})
	c.mu.Unlock()
	return text, nil
}

func (c *OpenAICompatibleClient) ConnectEventStream(ctx context.Context) (<-chan SSEEvent, error) {
	return nil, fmt.Errorf("event stream unsupported for openai-compatible provider")
}

func (c *OpenAICompatibleClient) GetSessionChildren(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}
