package lib

import (
    "context"
    "database/sql"
    "fmt"
    "log"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "time"

    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
    "github.com/hihikaAAa/task-manager/internal/storage/sqlite"
)

type Bot struct {
    API    *tgbotapi.BotAPI
    DB     *sqlite.DB
    BossIDs map[int64]bool
    TZ     *time.Location
}

func NewBot(api *tgbotapi.BotAPI, db *sqlite.DB, bossIDs []int64, tz *time.Location) *Bot {
    m := map[int64]bool{}
    for _, id := range bossIDs { m[id] = true }
    return &Bot{API: api, DB: db, BossIDs: m, TZ: tz}
}

func (b *Bot) isBoss(tgID int64) bool { return b.BossIDs[tgID] }

func (b *Bot) Start() error {
    upd := tgbotapi.NewUpdate(0)
    upd.Timeout = 30
    updates := b.API.GetUpdatesChan(upd)
    for update := range updates {
        if update.Message != nil { go b.handleMessage(update.Message) }
        if update.CallbackQuery != nil { go b.handleCallback(update.CallbackQuery) }
    }
    return nil
}

func (b *Bot) handleMessage(m *tgbotapi.Message) {
    ctx := context.Background()
    role := "worker"; if b.isBoss(m.From.ID) { role = "boss" }
    var username *string; if m.From.UserName != "" { u := m.From.UserName; username = &u }
    user, err := b.DB.UpsertUser(ctx, m.From.ID, username, role); if err != nil { log.Println("upsert user:", err) }

    if m.IsCommand() {
        switch m.Command() {
        case "start":
            b.onStart(m)
        case "register":
            b.DB.SaveState(ctx, m.From.ID, StateRegName, nil)
            b.reply(m.Chat.ID, "Введите ФИО сотрудника (пример: Иванов Иван):")
        case "newtask":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Команда доступна только боссам."); return }
            b.DB.SaveState(ctx, m.From.ID, StateNewTaskWaitBody, &NewTaskDraft{})
            b.reply(m.Chat.ID, "Опишите задание текстом ИЛИ пришлите голосовое сообщение. Первая строка может быть заголовком.")
        case "mytasks":
            b.cmdMyTasks(m)
        case "teamtasks":
            b.cmdTeamTasks(m)
        case "allactive":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
            b.cmdAllActive(m)
        case "users":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
            b.cmdUsers(m)
        case "del":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
            b.cmdDeleteUser(m)
        default:
            b.reply(m.Chat.ID, "Неизвестная команда.")
        }
        return
    }

    state, _ := b.DB.LoadState(ctx, m.From.ID, nil)
    switch state {
    case StateRegName:
        name := strings.TrimSpace(m.Text)
        if name == "" { b.reply(m.Chat.ID, "Введите имя/ФИО текстом."); return }
        b.DB.SaveState(ctx, m.From.ID, StateRegTeam, map[string]string{"name": name})
        b.reply(m.Chat.ID, "Введите название команды (отдела):")
        return
    case StateRegTeam:
        var payload map[string]string
        b.DB.LoadState(ctx, m.From.ID, &payload)
        team := strings.TrimSpace(m.Text)
        if team == "" { b.reply(m.Chat.ID, "Введите команду текстом."); return }
        _ = b.DB.SetWorkerProfile(ctx, m.From.ID, payload["name"], team)
        b.DB.ClearState(ctx, m.From.ID)
        b.reply(m.Chat.ID, "Готово! Вы зарегистрированы как сотрудник: "+payload["name"]+" ("+team+").")
        return
    }

    if state == StateNewTaskWaitBody && b.isBoss(m.From.ID) {
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, m.From.ID, d)
        if m.Text != "" {
            lines := strings.SplitN(m.Text, "\n", 2)
            if len(lines) == 1 { d.Title = lines[0]; d.Description = "" } else { d.Title = lines[0]; d.Description = lines[1] }
        } else if m.Voice != nil {
            d.VoiceFileID = m.Voice.FileID
            if strings.TrimSpace(d.Title) == "" { d.Title = "Голосовое задание" }
        } else {
            b.reply(m.Chat.ID, "Пришлите текст или голосовое."); return
        }
        b.DB.SaveState(ctx, m.From.ID, StateNewTaskAssignees, d)
        b.askAssignees(m.Chat.ID)
        return
    }

    if state == StateAwaitResult {
		var pld struct{ TaskID int64 `json:"task_id"` }
		if _, err := b.DB.LoadState(ctx, m.From.ID, &pld); err != nil {
			log.Println("load state:", err)
			return
		}

		var text *string
		var fileID *string

		if m.Text != "" { t := m.Text; text = &t }
		if m.Document != nil { f := m.Document.FileID; fileID = &f }
		if m.Audio != nil   { f := m.Audio.FileID;   fileID = &f }
		if m.Voice != nil   { f := m.Voice.FileID;   fileID = &f }
		if len(m.Photo) > 0 {                    
			f := m.Photo[len(m.Photo)-1].FileID              
			fileID = &f
		}
		if m.Video != nil { f := m.Video.FileID; fileID = &f }        

		if text == nil && fileID == nil {
			b.reply(m.Chat.ID, "Пришлите текст результата или файл.")
			return
		}

		if err := b.DB.AddResult(ctx, pld.TaskID, user.ID, text, fileID); err != nil {
			log.Println("add result:", err)
		}

		t, _ := b.DB.GetTask(ctx, pld.TaskID)
		creator, _ := b.DB.GetUserByID(ctx, t.CreatorID)

		msg := fmt.Sprintf("📎 Получен результат по задаче #%d от @%s",
			pld.TaskID, ifEmpty(user.Username.String, "user"))
		b.API.Send(tgbotapi.NewMessage(creator.TgID, msg))
		if text != nil { b.API.Send(tgbotapi.NewMessage(creator.TgID, *text)) }
		if fileID != nil { b.API.Send(tgbotapi.NewDocument(creator.TgID, tgbotapi.FileID(*fileID))) }

		_ = b.DB.ClearState(ctx, m.From.ID)                       


		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✔️ Сделано", fmt.Sprintf("task_action:done:%d", pld.TaskID)),
				tgbotapi.NewInlineKeyboardButtonData("📎 Отправить ещё", fmt.Sprintf("task_action:upload:%d", pld.TaskID)),
			),
		)
		hint := tgbotapi.NewMessage(m.Chat.ID, "Результат отправлен. Теперь можно отметить задачу как выполненную.")
		hint.ReplyMarkup = kb
		b.API.Send(hint)
		return
	}
    if b.HandleTextFlow(m) {
		 return 
		}
}

func (b *Bot) onStart(m *tgbotapi.Message) {
    if b.isBoss(m.From.ID) {
        b.reply(m.Chat.ID, "Вы отмечены как Босс. Команды:\n/newtask — выдать задание\n/allactive — активные задачи (со статусами)\n/users — список сотрудников\n/del <tg_id> — удалить сотрудника\n/mytasks — ваши задачи (если вы исполнитель)\n/register — профиль сотрудника")
    } else {
        b.reply(m.Chat.ID, "Привет! Зарегистрируйтесь как сотрудник: /register\nКоманды:\n/mytasks — мои незавершённые задачи\n/teamtasks — задачи по моей команде")
    }
}

func (b *Bot) askAssignees(chatID int64) {
    ctx := context.Background()
    teams, _ := b.DB.ListTeams(ctx)
    var rows [][]tgbotapi.InlineKeyboardButton
    for _, t := range teams {
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Команда: "+t, "pick_team:"+t)))
    }
    rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Выбрать по людям", "pick_people")))
    rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Далее ▶", "assignees_next")))
    kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
    msg := tgbotapi.NewMessage(chatID, "Выберите исполнителей: можно выбирать команды и отдельных людей. Нажмите «Далее», когда закончите.")
    msg.ReplyMarkup = kb
    b.API.Send(msg)
}

func (b *Bot) handleCallback(cq *tgbotapi.CallbackQuery) {
    ctx := context.Background()
    data := cq.Data
    from := cq.From
    role := "worker"; if b.isBoss(from.ID) { role = "boss" }
    _, _ = b.DB.UpsertUser(ctx, from.ID, strPtrIf(from.UserName != "", from.UserName), role)

    if strings.HasPrefix(data, "pick_team:") {
        team := strings.TrimPrefix(data, "pick_team:")
        workers, _ := b.DB.ListWorkersByTeam(ctx, team)
        var rows [][]tgbotapi.InlineKeyboardButton
        for _, w := range workers {
            label := fmt.Sprintf("%s [%s]", nullStr(w.Name), nullStr(w.Team))
            rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("toggle_user:%d", w.TgID))))
        }
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "assignees_menu")))
        kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
        edit := tgbotapi.NewEditMessageTextAndMarkup(cq.Message.Chat.ID, cq.Message.MessageID, "Отметьте сотрудников (повторное нажатие снимает выбор):", kb)
        b.API.Send(edit)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Команда: "+team))
        return
    }
    if data == "pick_people" {
        workers, _ := b.DB.ListAllWorkers(ctx)
        var rows [][]tgbotapi.InlineKeyboardButton
        for _, w := range workers {
            label := fmt.Sprintf("%s [%s]", nullStr(w.Name), nullStr(w.Team))
            rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("toggle_user:%d", w.TgID))))
        }
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "assignees_menu")))
        kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
        edit := tgbotapi.NewEditMessageTextAndMarkup(cq.Message.Chat.ID, cq.Message.MessageID, "Отметьте сотрудников (повторное нажатие снимает выбор):", kb)
        b.API.Send(edit)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Список сотрудников"))
        return
    }
    if data == "assignees_menu" {
        b.askAssignees(cq.Message.Chat.ID)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Меню исполнителей"))
        return
    }
    if strings.HasPrefix(data, "toggle_user:") {
        tgID, _ := strconv.ParseInt(strings.TrimPrefix(data, "toggle_user:"), 10, 64)
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, from.ID, d)
        if d.AssigneeIDs == nil { d.AssigneeIDs = []int64{} }
        found := false
        for i, id := range d.AssigneeIDs { if id == tgID { d.AssigneeIDs = append(d.AssigneeIDs[:i], d.AssigneeIDs[i+1:]...); found = true; break } }
        if !found { d.AssigneeIDs = append(d.AssigneeIDs, tgID) }
        b.DB.SaveState(ctx, from.ID, StateNewTaskAssignees, d)
        b.API.Request(tgbotapi.NewCallback(cq.ID, fmt.Sprintf("Выбрано: %d", len(d.AssigneeIDs))))
        return
    }
    if data == "assignees_next" {
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, from.ID, d)
        b.DB.SaveState(ctx, from.ID, StateNewTaskDeadline, d)
        msg := tgbotapi.NewMessage(cq.Message.Chat.ID, "Введите дедлайн в формате DD.MM.YYYY HH:MM (время по "+b.TZ.String()+")\nНапример: 28.08.2025 14:30")
        b.API.Send(msg)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Выбор дедлайна"))
        return
    }

    if strings.HasPrefix(data, "rem_preset:") {
        raw := strings.TrimPrefix(data, "rem_preset:")
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, from.ID, d)
        hours, _ := b.parseReminderHours(raw)
        d.RemindHours = hours
        b.createTaskFromDraft(cq.Message.Chat.ID, from.ID, d)
        b.DB.ClearState(ctx, from.ID)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Пресет применён"))
        return
    }
    if data == "rem_none" {
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, from.ID, d)
        d.RemindHours = []int{}
        b.createTaskFromDraft(cq.Message.Chat.ID, from.ID, d)
        b.DB.ClearState(ctx, from.ID)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Без напоминаний"))
        return
    }
    if data == "rem_custom" {
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Введите часы вручную"))
        b.API.Send(tgbotapi.NewMessage(cq.Message.Chat.ID, "Введите ЧАСЫ до дедлайна через запятую (например: 48,24,6)."))
        return
    }

    if strings.HasPrefix(data, "task_action:") {
        parts := strings.Split(strings.TrimPrefix(data, "task_action:"), ":")
        if len(parts) != 2 { return }
        action := parts[0]
        taskID, _ := strconv.ParseInt(parts[1], 10, 64)
        b.onTaskAction(from.ID, cq, action, taskID)
        return
    }
}

func (b *Bot) onTaskAction(userTgID int64, cq *tgbotapi.CallbackQuery, action string, taskID int64) {
    ctx := context.Background()
    u, err := b.DB.GetUserByTgID(ctx, userTgID) 
    if err != nil { b.API.Request(tgbotapi.NewCallback(cq.ID, "Профиль не найден")); return }

    switch action {
    case "accept":
        _ = b.DB.UpdateAssigneeStatus(ctx, taskID, u.ID, "in_progress")
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Статус: В работе"))
    case "done":
        has, _ := b.DB.HasResult(ctx, taskID, u.ID)
        if !has {
            b.API.Request(tgbotapi.NewCallback(cq.ID, "Сначала отправьте результат"))
            return
        }
        _ = b.DB.UpdateAssigneeStatus(ctx, taskID, u.ID, "done")
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Отмечено как выполнено"))
        t, _ := b.DB.GetTask(ctx, taskID)
        creator, _ := b.DB.GetUserByID(ctx, t.CreatorID) 
        b.API.Send(tgbotapi.NewMessage(creator.TgID, fmt.Sprintf("✔️ Исполнитель @%s завершил задачу #%d", cq.From.UserName, taskID)))
    case "fail":
        _ = b.DB.UpdateAssigneeStatus(ctx, taskID, u.ID, "failed")
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Отмечено: не выполнено"))
    case "upload":
        b.DB.SaveState(ctx, userTgID, StateAwaitResult, map[string]any{"task_id": taskID})
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Пришлите результат сообщением или файлом"))
        b.API.Send(tgbotapi.NewMessage(cq.Message.Chat.ID, "Пришлите результат (текст/файл/голосовое)."))
    }
}


func (b *Bot) cmdMyTasks(m *tgbotapi.Message) {
    ctx := context.Background()
    u, _ := b.DB.GetUserByTgID(ctx, m.From.ID)
    ts, err := b.DB.ListActiveTasksForUser(ctx, u.ID)
    if err != nil || len(ts) == 0 { b.reply(m.Chat.ID, "Нет активных задач."); return }
    b.reply(m.Chat.ID, b.formatTasks(ts, false))
}

func (b *Bot) cmdTeamTasks(m *tgbotapi.Message) {
    ctx := context.Background()
    u, _ := b.DB.GetUserByTgID(ctx, m.From.ID)
    team := nullStr(u.Team)
    if team == "" { b.reply(m.Chat.ID, "В вашем профиле не указана команда. Используйте /register."); return }
    ts, err := b.DB.ListActiveTasksForTeam(ctx, team)
    if err != nil || len(ts) == 0 { b.reply(m.Chat.ID, "Нет активных задач по вашей команде."); return }
    b.reply(m.Chat.ID, b.formatTasks(ts, false))
}

func (b *Bot) cmdAllActive(m *tgbotapi.Message) {
    ctx := context.Background()
    ts, err := b.DB.ListActiveTasksForBoss(ctx)
    if err != nil || len(ts) == 0 { b.reply(m.Chat.ID, "Нет активных задач."); return }
    var out strings.Builder
    for _, t := range ts {
        out.WriteString(fmt.Sprintf("• #%d %s\n", t.ID, nullStr(t.Title)))
        if t.DueAt.Valid { out.WriteString("  Дедлайн: "+t.DueAt.Time.Format("02.01.2006 15:04")+"\n") }
        ass, _ := b.DB.ListAssigneesWithUsers(ctx, t.ID)
        for _, a := range ass {
            out.WriteString(fmt.Sprintf("  - %s @%s [%s]: %s\n",
                nullStr(a.Name), nullStr(a.Username), nullStr(a.Team), mapStatus(a.Status)))
        }
        out.WriteString("\n")
    }
    b.reply(m.Chat.ID, out.String())
}

func (b *Bot) cmdUsers(m *tgbotapi.Message) {
    ctx := context.Background()
    users, err := b.DB.ListAllWorkers(ctx)
    if err != nil || len(users) == 0 { b.reply(m.Chat.ID, "Сотрудников пока нет."); return }
    var out strings.Builder
    out.WriteString("Сотрудники (tg_id):\n")
    for _, u := range users {
        out.WriteString(fmt.Sprintf("- %s [%s] @%s — %d\n", nullStr(u.Name), nullStr(u.Team), nullStr(u.Username), u.TgID))
    }
    out.WriteString("\nУдалить: /del <tg_id>")
    b.reply(m.Chat.ID, out.String())
}

func (b *Bot) cmdDeleteUser(m *tgbotapi.Message) {
    args := strings.TrimSpace(m.CommandArguments())
    if args == "" { b.reply(m.Chat.ID, "Использование: /del <tg_id>"); return }
    tgID, err := strconv.ParseInt(args, 10, 64)
    if err != nil { b.reply(m.Chat.ID, "tg_id должен быть числом"); return }
    if b.isBoss(tgID) { b.reply(m.Chat.ID, "Нельзя удалить босса."); return }
    n, err := b.DB.DeleteWorkerByTgID(context.Background(), tgID)
    if err != nil { b.reply(m.Chat.ID, "Ошибка: "+err.Error()); return }
    if n == 0 { b.reply(m.Chat.ID, "Сотрудник не найден или не worker."); return }
    b.reply(m.Chat.ID, "Удалён. Связанные напоминания и назначения также удалены.")
}

func (b *Bot) formatTasks(ts []*sqlite.Task, withAssignees bool) string {
    var bld strings.Builder
    for _, t := range ts {
        bld.WriteString(fmt.Sprintf("#%d %s\n", t.ID, nullStr(t.Title)))
        if t.DueAt.Valid { bld.WriteString("Дедлайн: "+t.DueAt.Time.Format("02.01.2006 15:04")+"\n") }
        if withAssignees {
            ass, _ := b.DB.ListAssigneesWithUsers(context.Background(), t.ID)
            for _, a := range ass {
                bld.WriteString(fmt.Sprintf("• %s @%s [%s]: %s\n", nullStr(a.Name), nullStr(a.Username), nullStr(a.Team), mapStatus(a.Status)))
            }
        }
        bld.WriteString("— — —\n")
    }
    return bld.String()
}

func mapStatus(s string) string {
    switch s {
    case "new": return "🆕 Новая"
    case "in_progress": return "🚀 В работе"
    case "done": return "✔️ Готово"
    case "failed": return "⛔ Не выполнено"
    default: return s
    }
}

func (b *Bot) reply(chatID int64, text string) { b.API.Send(tgbotapi.NewMessage(chatID, text)) }

func ifEmpty(s, d string) string { if strings.TrimSpace(s)=="" { return d }; return s }
func nullStr(ns sql.NullString) string { if ns.Valid { return ns.String }; return "" }


var dlRx = regexp.MustCompile(`^([0-2]\d|3[01])\.(0\d|1[0-2])\.\d{4}\s([01]\d|2[0-3]):([0-5]\d)$`)

func (b *Bot) parseDeadline(s string) (time.Time, error) {
    s = strings.TrimSpace(s)
    if !dlRx.MatchString(s) { return time.Time{}, fmt.Errorf("bad format") }
    return time.ParseInLocation("02.01.2006 15:04", s, b.TZ)
}

func (b *Bot) parseReminderHours(s string) ([]int, error) {
    s = strings.ReplaceAll(s, " ", "")
    if s == "" { return []int{}, nil }
    parts := strings.Split(s, ",")
    var hours []int
    for _, p := range parts {
        h, err := strconv.Atoi(p)
        if err != nil { return nil, err }
        if h < 0 { h = 0 }
        hours = append(hours, h)
    }
    sort.Ints(hours)
    return hours, nil
}

func (b *Bot) HandleTextFlow(m *tgbotapi.Message) bool {
    ctx := context.Background()
    state, _ := b.DB.LoadState(ctx, m.From.ID, nil)

    if state == StateNewTaskDeadline {
        deadline, err := b.parseDeadline(m.Text)
        if err != nil {
            b.reply(m.Chat.ID, "Неверный формат. Пример: 28.08.2025 14:30")
            return true
        }
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, m.From.ID, d)
        d.DueAt = deadline.Format(time.RFC3339)
        b.DB.SaveState(ctx, m.From.ID, StateNewTaskReminders, d)

        kb := tgbotapi.NewInlineKeyboardMarkup(
            tgbotapi.NewInlineKeyboardRow(
                tgbotapi.NewInlineKeyboardButtonData("48,24,6 ч", "rem_preset:48,24,6"),
                tgbotapi.NewInlineKeyboardButtonData("24,12,1 ч", "rem_preset:24,12,1"),
            ),
            tgbotapi.NewInlineKeyboardRow(
                tgbotapi.NewInlineKeyboardButtonData("6,3,1 ч", "rem_preset:6,3,1"),
                tgbotapi.NewInlineKeyboardButtonData("Без напоминаний", "rem_none"),
            ),
            tgbotapi.NewInlineKeyboardRow(
                tgbotapi.NewInlineKeyboardButtonData("Ввести вручную", "rem_custom"),
            ),
        )
        msg := tgbotapi.NewMessage(m.Chat.ID, "Выберите пресет напоминаний или введите ЧАСЫ до дедлайна через запятую (например: 48,24,6).")
        msg.ReplyMarkup = kb
        b.API.Send(msg)
        return true
    }

    if state == StateNewTaskReminders {
        d := &NewTaskDraft{}; b.DB.LoadState(ctx, m.From.ID, d)
        hours, err := b.parseReminderHours(m.Text)
        if err != nil { b.reply(m.Chat.ID, "Не получилось разобрать список часов, пример: 48,24,6"); return true }
        d.RemindHours = hours
        b.createTaskFromDraft(m.Chat.ID, m.From.ID, d)
        b.DB.ClearState(ctx, m.From.ID)
        return true
    }
    return false
}

func (b *Bot) createTaskFromDraft(chatID, bossTgID int64, d *NewTaskDraft) {
    ctx := context.Background()
    boss, _ := b.DB.GetUserByTgID(ctx, bossTgID)

    var uids []int64
    for _, tg := range d.AssigneeIDs {
        u, err := b.DB.GetUserByTgID(ctx, tg)
        if err != nil { continue }
        uids = append(uids, u.ID)
    }

    var due sql.NullTime
    if d.DueAt != "" {
        if t, err := time.Parse(time.RFC3339, d.DueAt); err==nil { due = sql.NullTime{Time: t, Valid: true} }
    }

    task := &sqlite.Task{
        CreatorID:   boss.ID,
        Title:       sql.NullString{String: d.Title, Valid: d.Title != ""},
        Description: sql.NullString{String: d.Description, Valid: d.Description != ""},
        VoiceFileID: sql.NullString{String: d.VoiceFileID, Valid: d.VoiceFileID != ""},
        DueAt:       due,
    }
    id, err := b.DB.CreateTask(ctx, task, uids)
    if err != nil { b.reply(chatID, "Ошибка создания задачи: "+err.Error()); return }

    if due.Valid {
        var beforeTimes []time.Time
        for _, h := range d.RemindHours {
            beforeTimes = append(beforeTimes, due.Time.Add(-time.Duration(h)*time.Hour))
        }
        if len(beforeTimes) > 0 { _ = b.DB.CreateReminders(ctx, id, uids, beforeTimes, "before") }
        _ = b.DB.CreateReminders(ctx, id, uids, []time.Time{due.Time}, "deadline")
        ov := due.Time.Add(30 * time.Minute)
        _ = b.DB.CreateReminders(ctx, id, uids, []time.Time{ov}, "overdue")
    }

    for _, tg := range d.AssigneeIDs { b.sendTaskToAssignee(tg, id, task) }
    b.reply(chatID, fmt.Sprintf("Задача #%d создана и отправлена %d исполнителям.", id, len(d.AssigneeIDs)))
}

func (b *Bot) sendTaskToAssignee(tgID int64, taskID int64, t *sqlite.Task) {
    var text strings.Builder
    fmt.Fprintf(&text, "Новая задача #%d\n", taskID)
    if t.Title.Valid { text.WriteString("\n"+t.Title.String+"\n") }
    if t.Description.Valid { text.WriteString("\n"+t.Description.String+"\n") }
    if t.DueAt.Valid { text.WriteString("\nДедлайн: "+t.DueAt.Time.Format("02.01.2006 15:04")+"\n") }
    kb := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("🚀 В работу", fmt.Sprintf("task_action:accept:%d", taskID)),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("⛔ Не выполнено", fmt.Sprintf("task_action:fail:%d", taskID)),
            tgbotapi.NewInlineKeyboardButtonData("📎 Отправить результат", fmt.Sprintf("task_action:upload:%d", taskID)),
        ),
    )
    msg := tgbotapi.NewMessage(tgID, text.String()); msg.ReplyMarkup = kb
    b.API.Send(msg)
    if t.VoiceFileID.Valid { b.API.Send(tgbotapi.NewVoice(tgID, tgbotapi.FileID(t.VoiceFileID.String))) }
}

func strPtrIf(cond bool, s string) *string { if cond { 
		return &s 
	}; 
	return nil 
}
