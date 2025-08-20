package sqlite

import (
	"context"
	"database/sql"
	"time"
)


type Reminder struct {
	ID     int64
	TaskID int64
	UserID sql.NullInt64
	At     time.Time
	Kind   string 
}


func (d *DB) ListDueReminders(ctx context.Context, until time.Time) ([]*Reminder, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT id, task_id, user_id, at, kind
		FROM reminders
		WHERE sent=0 AND at<=?
		ORDER BY at`, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Reminder
	for rows.Next() {
		r := &Reminder{}
		if err := rows.Scan(&r.ID, &r.TaskID, &r.UserID, &r.At, &r.Kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}


func (d *DB) MarkReminderSent(ctx context.Context, id int64) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE reminders SET sent=1 WHERE id=?`, id)
	return err
}
func (d *DB) MarkAllRemindersSentFor(ctx context.Context, taskID, userID int64) error {
    _, err := d.SQL.ExecContext(ctx, `
        UPDATE reminders SET sent=1
        WHERE task_id=? AND user_id=? AND sent=0
    `, taskID, userID)
    return err
}