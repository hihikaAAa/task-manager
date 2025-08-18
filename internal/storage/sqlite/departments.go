package sqlite

import (
    "context"
)

type Department struct {
    ID   int64
    Name string
}

func (d *DB) CreateDepartment(ctx context.Context, name string, createdBy *int64) (int64, error) {
    now := Now()
    var cb interface{}
    if createdBy != nil { cb = *createdBy }
    res, err := d.SQL.ExecContext(ctx, `INSERT INTO departments(name, created_at, created_by) VALUES(?,?,?)`, name, now, cb)
    if err != nil { return 0, err }
    id, _ := res.LastInsertId()
    return id, nil
}

func (d *DB) ListDepartments(ctx context.Context) ([]*Department, error) {
    rows, err := d.SQL.QueryContext(ctx, `SELECT id, name FROM departments ORDER BY name`)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []*Department
    for rows.Next() {
        var dep Department
        if err := rows.Scan(&dep.ID, &dep.Name); err != nil { return nil, err }
        out = append(out, &dep)
    }
    return out, nil
}

func (d *DB) GetDepartmentByID(ctx context.Context, id int64) (*Department, error) {
    row := d.SQL.QueryRowContext(ctx, `SELECT id, name FROM departments WHERE id=?`, id)
    dep := &Department{}
    if err := row.Scan(&dep.ID, &dep.Name); err != nil { return nil, err }
    return dep, nil
}

func (d *DB) DeleteDepartment(ctx context.Context, id int64) error {
    _, err := d.SQL.ExecContext(ctx, `DELETE FROM departments WHERE id=?`, id)
    return err
}
