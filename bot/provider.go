package bot

import (
	"context"
)

// SessionClient abstracts LLM/session provider used by bridge runtimes.
type SessionClient interface {
	CreateSession(ctx context.Context) (string, error)
	DeleteSession(ctx context.Context, sessionID string) error
	SendMessage(ctx context.Context, sessionID, prompt string) (string, error)
	ConnectEventStream(ctx context.Context) (<-chan SSEEvent, error)
	GetSessionChildren(ctx context.Context, sessionID string) ([]string, error)
}
