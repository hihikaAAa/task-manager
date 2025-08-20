package sqlite

import (
    "context"
    "database/sql"
    "errors"
    "time"
)

type User struct {
    ID       int64
    TgID     int64
    Username sql.NullString
    Role     string
    Name     sql.NullString
    Team     sql.NullString
    CreatedAt time.Time
}

func (d *DB) UpsertUser(ctx context.Context, tgID int64, username *string, role string) (*User, error) {
    now := Now()
    var uname interface{} = nil
    if username != nil { uname = *username }
    _, err := d.SQL.ExecContext(ctx, `
        INSERT INTO users (tg_id, username, role, created_at)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(tg_id) DO UPDATE SET username=excluded.username
    `, tgID, uname, role, now)
    if err != nil { return nil, err }
    return d.GetUserByTgID(ctx, tgID)
}

func (d *DB) GetUserByTgID(ctx context.Context, tgID int64) (*User, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT id, tg_id, username, role, name, team, created_at FROM users WHERE tg_id=?`, tgID)
    u := &User{}
    err := row.Scan(&u.ID, &u.TgID, &u.Username, &u.Role, &u.Name, &u.Team, &u.CreatedAt)
    if err != nil { 
		return nil, err }
    return u, nil
}

func (d *DB) SetWorkerProfile(ctx context.Context, tgID int64, name, team string) error {
    _, err := d.SQL.ExecContext(ctx, `UPDATE users SET name=?, team=? WHERE tg_id=?`, name, team, tgID)
    return err
}

func (d *DB) ListTeams(ctx context.Context) ([]string, error) {
    rows, err := d.SQL.QueryContext(ctx, `SELECT name FROM departments ORDER BY name`)
    if err != nil { return nil, err }
    defer rows.Close()
    var teams []string
    for rows.Next() {
        var name string
        if err := rows.Scan(&name); err != nil { return nil, err }
        teams = append(teams, name)
    }
    return teams, nil
}

func (d *DB) SetWorkerTeamByDeptID(ctx context.Context, tgID, deptID int64) error {
    dep, err := d.GetDepartmentByID(ctx, deptID)
    if err != nil { return err }
    _, err = d.SQL.ExecContext(ctx, `UPDATE users SET team=? WHERE tg_id=?`, dep.Name, tgID)
    return err
}

func (d *DB) ListWorkersByTeam(ctx context.Context, team string) ([]*User, error) {
    rows, err := d.SQL.QueryContext(ctx, `SELECT id, tg_id, username, role, name, team, created_at
        FROM users WHERE role='worker' AND team=? ORDER BY name`, team)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*User
    for rows.Next() {
        u := &User{}
        if err := rows.Scan(&u.ID, &u.TgID, &u.Username, &u.Role, &u.Name, &u.Team, &u.CreatedAt); err != nil { return nil, err }
        out = append(out, u)
    }
    return out, nil
}

func (d *DB) ListAllWorkers(ctx context.Context) ([]*User, error) {
    rows, err := d.SQL.QueryContext(ctx, `SELECT id, tg_id, username, role, name, team, created_at
        FROM users WHERE role='worker' ORDER BY team, name`)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*User
    for rows.Next() {
        u := &User{}
        if err := rows.Scan(&u.ID, &u.TgID, &u.Username, &u.Role, &u.Name, &u.Team, &u.CreatedAt); err != nil { return nil, err }
        out = append(out, u)
    }
    return out, nil
}

var ErrNotFound = errors.New("not found")

func (d *DB) FindWorkerByUsername(ctx context.Context, username string) (*User, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT id, tg_id, username, role, name, team, created_at
        FROM users WHERE role='worker' AND lower(username)=lower(?)`, username)
    u := &User{}
    if err := row.Scan(&u.ID, &u.TgID, &u.Username, &u.Role, &u.Name, &u.Team, &u.CreatedAt); err != nil {
        if err == sql.ErrNoRows { return nil, ErrNotFound }
        return nil, err
    }
    return u, nil
}

func (d *DB) DeleteWorkerByTgID(ctx context.Context, tgID int64) (int64, error) {
    u, err := d.GetUserByTgID(ctx, tgID)
    if err != nil { return 0, err }

    _, _ = d.SQL.ExecContext(ctx, `UPDATE task_assignees SET user_id=NULL WHERE user_id=?`, u.ID)
    _, _ = d.SQL.ExecContext(ctx, `DELETE FROM reminders WHERE user_id=?`, u.ID)

    res, err := d.SQL.ExecContext(ctx, `DELETE FROM users WHERE tg_id=? AND role='worker'`, tgID)
    if err != nil { return 0, err }
    n, _ := res.RowsAffected()
    return n, nil
}


func (d *DB) GetUserByID(ctx context.Context, id int64) (*User, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT id, tg_id, username, role, name, team, created_at FROM users WHERE id=?`, id)
    u := &User{}
    if err := row.Scan(&u.ID, &u.TgID, &u.Username, &u.Role, &u.Name, &u.Team, &u.CreatedAt); err != nil {
        return nil, err
    }
    return u, nil
}

func (d *DB) ListDoneExecutorsForTask(ctx context.Context, taskID int64) ([]*AssigneeWithUser, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT ta.user_id, ta.status, u.name, u.username, u.team
		FROM task_assignees ta
		LEFT JOIN users u ON u.id = ta.user_id
		WHERE ta.task_id = ? AND ta.status='done'
		ORDER BY ta.updated_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AssigneeWithUser
	for rows.Next() {
		r := &AssigneeWithUser{}
		if err := rows.Scan(&r.UserID, &r.Status, &r.Name, &r.Username, &r.Team); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
