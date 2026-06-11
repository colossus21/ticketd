package store

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order; the index+1 is the schema version they
// bring the database to. Gated by PRAGMA user_version — no migration library.
var migrations = []string{
	// 001 — initial schema
	`
CREATE TABLE tickets (
    id          INTEGER PRIMARY KEY,
    key         TEXT UNIQUE NOT NULL,
    project     TEXT NOT NULL DEFAULT 'default',
    title       TEXT NOT NULL CHECK (length(title) > 0),
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'todo',
    priority    INTEGER NOT NULL DEFAULT 2,
    parent_id   INTEGER REFERENCES tickets(id),
    labels      TEXT NOT NULL DEFAULT '[]',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE INDEX idx_tickets_project_status ON tickets(project, status);
CREATE INDEX idx_tickets_parent ON tickets(parent_id);

CREATE TABLE comments (
    id         INTEGER PRIMARY KEY,
    ticket_id  INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    author     TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_comments_ticket ON comments(ticket_id, created_at);

CREATE TABLE links (
    from_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    to_id   INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    kind    TEXT NOT NULL,
    PRIMARY KEY (from_id, to_id, kind)
);

CREATE TABLE counters (
    project TEXT PRIMARY KEY,
    next    INTEGER NOT NULL
);

CREATE VIRTUAL TABLE tickets_fts USING fts5(
    title, description, content='tickets', content_rowid='id'
);
CREATE VIRTUAL TABLE comments_fts USING fts5(
    body, content='comments', content_rowid='id'
);

CREATE TRIGGER tickets_ai AFTER INSERT ON tickets BEGIN
    INSERT INTO tickets_fts(rowid, title, description)
    VALUES (new.id, new.title, new.description);
END;
CREATE TRIGGER tickets_au AFTER UPDATE ON tickets BEGIN
    INSERT INTO tickets_fts(tickets_fts, rowid, title, description)
    VALUES ('delete', old.id, old.title, old.description);
    INSERT INTO tickets_fts(rowid, title, description)
    VALUES (new.id, new.title, new.description);
END;
CREATE TRIGGER tickets_ad AFTER DELETE ON tickets BEGIN
    INSERT INTO tickets_fts(tickets_fts, rowid, title, description)
    VALUES ('delete', old.id, old.title, old.description);
END;
CREATE TRIGGER comments_ai AFTER INSERT ON comments BEGIN
    INSERT INTO comments_fts(rowid, body) VALUES (new.id, new.body);
END;
CREATE TRIGGER comments_ad AFTER DELETE ON comments BEGIN
    INSERT INTO comments_fts(comments_fts, rowid, body)
    VALUES ('delete', old.id, old.body);
END;
`,
}

// migrate applies every migration above the current user_version inside a
// single transaction per migration, then bumps the version.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		// PRAGMA cannot be parameterized; the value is an int we control.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("bump user_version to %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
