package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubRoundTripper func(*http.Request) (*http.Response, error)

func (s stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return s(r)
}

func TestWhatsAppBotAuthorized(t *testing.T) {
	bot := NewWhatsAppBot(&WhatsAppBotConfig{InboundSecret: "bearer-secret", ServiceSecret: "svc-secret"}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", nil)
	req.Header.Set("Authorization", "Bearer bearer-secret")
	if !bot.authorized(req) {
		t.Fatal("authorized() = false for bearer")
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", nil)
	req.Header.Set("x-service-secret", "svc-secret")
	if !bot.authorized(req) {
		t.Fatal("authorized() = false for service secret")
	}
}

func TestWhatsAppBotHandleInboundUnauthorized(t *testing.T) {
	bot := NewWhatsAppBot(&WhatsAppBotConfig{InboundSecret: "secret", SessionTimeout: time.Second}, NewOCClient("http://example.com", "p"), NewSessionMap(), NewWhatsAppSender("http://example.com", "/send", ""))
	req := httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	bot.HandleInbound(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestWhatsAppBotHandleInboundHelp(t *testing.T) {
	bot := NewWhatsAppBot(&WhatsAppBotConfig{InboundSecret: "secret", SessionTimeout: time.Second}, NewOCClient("http://example.com", "p"), NewSessionMap(), NewWhatsAppSender("http://example.com", "/send", ""))
	payload := WhatsAppInboundPayload{
		Command: "help",
		User:    WhatsAppInboundUser{WANumber: "628123"},
		Message: WhatsAppInboundMessage{Body: "help"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	bot.HandleInbound(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Available commands") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWhatsAppBotHandleInboundStatusSync(t *testing.T) {
	oc := NewOCClient("http://opencode.local", "p")
	oc.httpClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/session") {
			t.Fatalf("unexpected create session request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"id":"sess-1"}`)), Header: make(http.Header)}, nil
	})}
	oc.sseClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/message") {
			t.Fatalf("unexpected send request path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"parts":[{"type":"text","text":"server ok"}]}`)), Header: make(http.Header)}, nil
	})}

	bot := NewWhatsAppBot(&WhatsAppBotConfig{InboundSecret: "secret", SessionTimeout: time.Second}, oc, NewSessionMap(), NewWhatsAppSender("http://example.com", "/send", ""))
	payload := WhatsAppInboundPayload{
		Command: "status",
		User:    WhatsAppInboundUser{WANumber: "628123"},
		Message: WhatsAppInboundMessage{Body: "status"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	bot.HandleInbound(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "server ok") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWhatsAppBotHandleInboundSessionNew(t *testing.T) {
	oc := NewOCClient("http://opencode.local", "p")
	oc.httpClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/session"):
			return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"id":"sess-2"}`)), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})}
	oc.sseClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"parts":[{"type":"text","text":"halo"}]}`)), Header: make(http.Header)}, nil
	})}

	bot := NewWhatsAppBot(&WhatsAppBotConfig{InboundSecret: "secret", SessionTimeout: time.Second}, oc, NewSessionMap(), NewWhatsAppSender("http://example.com", "/send", ""))
	payload := WhatsAppInboundPayload{
		Command: "session new halo",
		User:    WhatsAppInboundUser{WANumber: "628123"},
		Message: WhatsAppInboundMessage{Body: "session new halo"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	bot.HandleInbound(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Session baru dibuat: sess-2") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWhatsAppBotHandleInboundAsyncPrompt(t *testing.T) {
	oc := NewOCClient("http://opencode.local", "p")
	oc.httpClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"id":"sess-3"}`)), Header: make(http.Header)}, nil
	})}
	oc.sseClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"parts":[{"type":"text","text":"jawaban async"}]}`)), Header: make(http.Header)}, nil
	})}

	sent := make(chan string, 1)
	sender := NewWhatsAppSender("http://wa.local", "/send", "")
	sender.HTTPClient = &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		sent <- string(body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`)), Header: make(http.Header)}, nil
	})}

	bot := NewWhatsAppBot(&WhatsAppBotConfig{InboundSecret: "secret", SessionTimeout: 2 * time.Second}, oc, NewSessionMap(), sender)
	payload := WhatsAppInboundPayload{
		User:    WhatsAppInboundUser{WANumber: "628123"},
		Message: WhatsAppInboundMessage{Body: "tolong bantu"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/internal/wa/inbound", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	bot.HandleInbound(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	select {
	case got := <-sent:
		if !strings.Contains(got, `"pesan":"jawaban async"`) {
			t.Fatalf("sent body = %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting async send")
	}
}

func TestWhatsAppBotSessionList(t *testing.T) {
	bot := NewWhatsAppBot(&WhatsAppBotConfig{}, nil, NewSessionMap(), nil)
	bot.sessions.Store("sess-1", 10, 0, 0)
	bot.sessions.Store("sess-2", 10, 0, 0)
	text, err := bot.handleSessionCommand(context.Background(), 10, ParsedCommand{Type: CmdSession, SessionAct: SessList})
	if err != nil {
		t.Fatalf("handleSessionCommand error = %v", err)
	}
	if !strings.Contains(text, "Daftar session") {
		t.Fatalf("text = %q", text)
	}
}
