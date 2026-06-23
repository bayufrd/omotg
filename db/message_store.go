package db

import (
	"context"
	"database/sql"
	"fmt"
)

// MessageStore handles message persistence
type MessageStore struct {
	db *sql.DB
}

// NewMessageStore creates a new message store
func NewMessageStore(db *sql.DB) *MessageStore {
	return &MessageStore{db: db}
}

// SaveMessage inserts a new message
func (m *MessageStore) SaveMessage(ctx context.Context, msg *Message) error {
	query := `
		INSERT INTO messages (session_id, channel, direction, message_id, content, status, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := m.db.ExecContext(ctx, query,
		msg.SessionID, msg.Channel, msg.Direction, msg.MessageID,
		msg.Content, msg.Status, msg.Error, msg.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	id, _ := result.LastInsertId()
	msg.ID = id
	return nil
}

// GetMessageByID retrieves a message by ID
func (m *MessageStore) GetMessageByID(ctx context.Context, id int64) (*Message, error) {
	query := `SELECT id, session_id, channel, direction, message_id, content, status, error, created_at FROM messages WHERE id = ?`
	var msg Message
	err := m.db.QueryRowContext(ctx, query, id).Scan(
		&msg.ID, &msg.SessionID, &msg.Channel, &msg.Direction,
		&msg.MessageID, &msg.Content, &msg.Status, &msg.Error, &msg.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return &msg, nil
}

// FindMessageByExternalID finds a message by channel + external message_id (for idempotency)
func (m *MessageStore) FindMessageByExternalID(ctx context.Context, channel, messageID string) (*Message, error) {
	query := `SELECT id, session_id, channel, direction, message_id, content, status, error, created_at 
			  FROM messages WHERE channel = ? AND message_id = ? LIMIT 1`
	var msg Message
	err := m.db.QueryRowContext(ctx, query, channel, messageID).Scan(
		&msg.ID, &msg.SessionID, &msg.Channel, &msg.Direction,
		&msg.MessageID, &msg.Content, &msg.Status, &msg.Error, &msg.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find message: %w", err)
	}
	return &msg, nil
}

// UpdateMessageStatus updates message status
func (m *MessageStore) UpdateMessageStatus(ctx context.Context, id int64, status, errStr string) error {
	query := `UPDATE messages SET status = ?, error = ? WHERE id = ?`
	_, err := m.db.ExecContext(ctx, query, status, nullStr(errStr), id)
	if err != nil {
		return fmt.Errorf("update message status: %w", err)
	}
	return nil
}

// ListMessagesBySession returns messages for a session
func (m *MessageStore) ListMessagesBySession(ctx context.Context, sessionID string, limit int) ([]*Message, error) {
	query := `SELECT id, session_id, channel, direction, message_id, content, status, error, created_at 
			  FROM messages WHERE session_id = ? ORDER BY created_at DESC LIMIT ?`
	rows, err := m.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Channel, &msg.Direction,
			&msg.MessageID, &msg.Content, &msg.Status, &msg.Error, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, &msg)
	}
	return messages, rows.Err()
}

// SaveDeliveryAttempt logs a delivery attempt
func (m *MessageStore) SaveDeliveryAttempt(ctx context.Context, attempt *DeliveryAttempt) error {
	query := `
		INSERT INTO delivery_attempts (message_id, channel, attempt, status, status_code, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := m.db.ExecContext(ctx, query,
		attempt.MessageID, attempt.Channel, attempt.Attempt,
		attempt.Status, attempt.StatusCode, attempt.Error, attempt.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("save delivery attempt: %w", err)
	}
	id, _ := result.LastInsertId()
	attempt.ID = id
	return nil
}

// GetDeliveryAttempts returns all attempts for a message
func (m *MessageStore) GetDeliveryAttempts(ctx context.Context, channel, messageID string) ([]*DeliveryAttempt, error) {
	query := `SELECT id, message_id, channel, attempt, status, status_code, error, created_at 
			  FROM delivery_attempts WHERE channel = ? AND message_id = ? ORDER BY attempt ASC`
	rows, err := m.db.QueryContext(ctx, query, channel, messageID)
	if err != nil {
		return nil, fmt.Errorf("get delivery attempts: %w", err)
	}
	defer rows.Close()

	var attempts []*DeliveryAttempt
	for rows.Next() {
		var attempt DeliveryAttempt
		if err := rows.Scan(&attempt.ID, &attempt.MessageID, &attempt.Channel,
			&attempt.Attempt, &attempt.Status, &attempt.StatusCode, &attempt.Error, &attempt.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}
		attempts = append(attempts, &attempt)
	}
	return attempts, rows.Err()
}

// nullStr converts string to *string for nullable columns
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
