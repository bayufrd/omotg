package bot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SSEEvent represents a parsed event from OpenCode's global event stream.
type SSEEvent struct {
	SessionID string
	Type      string // "delta", "text", "done", "error"
	Content   string
}

// OCClient is an HTTP client for the OpenCode Serve REST API.
type OCClient struct {
	baseURL    string
	password   string
	httpClient *http.Client
}

// NewOCClient creates a new OCClient with a 30-second HTTP timeout.
func NewOCClient(baseURL, password string) *OCClient {
	return &OCClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		password: password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *OCClient) setAuth(req *http.Request) {
	req.SetBasicAuth("opencode", c.password)
}

// CreateSession creates a new OpenCode session and returns its ID.
func (c *OCClient) CreateSession(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session", http.NoBody)
	if err != nil {
		return "", fmt.Errorf("create session request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create session: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		SessionID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode create session response: %w", err)
	}
	if result.SessionID == "" {
		return "", fmt.Errorf("create session: empty session_id in response")
	}
	return result.SessionID, nil
}

// SendMessage sends a prompt message to an existing session.
func (c *OCClient) SendMessage(ctx context.Context, sessionID, prompt string) error {
	body := map[string]interface{}{
		"parts": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return fmt.Errorf("encode send message body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session/"+sessionID+"/message", &buf)
	if err != nil {
		return fmt.Errorf("send message request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send message: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type rawSSEEvent struct {
	Payload json.RawMessage `json:"payload"`
}

type rawPayload struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties,omitempty"`
}

type propSession struct {
	SessionID string `json:"sessionID"`
}

type propDelta struct {
	SessionID string `json:"sessionID"`
	Field     string `json:"field"`
	Delta     string `json:"delta"`
}

type propUpdated struct {
	SessionID string `json:"sessionID"`
	Part      *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"part,omitempty"`
}

func (c *OCClient) SubscribeEvents(ctx context.Context) (<-chan SSEEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/event", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("subscribe events request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscribe events: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("subscribe events: status %d", resp.StatusCode)
	}

	ch := make(chan SSEEvent, 128)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")

			var raw rawSSEEvent
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				continue
			}

			var p rawPayload
			if err := json.Unmarshal(raw.Payload, &p); err != nil {
				continue
			}

			// Extract sessionID (present in most events)
			var sessionID string
			if p.Properties != nil {
				var ps propSession
				if err := json.Unmarshal(p.Properties, &ps); err == nil {
					sessionID = ps.SessionID
				}
			}

			switch {
			case strings.HasPrefix(p.Type, "message.part.delta"):
				var pd propDelta
				if err := json.Unmarshal(p.Properties, &pd); err != nil || pd.Field != "text" {
					continue
				}
				ev := SSEEvent{SessionID: sessionID, Type: "delta", Content: pd.Delta}
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}

			case p.Type == "message.part.updated":
				var pu propUpdated
				if err := json.Unmarshal(p.Properties, &pu); err != nil || pu.Part == nil {
					continue
				}
				if pu.Part.Type == "text" && pu.Part.Text != "" {
					ev := SSEEvent{SessionID: sessionID, Type: "text", Content: pu.Part.Text}
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				}

			case p.Type == "message.completed":
				ev := SSEEvent{SessionID: sessionID, Type: "done"}
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}

			case strings.HasPrefix(p.Type, "error"):
				ev := SSEEvent{SessionID: sessionID, Type: "error", Content: p.Type}
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			slog.Error("SSE event stream read error", "error", err)
		}
	}()

	return ch, nil
}
