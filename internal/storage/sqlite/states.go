package sqlite

import (
    "context"
    "encoding/json"
    "time"
)

type State struct {
    UserID  int64
    State   string
    Payload []byte
    UpdatedAt time.Time
}

func (d *DB) SaveState(ctx context.Context, userID int64, state string, payload any) error {
    var b []byte
    var err error
    if payload != nil {
        b, err = json.Marshal(payload)
        if err != nil { return err }
    }
    now := Now()
    _, err = d.SQL.ExecContext(ctx, `
        INSERT INTO user_states (user_id, state, payload, updated_at) VALUES (?, ?, ?, ?)
        ON CONFLICT(user_id) DO UPDATE SET state=excluded.state, payload=excluded.payload, updated_at=excluded.updated_at
    `, userID, state, b, now)
    return err
}

func (d *DB) LoadState(ctx context.Context, userID int64, dst any) (string, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT state, payload FROM user_states WHERE user_id=?`, userID)
    var state string
    var payload []byte
    if err := row.Scan(&state, &payload); err != nil { return "", err }
    if dst != nil && len(payload) > 0 {
        _ = json.Unmarshal(payload, dst)
    }
    return state, nil
}

func (d *DB) ClearState(ctx context.Context, userID int64) error {
    _, err := d.SQL.ExecContext(ctx, `DELETE FROM user_states WHERE user_id=?`, userID)
    return err
}
