package sqlite

import (
    "context"
    "database/sql"
    "time"
    "strings"
)

type Task struct {
    ID          int64
    CreatorID   int64
    Title       sql.NullString
    Description sql.NullString
    VoiceFileID sql.NullString
    DueAt       sql.NullTime
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type TaskAssignee struct {
    ID        int64
    TaskID    int64
    UserID    int64
    Status    string
    UpdatedAt time.Time
}

type AssigneeRow struct {
    TgID     int64
    Name     sql.NullString
    Username sql.NullString
    Team     sql.NullString
    Status   string
}

type AssigneeWithUser struct {
	UserID   sql.NullInt64
	Status   string
	Name     sql.NullString
	Username sql.NullString
	Team     sql.NullString
	TgID     sql.NullInt64
}

func (d *DB) CreateTask(ctx context.Context, t *Task, assigneeIDs []int64) (int64, error) {
    now := Now()
    res, err := d.SQL.ExecContext(ctx, `
        INSERT INTO tasks (creator_id, title, description, voice_file_id, due_at, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
    `, t.CreatorID, t.Title, t.Description, t.VoiceFileID, t.DueAt, now, now)
    if err != nil { return 0, err }
    id, _ := res.LastInsertId()
    for _, uid := range assigneeIDs {
        _, err := d.SQL.ExecContext(ctx, `INSERT INTO task_assignees (task_id, user_id, status, updated_at) VALUES (?, ?, 'new', ?)`, id, uid, now)
        if err != nil { return 0, err }
    }
    return id, nil
}

func (d *DB) UpdateAssigneeStatus(ctx context.Context, taskID, userID int64, status string) (bool, error) {
    now := Now()
    res, err := d.SQL.ExecContext(ctx, `
        UPDATE task_assignees
        SET status=?, updated_at=?
        WHERE task_id=? AND user_id=? AND status<>?`,
        status, now, taskID, userID, status)
    if err != nil { return false, err }
    n, _ := res.RowsAffected()
    return n > 0, nil
}


func (d *DB) GetTask(ctx context.Context, id int64) (*Task, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT id, creator_id, title, description, voice_file_id, due_at, created_at, updated_at FROM tasks WHERE id=?`, id)
    t := &Task{}
    if err := row.Scan(&t.ID, &t.CreatorID, &t.Title, &t.Description, &t.VoiceFileID, &t.DueAt, &t.CreatedAt, &t.UpdatedAt); err != nil { return nil, err }
    return t, nil
}

func (d *DB) ListActiveTasksForBoss(ctx context.Context) ([]*Task, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT t.id, t.creator_id, t.title, t.description, t.voice_file_id, t.due_at, t.created_at, t.updated_at
		FROM tasks t
		LEFT JOIN task_assignees ta ON ta.task_id = t.id
		GROUP BY t.id
		HAVING COUNT(ta.id)=0
		   OR SUM(CASE WHEN ta.status!='done' THEN 1 ELSE 0 END) > 0
		ORDER BY t.created_at DESC`)
	if err != nil { return nil, err }
	defer rows.Close()

	var out []*Task
	for rows.Next() {
		t := &Task{}
		if err := rows.Scan(&t.ID,&t.CreatorID,&t.Title,&t.Description,&t.VoiceFileID,&t.DueAt,&t.CreatedAt,&t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}


func (d *DB) ListActiveTasksForUser(ctx context.Context, userID int64) ([]*Task, error) {
    rows, err := d.SQL.QueryContext(ctx, `
        SELECT t.id, t.creator_id, t.title, t.description, t.voice_file_id, t.due_at, t.created_at, t.updated_at
        FROM tasks t
        JOIN task_assignees ta ON ta.task_id = t.id
        WHERE ta.user_id=? AND ta.status != 'done'
        ORDER BY t.created_at DESC
    `, userID)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*Task
    for rows.Next() {
        t := &Task{}
        if err := rows.Scan(&t.ID, &t.CreatorID, &t.Title, &t.Description, &t.VoiceFileID, &t.DueAt, &t.CreatedAt, &t.UpdatedAt); err != nil { return nil, err }
        out = append(out, t)
    }
    return out, nil
}

func (d *DB) ListActiveTasksForTeam(ctx context.Context, team string) ([]*Task, error) {
    rows, err := d.SQL.QueryContext(ctx, `
        SELECT DISTINCT t.id, t.creator_id, t.title, t.description, t.voice_file_id, t.due_at, t.created_at, t.updated_at
        FROM tasks t
        JOIN task_assignees ta ON ta.task_id = t.id
        JOIN users u ON u.id = ta.user_id
        WHERE u.team = ? AND ta.status != 'done'
        ORDER BY t.created_at DESC
    `, team)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*Task
    for rows.Next() {
        t := &Task{}
        if err := rows.Scan(&t.ID, &t.CreatorID, &t.Title, &t.Description, &t.VoiceFileID, &t.DueAt, &t.CreatedAt, &t.UpdatedAt); err != nil { return nil, err }
        out = append(out, t)
    }
    return out, nil
}

func (d *DB) GetAssignees(ctx context.Context, taskID int64) ([]*TaskAssignee, error) {
    rows, err := d.SQL.QueryContext(ctx, `SELECT id, task_id, user_id, status, updated_at FROM task_assignees WHERE task_id=?`, taskID)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*TaskAssignee
    for rows.Next() {
        a := &TaskAssignee{}
        if err := rows.Scan(&a.ID, &a.TaskID, &a.UserID, &a.Status, &a.UpdatedAt); err != nil { return nil, err }
        out = append(out, a)
    }
    return out, nil
}


func (d *DB) ListAssigneesWithUsers(ctx context.Context, taskID int64) ([]*AssigneeRow, error) {
    rows, err := d.SQL.QueryContext(ctx, `
        SELECT u.tg_id, u.name, u.username, u.team, ta.status
        FROM task_assignees ta
        JOIN users u ON u.id = ta.user_id
        WHERE ta.task_id = ?
        ORDER BY u.team, u.name
    `, taskID)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*AssigneeRow
    for rows.Next() {
        r := &AssigneeRow{}
        if err := rows.Scan(&r.TgID, &r.Name, &r.Username, &r.Team, &r.Status); err != nil { return nil, err }
        out = append(out, r)
    }
    return out, nil
}

func (d *DB) CreateReminders(ctx context.Context, taskID int64, userIDs []int64, reminderTimes []time.Time, kind string) error {
    for _, uid := range userIDs {
        for _, at := range reminderTimes {
            _, err := d.SQL.ExecContext(ctx, `INSERT INTO reminders (task_id, user_id, at, kind, sent) VALUES (?, ?, ?, ?, 0)`, taskID, uid, at, kind)
            if err != nil { return err }
        }
    }
    return nil
}

func (d *DB) DueAtForTask(ctx context.Context, taskID int64) (time.Time, bool, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT due_at FROM tasks WHERE id=?`, taskID)
    var due sql.NullTime
    if err := row.Scan(&due); err != nil { return time.Time{}, false, err }
    return due.Time, due.Valid, nil
}

func (d *DB) AddResult(ctx context.Context, taskID, userID int64, text, fileID *string) error {
    now := Now()
    _, err := d.SQL.ExecContext(ctx, `INSERT INTO task_results (task_id, user_id, text, file_id, created_at) VALUES (?, ?, ?, ?, ?)`,
        taskID, userID, text, fileID, now)
    return err
}

func (d *DB) ListResults(ctx context.Context, taskID int64) ([]string, error) {
    rows, err := d.SQL.QueryContext(ctx, `SELECT coalesce(text,'') || coalesce(file_id,'') FROM task_results WHERE task_id=? ORDER BY created_at`, taskID)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []string
    for rows.Next() { var s string; if err := rows.Scan(&s); err != nil { return nil, err }; out = append(out, s) }
    return out, nil
}

func (d *DB) SearchWorkers(ctx context.Context, q string) ([]*User, error) {
    q = strings.ToLower(q)
    rows, err := d.SQL.QueryContext(ctx, `
        SELECT id, tg_id, username, role, name, team, created_at
        FROM users
        WHERE role='worker' AND (
            lower(coalesce(username,'')) LIKE '%'||?||'%'
            OR lower(coalesce(name,'')) LIKE '%'||?||'%'
            OR lower(coalesce(team,'')) LIKE '%'||?||'%'
        )
        ORDER BY team, name
    `, q, q, q)
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

func (d *DB) HasResult(ctx context.Context, taskID, userID int64) (bool, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT 1 FROM task_results WHERE task_id=? AND user_id=? LIMIT 1`, taskID, userID)
    var one int
    if err := row.Scan(&one); err != nil {
        if err == sql.ErrNoRows { return false, nil }
        return false, err
    }
    return true, nil
}

// sqlite/tasks.go
func (d *DB) ListDoneTasksForBoss(ctx context.Context, creatorID int64, limit int) ([]*Task, []time.Time, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT t.id, t.creator_id, t.title, t.description, t.voice_file_id, t.due_at,
		       t.created_at, t.updated_at,
		       MAX(ta.updated_at) AS completed_at
		FROM tasks t
		JOIN task_assignees ta ON ta.task_id = t.id AND ta.status='done'
		WHERE t.creator_id = ?                         -- << ключевой фильтр по автору
		GROUP BY t.id
		ORDER BY completed_at DESC
		LIMIT ?`, creatorID, limit)
	if err != nil { return nil, nil, err }
	defer rows.Close()

	var ts []*Task
	var comps []time.Time
	for rows.Next() {
		t := &Task{}
		var comp time.Time
		if err := rows.Scan(&t.ID,&t.CreatorID,&t.Title,&t.Description,&t.VoiceFileID,&t.DueAt,&t.CreatedAt,&t.UpdatedAt,&comp); err != nil {
			return nil, nil, err
		}
		ts = append(ts, t)
		comps = append(comps, comp)
	}
	return ts, comps, nil
}


func (d *DB) ListDoneTasksForUser(ctx context.Context, userID int64, limit int) ([]*Task, []time.Time, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT t.id, t.creator_id, t.title, t.description, t.voice_file_id, t.due_at, t.created_at, t.updated_at,
		       ta.updated_at AS completed_at
		FROM tasks t
		JOIN task_assignees ta ON ta.task_id = t.id
		WHERE ta.user_id = ? AND ta.status='done'
		ORDER BY ta.updated_at DESC
		LIMIT ?`, userID, limit)
	if err != nil { return nil, nil, err }
	defer rows.Close()

	var ts []*Task; var comps []time.Time
	for rows.Next() {
		t := &Task{}; var comp time.Time
		if err := rows.Scan(&t.ID,&t.CreatorID,&t.Title,&t.Description,&t.VoiceFileID,&t.DueAt,&t.CreatedAt,&t.UpdatedAt,&comp); err != nil {
			return nil, nil, err
		}
		ts = append(ts, t); comps = append(comps, comp)
	}
	return ts, comps, nil
}

func (d *DB) ListTasksWithoutAssignees(ctx context.Context) ([]*Task, error) {
    rows, err := d.SQL.QueryContext(ctx, `
        SELECT t.id, t.creator_id, t.title, t.description, t.voice_file_id, t.due_at, t.created_at, t.updated_at
        FROM tasks t
        LEFT JOIN task_assignees ta ON ta.task_id = t.id
        GROUP BY t.id
        HAVING COUNT(ta.id)=0
        ORDER BY t.created_at DESC`)
    if err != nil { return nil, err }
    defer rows.Close()

    var out []*Task
    for rows.Next() {
        t := &Task{}
        if err := rows.Scan(&t.ID,&t.CreatorID,&t.Title,&t.Description,&t.VoiceFileID,&t.DueAt,&t.CreatedAt,&t.UpdatedAt); err != nil {
            return nil, err
        }
        out = append(out, t)
    }
    return out, nil
}

func (d *DB) ListAssigneesWithUsersAny(ctx context.Context, taskID int64) ([]*AssigneeWithUser, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT ta.user_id, ta.status, u.name, u.username, u.team, u.tg_id
		FROM task_assignees ta
		LEFT JOIN users u ON u.id = ta.user_id
		WHERE ta.task_id = ?
		ORDER BY COALESCE(u.name, u.username)`, taskID)
	if err != nil { return nil, err }
	defer rows.Close()

	var out []*AssigneeWithUser
	for rows.Next() {
		r := &AssigneeWithUser{}
		if err := rows.Scan(&r.UserID, &r.Status, &r.Name, &r.Username, &r.Team, &r.TgID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (d *DB) IsAssigneeDone(ctx context.Context, taskID, userID int64) (bool, error) {
	var st string
	err := d.SQL.QueryRowContext(ctx,
		`SELECT status FROM task_assignees WHERE task_id=? AND user_id=?`, taskID, userID).Scan(&st)
	if err == sql.ErrNoRows { return false, nil }
	if err != nil { return false, err }
	return st == "done", nil
}

func (d *DB) ListAllTasks(ctx context.Context) ([]*Task, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT id, creator_id, title, description, voice_file_id, due_at, created_at, updated_at
		FROM tasks ORDER BY id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t := &Task{}
		if err := rows.Scan(&t.ID,&t.CreatorID,&t.Title,&t.Description,&t.VoiceFileID,&t.DueAt,&t.CreatedAt,&t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (d *DB) DeleteTask(ctx context.Context, taskID int64) (int64, error) {
	res, err := d.SQL.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, taskID)
	if err != nil { return 0, err }
	return res.RowsAffected()
}

func (d *DB) DeleteAllTasks(ctx context.Context) (int64, error) {
	res, err := d.SQL.ExecContext(ctx, `DELETE FROM tasks`)
	if err != nil { return 0, err }
	return res.RowsAffected()
}

func (d *DB) DeleteTasksByExactTitle(ctx context.Context, title string) (int64, error) {
    res, err := d.SQL.ExecContext(ctx, `DELETE FROM tasks WHERE title = ?`, title)
    if err != nil { return 0, err }
    return res.RowsAffected()
}

func (d *DB) FindTasksByTitleLike(ctx context.Context, q string, limit int) ([]*Task, error) {
    rows, err := d.SQL.QueryContext(ctx, `
        SELECT id, creator_id, title, description, voice_file_id, due_at, created_at, updated_at
        FROM tasks
        WHERE title LIKE ?
        ORDER BY created_at DESC
        LIMIT ?`, "%"+q+"%", limit)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*Task
    for rows.Next() {
        t := &Task{}
        if err := rows.Scan(&t.ID,&t.CreatorID,&t.Title,&t.Description,&t.VoiceFileID,&t.DueAt,&t.CreatedAt,&t.UpdatedAt); err != nil {
            return nil, err
        }
        out = append(out, t)
    }
    return out, nil
}

func (d *DB) ListDoneExecutorsForTask(ctx context.Context, taskID int64) ([]*User, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT u.id, u.tg_id, u.username, u.role, u.name, u.team, u.created_at
		FROM task_assignees ta
		JOIN users u ON u.id = ta.user_id
		WHERE ta.task_id = ? AND ta.status = 'done'
		ORDER BY ta.updated_at DESC`, taskID)
	if err != nil { return nil, err }
	defer rows.Close()

	var out []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.TgID, &u.Username, &u.Role, &u.Name, &u.Team, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}