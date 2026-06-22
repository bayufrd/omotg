package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeWhatsAppSender struct {
	number  string
	message string
}

func (f *fakeWhatsAppSender) SendText(ctx context.Context, number, message string) (string, error) {
	f.number = number
	f.message = message
	return "ok", nil
}

func TestRegisterWhatsAppTools(t *testing.T) {
	server := New("http://127.0.0.1:9090")
	sender := &fakeWhatsAppSender{}

	RegisterWhatsAppTools(server, sender)

	if len(server.tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(server.tools))
	}
	if server.tools[0].def.Name != "send_whatsapp_message" {
		t.Fatalf("tool[0] name = %q", server.tools[0].def.Name)
	}
	if server.tools[1].def.Name != "send_whatsapp_notification" {
		t.Fatalf("tool[1] name = %q", server.tools[1].def.Name)
	}

	args, _ := json.Marshal(map[string]string{"number": "628123", "text": "Halo"})
	result, err := server.tools[0].handler(context.Background(), args)
	if err != nil {
		t.Fatalf("message handler error = %v", err)
	}
	if result != "ok" {
		t.Fatalf("message handler result = %q", result)
	}
	if sender.number != "628123" || sender.message != "Halo" {
		t.Fatalf("sender got number=%q message=%q", sender.number, sender.message)
	}

	args, _ = json.Marshal(map[string]string{"number": "628999", "status": "success", "message": "Deploy selesai"})
	_, err = server.tools[1].handler(context.Background(), args)
	if err != nil {
		t.Fatalf("notification handler error = %v", err)
	}
	if sender.number != "628999" {
		t.Fatalf("notification number = %q", sender.number)
	}
	if sender.message != "✅ Success\nDeploy selesai" {
		t.Fatalf("notification message = %q", sender.message)
	}
}
