package lib

import (
    "context"
    "log"
    "time"
    "fmt"
    "github.com/go-telegram-bot-api/telegram-bot-api/v5"
    "github.com/hihikaAAa/task-manager/internal/storage/sqlite"
)

type ReminderWorker struct {
    DB  *sqlite.DB
    Bot *tgbotapi.BotAPI
    Stop chan struct{}
}

func (rw *ReminderWorker) Start() {
    if rw.Stop != nil { close(rw.Stop) }
    rw.Stop = make(chan struct{})
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-rw.Stop:
                return
            case <-ticker.C:
                if err := rw.tick(); err != nil {
                    log.Println("reminder tick error:", err)
                }
            }
        }
    }()
}

func (rw *ReminderWorker) tick() error {
    ctx := context.Background()
    rows, err := rw.DB.SQL.QueryContext(ctx, `SELECT id, task_id, user_id, at, kind FROM reminders WHERE sent=0 AND at <= ? ORDER BY at LIMIT 100`, sqlite.Now())
    if err != nil { return err }
    defer rows.Close()
    for rows.Next() {
        var id, taskID, userID int64
        var at time.Time
        var kind string
        if err := rows.Scan(&id, &taskID, &userID, &at, &kind); err != nil { return err }
        msg := tgbotapi.NewMessage(userID, formatReminder(kind, taskID, at))
        if _, err := rw.Bot.Send(msg); err != nil {
            log.Println("send reminder error:", err)
            continue
        }
        _, _ = rw.DB.SQL.ExecContext(ctx, `UPDATE reminders SET sent=1 WHERE id=?`, id)
    }
    return nil
}

func formatReminder(kind string, taskID int64, at time.Time) string {
    switch kind {
    case "before":
        return fmt.Sprintf("🔔 Напоминание: скоро дедлайн по задаче #%d", taskID)
    case "deadline":
        return fmt.Sprintf("⏰ Наступил дедлайн по задаче #%d. Обновите статус.", taskID)
    case "overdue":
        return fmt.Sprintf("⛔ Просрочка по задаче #%d. Обновите статус.", taskID)
    default:
        return fmt.Sprintf("Напоминание по задаче #%d", taskID)
    }
}
