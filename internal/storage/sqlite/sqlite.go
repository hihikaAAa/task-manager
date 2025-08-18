package sqlite

import (
    "context"
    "database/sql"
    _ "modernc.org/sqlite"
    "time"
)

type DB struct {
    SQL *sql.DB
}

func Open(path string) (*DB, error) {
    dsn := path + "?_pragma=busy_timeout(5000)"
    s, err := sql.Open("sqlite", dsn)
    if err != nil { return nil, err }
    s.SetMaxOpenConns(1)
    if err := migrate(context.Background(), s); err != nil {
		 return nil, err
		}
    return &DB{SQL: s}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
    stmts := []string{
        `PRAGMA foreign_keys = ON;`,
        `CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            tg_id INTEGER UNIQUE NOT NULL,
            username TEXT,
            role TEXT NOT NULL,
            name TEXT,
            team TEXT,
            created_at DATETIME NOT NULL
        );`,
        `CREATE TABLE IF NOT EXISTS tasks (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            creator_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            title TEXT,
            description TEXT,
            voice_file_id TEXT,
            due_at DATETIME,
            created_at DATETIME NOT NULL,
            updated_at DATETIME NOT NULL
        );`,
        `CREATE TABLE IF NOT EXISTS task_assignees (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
            user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            status TEXT NOT NULL DEFAULT 'new',
            updated_at DATETIME NOT NULL
        );`,
        `CREATE UNIQUE INDEX IF NOT EXISTS idx_task_assignees_unique ON task_assignees(task_id, user_id);`,
        `CREATE TABLE IF NOT EXISTS reminders (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
            user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            at DATETIME NOT NULL,
            kind TEXT NOT NULL,
            sent INTEGER NOT NULL DEFAULT 0
        );`,
        `CREATE TABLE IF NOT EXISTS task_results (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
            user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            text TEXT,
            file_id TEXT,
            created_at DATETIME NOT NULL
        );`,
        `CREATE TABLE IF NOT EXISTS user_states (
            user_id INTEGER PRIMARY KEY,
            state TEXT NOT NULL,
            payload TEXT,
            updated_at DATETIME NOT NULL
        );`,
    }
    for _, s := range stmts {
        if _, err := db.ExecContext(ctx, s); err != nil { return err }
    }
    return nil
}

func Now() time.Time { 
	return time.Now().In(time.Local) 
}
