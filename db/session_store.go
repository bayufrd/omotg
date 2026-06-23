package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Session represents a persisted session
type Session struct {
	ID                 string          `json:"id"`
	Channel            string          `json:"channel"`
	ChatID             string          `json:"chat_id"`
	ThreadID           *string        `json:"thread_id,omitempty"`
	ProviderSessionID  string          `json:"provider_session_id"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	LastMessageAt      *time.Time     `json:"last_message_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
}

// Message represents a persisted message
type Message struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	Channel   string    `json:"channel"`
	Direction string    `json:"direction"`
	MessageID string    `json:"message_id"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Error     *string  `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DeliveryAttempt represents a delivery attempt
type DeliveryAttempt struct {
	ID         int64     `json:"id"`
	MessageID  string    `json:"message_id"`
	Channel    string    `json:"channel"`
	Attempt    int       `json:"attempt"`
	Status     string    `json:"status"`
	StatusCode *int      `json:"status_code,omitempty"`
	Error      *string  `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// SessionStore handles session persistence
type SessionStore struct {
	db *sql.DB
}

// NewSessionStore creates a new session store
func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{db: db}
}

// CreateSession inserts a new session
func (s *SessionStore) CreateSession(ctx context.Context, session *Session) error {
	query := `
		INSERT INTO sessions (id, channel, chat_id, thread_id, provider_session_id, created_at, updated_at, last_message_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		session.ID, session.Channel, session.ChatID, session.ThreadID,
		session.ProviderSessionID, session.CreatedAt.Unix(), session.UpdatedAt.Unix(),
		nullTime(session.LastMessageAt), string(session.Metadata))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID
func (s *SessionStore) GetSession(ctx context.Context, id string) (*Session, error) {
	query := `
		SELECT id, channel, chat_id, thread_id, provider_session_id, created_at, updated_at, last_message_at, metadata
		FROM sessions WHERE id = ?
	`
	var session Session
	var threadID, metadata sql.NullString
	var lastMsgAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&session.ID, &session.Channel, &session.ChatID, &threadID,
		&session.ProviderSessionID, &session.CreatedAt, &session.UpdatedAt,
		&lastMsgAt, &metadata)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	session.ThreadID = nullStrPtr(threadID)
	session.Metadata = nullStrJSON(metadata)
	if lastMsgAt.Valid {
		t := time.Unix(lastMsgAt.Int64, 0)
		session.LastMessageAt = &t
	}
	return &session, nil
}

// FindSession finds a session by channel and chat_id (and optional thread_id)
func (s *SessionStore) FindSession(ctx context.Context, channel, chatID, threadID string) (*Session, error) {
	var query string
	var args []interface{}

	if threadID != "" {
		query = `SELECT id, channel, chat_id, thread_id, provider_session_id, created_at, updated_at, last_message_at, metadata
				 FROM sessions WHERE channel = ? AND chat_id = ? AND thread_id = ? LIMIT 1`
		args = []interface{}{channel, chatID, threadID}
	} else {
		query = `SELECT id, channel, chat_id, thread_id, provider_session_id, created_at, updated_at, last_message_at, metadata
				 FROM sessions WHERE channel = ? AND chat_id = ? AND thread_id IS NULL LIMIT 1`
		args = []interface{}{channel, chatID}
	}

	var session Session
	var threadIDStr, metadata sql.NullString
	var lastMsgAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&session.ID, &session.Channel, &session.ChatID, &threadIDStr,
		&session.ProviderSessionID, &session.CreatedAt, &session.UpdatedAt,
		&lastMsgAt, &metadata)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	session.ThreadID = nullStrPtr(threadIDStr)
	session.Metadata = nullStrJSON(metadata)
	if lastMsgAt.Valid {
		t := time.Unix(lastMsgAt.Int64, 0)
		session.LastMessageAt = &t
	}
	return &session, nil
}

// UpdateSession updates session timestamps and provider session ID
func (s *SessionStore) UpdateSession(ctx context.Context, session *Session) error {
	query := `UPDATE sessions SET provider_session_id = ?, updated_at = ?, last_message_at = ?, metadata = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query,
		session.ProviderSessionID, session.UpdatedAt.Unix(),
		nullTime(session.LastMessageAt), string(session.Metadata), session.ID)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return nil
}

// DeleteSession removes a session
func (s *SessionStore) DeleteSession(ctx context.Context, id string) error {
	query := `DELETE FROM sessions WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ListSessionsByChannel returns all sessions for a channel
func (s *SessionStore) ListSessionsByChannel(ctx context.Context, channel string) ([]*Session, error) {
	query := `SELECT id, channel, chat_id, thread_id, provider_session_id, created_at, updated_at, last_message_at, metadata
			  FROM sessions WHERE channel = ? ORDER BY last_message_at DESC`
	rows, err := s.db.QueryContext(ctx, query, channel)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var session Session
		var threadID, metadata sql.NullString
		var lastMsgAt sql.NullInt64
		if err := rows.Scan(&session.ID, &session.Channel, &session.ChatID, &threadID,
			&session.ProviderSessionID, &session.CreatedAt, &session.UpdatedAt,
			&lastMsgAt, &metadata); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		session.ThreadID = nullStrPtr(threadID)
		session.Metadata = nullStrJSON(metadata)
		if lastMsgAt.Valid {
			t := time.Unix(lastMsgAt.Int64, 0)
			session.LastMessageAt = &t
		}
		sessions = append(sessions, &session)
	}
	return sessions, rows.Err()
}

// Helper functions
func nullTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func nullStrPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func nullStrJSON(ns sql.NullString) json.RawMessage {
	if ns.Valid {
		return json.RawMessage(ns.String)
	}
	return json.RawMessage("{}")
}
