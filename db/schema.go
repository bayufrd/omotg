package db

import (
	"database/sql"
	"fmt"
)

// Schema SQL for SQLite
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    channel TEXT NOT NULL,           -- 'telegram' or 'whatsapp'
    chat_id TEXT NOT NULL,          -- chat/phone identifier
    thread_id TEXT,                 -- for telegram forum topics
    provider_session_id TEXT,       -- the actual session ID in LLM provider
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_message_at INTEGER,
    metadata TEXT                  -- JSON for extra data
);

CREATE INDEX IF NOT EXISTS idx_sessions_channel_chat ON sessions(channel, chat_id);
CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    direction TEXT NOT NULL,         -- 'inbound' or 'outbound'
    message_id TEXT NOT NULL,        -- external message ID for idempotency
    content TEXT NOT NULL,
    status TEXT NOT NULL,           -- 'sent', 'delivered', 'failed'
    error TEXT,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);
CREATE INDEX IF NOT EXISTS idx_messages_message_id ON messages(channel, message_id);

CREATE TABLE IF NOT EXISTS delivery_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    status TEXT NOT NULL,            -- 'success', 'failed'
    status_code INTEGER,
    error TEXT,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attempts_message ON delivery_attempts(channel, message_id);
`

// Init creates tables if not exist
func Init(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
