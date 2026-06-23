package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleClient_CreateSession(t *testing.T) {
	c := NewOpenAICompatibleClient("http://localhost:8080", "token", "gpt-4")
	id1, err := c.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if !strings.HasPrefix(id1, "oaic-") {
		t.Errorf("session ID = %q, want prefix oaic-", id1)
	}

	id2, err := c.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession() 2nd call error: %v", err)
	}
	if id1 == id2 {
		t.Error("two sessions should have different IDs")
	}
}

func TestOpenAICompatibleClient_DeleteSession(t *testing.T) {
	c := NewOpenAICompatibleClient("http://localhost:8080", "token", "gpt-4")
	id, _ := c.CreateSession(context.Background())
	if err := c.DeleteSession(context.Background(), id); err != nil {
		t.Errorf("DeleteSession() error: %v", err)
	}
	if err := c.DeleteSession(context.Background(), "nonexistent"); err != nil {
		t.Errorf("DeleteSession(nonexistent) error: %v", err)
	}
}

func TestOpenAICompatibleClient_SendMessage(t *testing.T) {
	var capturedReqBody map[string]interface{}
	var capturedAuth string
	var capturedModel string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&capturedReqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		capturedModel = capturedReqBody["model"].(string)

		resp := `{"choices":[{"message":{"content":"Hello! How can I help?"}}]}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	c := NewOpenAICompatibleClient(server.URL, "test-token-123", "gpt-4o-mini")
	id, _ := c.CreateSession(context.Background())

	reply, err := c.SendMessage(context.Background(), id, "Hello")
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if reply != "Hello! How can I help?" {
		t.Errorf("reply = %q", reply)
	}
	if !strings.HasPrefix(capturedAuth, "Bearer test-token-123") {
		t.Errorf("Authorization = %q, want Bearer test-token-123", capturedAuth)
	}
	if capturedModel != "gpt-4o-mini" {
		t.Errorf("model in request = %q", capturedModel)
	}

	// Second message should include history
	_, err = c.SendMessage(context.Background(), id, "Follow-up")
	if err != nil {
		t.Fatalf("SendMessage() 2nd error: %v", err)
	}
	messages := capturedReqBody["messages"].([]interface{})
	if len(messages) != 3 {
		t.Errorf("expected 3 messages in history, got %d", len(messages))
	}
}

func TestOpenAICompatibleClient_SendMessage_MissingFields(t *testing.T) {
	ctx := context.Background()
	id := "test-session"

	c := NewOpenAICompatibleClient("", "token", "model")
	_, err := c.SendMessage(ctx, id, "hello")
	if err == nil {
		t.Error("expected error for empty baseURL")
	}

	c = NewOpenAICompatibleClient("http://localhost", "", "model")
	_, err = c.SendMessage(ctx, id, "hello")
	if err == nil {
		t.Error("expected error for empty token")
	}

	c = NewOpenAICompatibleClient("http://localhost", "token", "")
	_, err = c.SendMessage(ctx, id, "hello")
	if err == nil {
		t.Error("expected error for empty model")
	}
}

func TestOpenAICompatibleClient_SendMessage_EmptyPrompt(t *testing.T) {
	c := NewOpenAICompatibleClient("http://localhost", "token", "model")
	_, err := c.SendMessage(context.Background(), "id", "   ")
	if err == nil {
		t.Error("expected error for empty prompt")
	}
}

func TestOpenAICompatibleClient_SendMessage_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	c := NewOpenAICompatibleClient(server.URL, "token", "model")
	id, _ := c.CreateSession(context.Background())
	_, err := c.SendMessage(context.Background(), id, "hello")
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestOpenAICompatibleClient_SendMessage_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := `{"choices":[]}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(resp))
	}))
	defer server.Close()

	c := NewOpenAICompatibleClient(server.URL, "token", "model")
	id, _ := c.CreateSession(context.Background())
	_, err := c.SendMessage(context.Background(), id, "hello")
	if err == nil {
		t.Error("expected error for empty choices")
	}
}

func TestOpenAICompatibleClient_ConnectEventStream(t *testing.T) {
	c := NewOpenAICompatibleClient("http://localhost", "token", "model")
	_, err := c.ConnectEventStream(context.Background())
	if err == nil {
		t.Error("expected error for unsupported event stream")
	}
}

func TestOpenAICompatibleClient_GetSessionChildren(t *testing.T) {
	c := NewOpenAICompatibleClient("http://localhost", "token", "model")
	children, err := c.GetSessionChildren(context.Background(), "any-id")
	if err != nil {
		t.Errorf("GetSessionChildren() error: %v", err)
	}
	if children != nil {
		t.Errorf("GetSessionChildren() = %v, want nil", children)
	}
}

func TestProviderConfig_NormalizedKind(t *testing.T) {
	tests := []struct {
		input    ProviderKind
		expected ProviderKind
	}{
		{"", ProviderOpenCode},
		{"opencode", ProviderOpenCode},
		{"OpenCode", ProviderOpenCode},
		{"OPENCODE", ProviderOpenCode},
		{"openai-compatible", ProviderOpenAICompatible},
		{"openai", ProviderOpenAICompatible},
		{"9router", ProviderOpenAICompatible},
		{"OpenAI", ProviderOpenAICompatible},
		{"9ROUTER", ProviderOpenAICompatible},
	}

	for _, tc := range tests {
		cfg := ProviderConfig{Kind: tc.input}
		if got := cfg.NormalizedKind(); got != tc.expected {
			t.Errorf("NormalizedKind(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestProviderConfig_DisplayName(t *testing.T) {
	cfg := ProviderConfig{Kind: ProviderOpenAICompatible}
	if got := cfg.DisplayName(); got != "openai-compatible" {
		t.Errorf("DisplayName() = %q", got)
	}

	cfg = ProviderConfig{Kind: ProviderOpenCode}
	if got := cfg.DisplayName(); got != "opencode" {
		t.Errorf("DisplayName() = %q", got)
	}
}

func TestNewSessionClient(t *testing.T) {
	// OpenCode provider
	ocClient := NewSessionClient(ProviderConfig{
		Kind:     ProviderOpenCode,
		BaseURL:  "http://localhost:4096",
		Password: "secret",
	})
	if ocClient == nil {
		t.Error("NewSessionClient returned nil for opencode")
	}
	if _, ok := ocClient.(*OCClient); !ok {
		t.Error("expected *OCClient for opencode provider")
	}

	// OpenAI-compatible provider
	oaiClient := NewSessionClient(ProviderConfig{
		Kind:     ProviderOpenAICompatible,
		BaseURL:  "http://localhost:8080",
		APIToken: "token",
		Model:    "gpt-4",
	})
	if oaiClient == nil {
		t.Error("NewSessionClient returned nil for openai-compatible")
	}
	if _, ok := oaiClient.(*OpenAICompatibleClient); !ok {
		t.Error("expected *OpenAICompatibleClient for openai-compatible provider")
	}
}
