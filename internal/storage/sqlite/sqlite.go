package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	SQL *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)"
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
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

		`CREATE TABLE IF NOT EXISTS departments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			created_at DATETIME NOT NULL,
			created_by INTEGER
		);`,
	}

	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}

	if err := ensureTaskAssigneesSchema(ctx, db); err != nil {
		return err
	}

	return nil
}

func ensureTaskAssigneesSchema(ctx context.Context, db *sql.DB) error {
	var cnt int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_assignees'`,
	).Scan(&cnt); err != nil {
		return err
	}
	if cnt == 0 {
		if _, err := db.ExecContext(ctx, `
			CREATE TABLE task_assignees (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
				user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
				status TEXT NOT NULL DEFAULT 'new',
				updated_at DATETIME NOT NULL
			);
		`); err != nil {
			return err
		}
		_, _ = db.ExecContext(ctx, `
			CREATE UNIQUE INDEX IF NOT EXISTS idx_task_assignees_unique
			ON task_assignees(task_id, user_id)
			WHERE user_id IS NOT NULL;
		`)
		return nil
	}

	type colInfo struct {
		notnull int
	}
	cols := map[string]colInfo{}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(task_assignees)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		cols[name] = colInfo{notnull: notnull}
	}
	userNotNull := 0
	if c, ok := cols["user_id"]; ok {
		userNotNull = c.notnull
	}

	onDelete := ""
	fkRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(task_assignees)`)
	if err != nil {
		return err
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDel, match string
		if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDel, &match); err != nil {
			return err
		}
		if from == "user_id" {
			onDelete = onDel
			break
		}
	}
	needMigrate := (userNotNull == 1) || (strings.ToUpper(onDelete) != "SET NULL")

	if !needMigrate {
		_, _ = db.ExecContext(ctx, `
			CREATE UNIQUE INDEX IF NOT EXISTS idx_task_assignees_unique
			ON task_assignees(task_id, user_id)
			WHERE user_id IS NOT NULL;
		`)
		return nil
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF;`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE task_assignees_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			status TEXT NOT NULL DEFAULT 'new',
			updated_at DATETIME NOT NULL
		);
	`); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_assignees_new (id, task_id, user_id, status, updated_at)
		SELECT id, task_id, user_id, status, updated_at FROM task_assignees;
	`); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE task_assignees;`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE task_assignees_new RENAME TO task_assignees;`); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_task_assignees_unique
		ON task_assignees(task_id, user_id)
		WHERE user_id IS NOT NULL;
	`); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `PRAGMA foreign_keys=ON;`)
	return nil
}

func Now() time.Time { return time.Now().In(time.Local) }
