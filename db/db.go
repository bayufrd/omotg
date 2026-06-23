package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Config holds database configuration
type Config struct {
	Path string // SQLite file path, defaults to ~/.config/omotg/omotg.db
}

// Open opens a SQLite database connection
func Open(cfg Config) (*sql.DB, error) {
	if cfg.Path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("user home dir: %w", err)
		}
		cfg.Path = filepath.Join(home, ".config", "omotg", "omotg.db")
	}

	// Ensure directory exists
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	db, err := sql.Open("sqlite", cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	// Initialize schema
	if err := Init(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init: %w", err)
	}

	return db, nil
}
