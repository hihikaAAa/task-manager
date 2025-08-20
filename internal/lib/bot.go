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

var menuKB = tgbotapi.NewReplyKeyboard(
    tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("Menu")),
)

func init() {
    menuKB.OneTimeKeyboard = false
    menuKB.ResizeKeyboard = true
}

func NewBot(api *tgbotapi.BotAPI, db *sqlite.DB, bossIDs []int64, tz *time.Location) *Bot {
    m := map[int64]bool{}
    for _, id := range bossIDs { m[id] = true }
    return &Bot{API: api, DB: db, BossIDs: m, TZ: tz}
}

func (b *Bot) isBoss(tgID int64) bool { 
    return b.BossIDs[tgID]
 }

func (b *Bot) Start() error {
    upd := tgbotapi.NewUpdate(0)
    upd.Timeout = 30
    updates := b.API.GetUpdatesChan(upd)

    b.startOrphansDailyPing(10) 
    b.startRemindersLoop()       

    for update := range updates {
        if update.Message != nil { go b.handleMessage(update.Message) }
        if update.CallbackQuery != nil { go b.handleCallback(update.CallbackQuery) }
    }
    return nil
}

func (b *Bot) startRemindersLoop() {
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        for range ticker.C {
            b.dispatchReminders()
        }
    }()
}

func (b *Bot) dispatchReminders() {
    ctx := context.Background()
    due := time.Now().In(b.TZ)
    rs, err := b.DB.ListDueReminders(ctx, due)
    if err != nil || len(rs) == 0 { return }

    for _, r := range rs {
        t, err := b.DB.GetTask(ctx, r.TaskID); if err != nil { _ = b.DB.MarkReminderSent(ctx, r.ID); continue }
        title := nullStr(t.Title)

        send := func(chatID int64, txt string) {
            _, _ = b.API.Send(tgbotapi.NewMessage(chatID, txt))
        }

        switch r.Kind {
        case "before", "deadline":
            if r.UserID.Valid {
                u, err := b.DB.GetUserByID(ctx, r.UserID.Int64)
                if err == nil {
                    if r.Kind == "before" {
                        send(u.TgID, "⏰ Напоминание: задача «"+title+"» скоро дедлайн.")
                    } else {
                        send(u.TgID, "⌛ Дедлайн по задаче «"+title+"». Обновите статус или отправьте результат.")
                    }
                }
            }
        case "overdue":
            if r.UserID.Valid {
                uid := r.UserID.Int64
                done, _ := b.DB.IsAssigneeDone(ctx, r.TaskID, uid) 
                hasRes, _ := b.DB.HasResult(ctx, r.TaskID, uid)
                if !done && !hasRes {
                    if u, err := b.DB.GetUserByID(ctx, uid); err == nil {
                        send(u.TgID, "❗ Просрочено: задача «"+title+"».")
                    }
                    if creator, err := b.DB.GetUserByID(ctx, t.CreatorID); err == nil {
                        send(creator.TgID, "❗ Просрочена задача «"+title+"». Проверьте статус.")
                    }
                }
            } else {
                if creator, err := b.DB.GetUserByID(ctx, t.CreatorID); err == nil {
                    send(creator.TgID, "❗ Просрочена задача «"+title+"» без назначенного исполнителя.")
                }
	}
     _ = b.DB.MarkReminderSent(ctx, r.ID)
        }
    }
}


func (b *Bot) showMenu(chatID int64, boss bool) {
    var txt string
    if boss {
        txt = "Меню:\n/newtask — выдать задание\n/allactive — активные задачи\n/users — список сотрудников\n/del <tg_id> — удалить сотрудника\n/dept_add <name> - добавить отдел\n/dept_list - список отделов\n/dept_del <id> - удалить отдел\n/done — выполненные задачи\n/error <сообщение> — отправить ошибку боссу"
    } else {
        txt = "Меню:\n/register — регистрация/обновить отдел\n/mytasks — мои задачи\n/teamtasks — задачи моей команды\n/mydone — мои выполненные задачи\n/error <сообщение> — отправить ошибку боссу"
    }
    msg := tgbotapi.NewMessage(chatID, txt)
    msg.ReplyMarkup = menuKB
    b.API.Send(msg)
}

func (b *Bot) handleMessage(m *tgbotapi.Message) {
    ctx := context.Background()
    role := "worker"; if b.isBoss(m.From.ID) { role = "boss" }
    var username *string; if m.From.UserName != "" { u := m.From.UserName; username = &u }
    user, err := b.DB.UpsertUser(ctx, m.From.ID, username, role); if err != nil { log.Println("upsert user:", err) }
    state, _ := b.DB.LoadState(ctx, m.From.ID, nil) 

    isNewTaskState := func(s string) bool {
        switch s {
        case StateNewTaskTitle, StateNewTaskBody, StateNewTaskAssignees, StateNewTaskDeadline, StateNewTaskReminders:
            return true
        }
        return false
    }
    if m.IsCommand() {
        cmd := m.Command()
        if isNewTaskState(state) && cmd != "newtask" {
            _ = b.DB.ClearState(ctx, m.From.ID)
            state = "" 
        }   
        switch m.Command() {
        case "start":
            b.onStart(m)
        case "register":
            b.DB.SaveState(ctx, m.From.ID, StateRegName, nil)
            b.reply(m.Chat.ID, "Введите ФИО сотрудника (пример: Иванов Иван):")
        case "newtask":
            if !b.isBoss(m.From.ID) { 
                b.reply(m.Chat.ID, "Команда доступна только боссам."); 
            return 
            }
            b.DB.SaveState(ctx, m.From.ID, StateNewTaskTitle, &NewTaskDraft{})
            b.reply(m.Chat.ID, "Введите НАЗВАНИЕ задачи (только текстом):")
        case "mytasks":
            if b.isBoss(m.From.ID) { 
                b.reply(m.Chat.ID, "Команда недоступна для боссов.");
                 return
                }
            b.cmdMyTasks(m)
        case "teamtasks":
            if b.isBoss(m.From.ID) { 
                b.reply(m.Chat.ID, "Команда недоступна для боссов.");
                return 
            }
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
        case "menu":
            b.showMenu(m.Chat.ID, b.isBoss(m.From.ID))
            return
        case "dept_add":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
            name := strings.TrimSpace(m.CommandArguments())
            if name == "" { 
                b.reply(m.Chat.ID, "Добавление отдела:\n/dept_add <название>\nНапример: /dept_add Маркетинг");
                 return
                 }
            _, err := b.DB.CreateDepartment(ctx, name, nil)
            if err != nil { 
                b.reply(m.Chat.ID, "Ошибка: "+err.Error());
                 return 
                }
            b.reply(m.Chat.ID, "Отдел создан: "+name)
        case "dept_list":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
            deps, _ := b.DB.ListDepartments(ctx)
            if len(deps)==0 { 
                b.reply(m.Chat.ID, "Отделов пока нет.\nДобавьте: /dept_add <название>");
                 return 
                }
            var sb strings.Builder
            sb.WriteString("Отделы (id → название):\n")
            for _, d := range deps { sb.WriteString(fmt.Sprintf("- [%d] %s\n", d.ID, d.Name)) }
            sb.WriteString("\nКоманды:\n• /dept_add <название> — создать отдел\n• /dept_del <id> — удалить отдел")
            b.reply(m.Chat.ID, sb.String())
        case "dept_del":
            if !b.isBoss(m.From.ID) { 
                b.reply(m.Chat.ID, "Только для боссов."); 
                return 
            }
            idStr := strings.TrimSpace(m.CommandArguments())
            if idStr == "" {
                b.reply(m.Chat.ID, "Удаление отдела:\n/dept_del <id>\nСписок id: /dept_list")
                return
            }       
            id, err := strconv.ParseInt(idStr, 10, 64); if err != nil { b.reply(m.Chat.ID, "id должен быть числом"); return }
            if err := b.DB.DeleteDepartment(ctx, id); err != nil { b.reply(m.Chat.ID, "Ошибка: "+err.Error()); return }
            b.reply(m.Chat.ID, "Отдел удалён.")
        case "error":
            arg := strings.TrimSpace(m.CommandArguments())
            if arg == "" {
                b.DB.SaveState(ctx, m.From.ID, StateErrorReport, nil)
                b.reply(m.Chat.ID, "Опишите проблему одним сообщением — я передам её боссу.")
                return
            }
            b.forwardError(m.From, arg)
            b.reply(m.Chat.ID, "Спасибо! Сообщение об ошибке отправлено.")
            return
        case "done":
            if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
            b.cmdDone(m)
        case "mydone":
            if b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Команда недоступна для боссов."); return }
            b.cmdMyDone(m)
        case "task_del":
	        if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
	        b.cmdTaskDel(m)
        case "task_del_all":
	        if !b.isBoss(m.From.ID) { b.reply(m.Chat.ID, "Только для боссов."); return }
	        b.cmdTaskDelAll(m)

        default:
            b.reply(m.Chat.ID, "Неизвестная команда.")
        }
        return
    }
    if strings.EqualFold(m.Text, "menu") || m.Text == "Меню" {
        b.showMenu(m.Chat.ID, b.isBoss(m.From.ID))
        return
    }
    state, _ = b.DB.LoadState(ctx, m.From.ID, nil)
    switch state {
        case StateRegName:
            name := strings.TrimSpace(m.Text)
            if name == "" { b.reply(m.Chat.ID, "Введите имя/ФИО текстом."); return }
            b.DB.SaveState(ctx, m.From.ID, StateRegTeam, map[string]string{"name": name})
            b.sendDeptKeyboard(m.Chat.ID)
            return
        case StateRegTeam:
            b.sendDeptKeyboard(m.Chat.ID)
            return
        case StateNewTaskTitle:
            title := strings.TrimSpace(m.Text)
            if title == "" { 
                b.reply(m.Chat.ID, "Название не может быть пустым. Введите текст."); 
                return }
            d := &NewTaskDraft{}
            b.DB.LoadState(ctx, m.From.ID, d)
            d.Title = title
            b.DB.SaveState(ctx, m.From.ID, StateNewTaskBody, d)
            b.reply(m.Chat.ID, "Теперь отправьте содержание задачи: текст ИЛИ голосовое.")
            return
        case StateNewTaskBody:
            d := &NewTaskDraft{}
            b.DB.LoadState(ctx, m.From.ID, d)
            if m.Text != "" { 
                d.Description = m.Text 
            }
            if m.Voice != nil { 
                d.VoiceFileID = m.Voice.FileID
             }
            if d.Description == "" && d.VoiceFileID == "" {
                b.reply(m.Chat.ID, "Пришлите текст или голосовое сообщение.")
                return
            }
            b.DB.SaveState(ctx, m.From.ID, StateNewTaskAssignees, d)
            b.askAssignees(m.Chat.ID)
            return        
    }
    if state == StateErrorReport {
        txt := m.Text
        if strings.TrimSpace(txt) == "" { b.reply(m.Chat.ID, "Нужно текстовое описание ошибки."); return }
        b.forwardError(m.From, txt)
        b.DB.ClearState(ctx, m.From.ID)
        b.reply(m.Chat.ID, "Спасибо! Сообщение об ошибке отправлено.")
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
        fileKind := "" 

        if m.Text != "" { t := m.Text; text = &t }
        switch {
        case m.Document != nil:
            f := m.Document.FileID; fileID = &f; fileKind = "document"
        case m.Voice != nil:
            f := m.Voice.FileID; fileID = &f; fileKind = "voice"
        case m.Audio != nil:
            f := m.Audio.FileID; fileID = &f; fileKind = "audio"
        case len(m.Photo) > 0:
            f := m.Photo[len(m.Photo)-1].FileID; fileID = &f; fileKind = "photo"
        case m.Video != nil:
            f := m.Video.FileID; fileID = &f; fileKind = "video"
        }

        if text == nil && fileID == nil {
            b.reply(m.Chat.ID, "Пришлите текст результата или файл.")
            return
        }

        if err := b.DB.AddResult(ctx, pld.TaskID, user.ID, text, fileID); err != nil {
            log.Println("add result:", err)
        }

        t, _ := b.DB.GetTask(ctx, pld.TaskID)
        creator, _ := b.DB.GetUserByID(ctx, t.CreatorID)

        fullName := nullStr(user.Name)
        if strings.TrimSpace(fullName) == "" {
            fullName = strings.TrimSpace(strings.TrimSpace(m.From.FirstName + " " + m.From.LastName))
        }
        tag := ifEmpty(user.Username.String, m.From.UserName)
        if tag != "" { tag = "(@" + tag + ")" }

        head := fmt.Sprintf("📎 Получен результат по задаче «%s» от %s %s",
            nullStr(t.Title), strings.TrimSpace(fullName), strings.TrimSpace(tag))

        if _, err := b.API.Send(tgbotapi.NewMessage(creator.TgID, head)); err != nil {
            log.Println("send head to boss:", err)
        }
        if text != nil {
            if _, err := b.API.Send(tgbotapi.NewMessage(creator.TgID, *text)); err != nil {
                log.Println("send text to boss:", err)
            }
        }
        if fileID != nil {
            var _, err error
            switch fileKind {
            case "document":
                _, err = b.API.Send(tgbotapi.NewDocument(creator.TgID, tgbotapi.FileID(*fileID)))
            case "voice":
                _, err = b.API.Send(tgbotapi.NewVoice(creator.TgID, tgbotapi.FileID(*fileID)))
            case "audio":
                _, err = b.API.Send(tgbotapi.NewAudio(creator.TgID, tgbotapi.FileID(*fileID)))
            case "photo":
                _, err = b.API.Send(tgbotapi.NewPhoto(creator.TgID, tgbotapi.FileID(*fileID)))
            case "video":
                _, err = b.API.Send(tgbotapi.NewVideo(creator.TgID, tgbotapi.FileID(*fileID)))
            }
            if err != nil { log.Println("send file to boss:", err) }
        }

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
    func (b *Bot) forwardError(from *tgbotapi.User, text string) {
    log.Printf("ERROR REPORT from @%s (%d): %s", from.UserName, from.ID, text)
    var bossID int64 = 653296078
    msg := fmt.Sprintf("🐞 Error report от @%s (%d):\n%s", from.UserName, from.ID, text)
    b.API.Send(tgbotapi.NewMessage(bossID, msg))
    }

func (b *Bot) sendDeptKeyboard(chatID int64) {
    deps, _ := b.DB.ListDepartments(context.Background())
    if len(deps)==0 {
        b.reply(chatID, "Отделы ещё не созданы. Попросите босса выполнить /dept_add <название>.")
        return
    }
    var rows [][]tgbotapi.InlineKeyboardButton
    for _, d := range deps {
        rows = append(rows, tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(d.Name, fmt.Sprintf("choose_dept:%d", d.ID)),
        ))
    }
    kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
    msg := tgbotapi.NewMessage(chatID, "Выберите ваш отдел кнопкой:")
    msg.ReplyMarkup = kb
    b.API.Send(msg)
}


func (b *Bot) onStart(m *tgbotapi.Message) {
    txt := "Привет! Зарегистрируйтесь как сотрудник: /register\nКоманды:\n/mytasks — мои задачи\n/teamtasks — задачи команды\n/menu — показать меню"
    if b.isBoss(m.From.ID) {
        txt = "Вы Босс. Команды:\n/newtask — выдать задание\n/allactive — активные задачи\n/users — список сотрудников\n/del <tg_id> — удалить сотрудника\n/dept_add <name> — создать отдел\n/dept_list — список отделов\n/dept_del <id> — удалить отдел\n/menu — показать меню"
    }
    msg := tgbotapi.NewMessage(m.Chat.ID, txt)
    msg.ReplyMarkup = menuKB                     
    b.API.Send(msg)
}

func (b *Bot) askAssignees(chatID int64) {
	ctx := context.Background()
	teams, _ := b.DB.ListTeams(ctx)
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, t := range teams {
		rows = append(rows,
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Отделом: "+t, "assign_team:"+t),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Выбрать по людям («"+t+"»)", "pick_team:"+t),
			),
		)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("Выбрать из всех сотрудников", "pick_people"),
	))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("Далее ▶", "assignees_next"),
	))
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewMessage(chatID, "Выберите исполнителей: можно выдать сразу на весь отдел или выбрать конкретных людей. Нажмите «Далее», чтобы продолжить.")
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
            label := fmt.Sprintf("%s [%s]", b.userLabel(w), nullStr(w.Team))
            rows = append(rows,
                tgbotapi.NewInlineKeyboardRow(
                    tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("toggle_user:%d", w.TgID)),
                ),
            )
        }
        rows = append(rows,
            tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "assignees_menu")),
            tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Далее ▶", "assignees_next")),
        )
        kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
        edit := tgbotapi.NewEditMessageTextAndMarkup(cq.Message.Chat.ID, cq.Message.MessageID,
            "Отметьте сотрудников (повторное нажатие снимает выбор):", kb)
        b.API.Send(edit)
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Команда: "+team))
        return
    }

    if data == "pick_people" {
        workers, _ := b.DB.ListAllWorkers(ctx)
        var rows [][]tgbotapi.InlineKeyboardButton
        for _, w := range workers {
            label := fmt.Sprintf("%s [%s]", b.userLabel(w), nullStr(w.Team))
            rows = append(rows,
                tgbotapi.NewInlineKeyboardRow(
                    tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("toggle_user:%d", w.TgID)),
                ),
            )
        }
        rows = append(rows,
            tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "assignees_menu")),
            tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Далее ▶", "assignees_next")),
        )
        kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
        edit := tgbotapi.NewEditMessageTextAndMarkup(cq.Message.Chat.ID, cq.Message.MessageID,
            "Отметьте сотрудников (повторное нажатие снимает выбор):", kb)
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
   if strings.HasPrefix(data, "choose_dept:") {
    depID, _ := strconv.ParseInt(strings.TrimPrefix(data, "choose_dept:"), 10, 64)

    var p map[string]string
    b.DB.LoadState(ctx, from.ID, &p)

    name := strings.TrimSpace(p["name"])
    if name == "" {
        name = strings.TrimSpace(strings.TrimSpace(from.FirstName + " " + from.LastName))
        if name == "" && from.UserName != "" { name = "@" + from.UserName }
        if name == "" { name = fmt.Sprintf("user-%d", from.ID) }
    }

    dep, _ := b.DB.GetDepartmentByID(ctx, depID)
    _ = b.DB.SetWorkerProfile(ctx, from.ID, name, dep.Name)

    b.DB.ClearState(ctx, from.ID)
    b.API.Send(tgbotapi.NewMessage(cq.Message.Chat.ID,
        fmt.Sprintf("Готово! Вы зарегистрированы как сотрудник: %s (%s).", name, dep.Name)))
    b.API.Request(tgbotapi.NewCallback(cq.ID, "Отдел выбран"))
    return
}
    if strings.HasPrefix(data, "assign_team:") {
	team := strings.TrimPrefix(data, "assign_team:")
	workers, _ := b.DB.ListWorkersByTeam(ctx, team)
	var tgIDs []int64
	for _, w := range workers { tgIDs = append(tgIDs, w.TgID) }

	d := &NewTaskDraft{}; b.DB.LoadState(ctx, from.ID, d)
	d.AssigneeIDs = uniqAppend(d.AssigneeIDs, tgIDs...)
	b.DB.SaveState(ctx, from.ID, StateNewTaskDeadline, d)

	msg := tgbotapi.NewMessage(cq.Message.Chat.ID,
		fmt.Sprintf("Назначено отделу «%s» (%d сотрудн.). Введите дедлайн в формате DD.MM.YYYY HH:MM.", team, len(tgIDs)))
	b.API.Send(msg)
	b.API.Request(tgbotapi.NewCallback(cq.ID, "Назначено отделу"))
	return
}

}

func (b *Bot) onTaskAction(userTgID int64, cq *tgbotapi.CallbackQuery, action string, taskID int64) {
    ctx := context.Background()
    u, err := b.DB.GetUserByTgID(ctx, userTgID) 
    if err != nil { b.API.Request(tgbotapi.NewCallback(cq.ID, "Профиль не найден")); return }

    switch action {
    case "accept":
        _,_ = b.DB.UpdateAssigneeStatus(ctx, taskID, u.ID, "in_progress")
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Статус: В работе"))
    case "done":
        has, _ := b.DB.HasResult(ctx, taskID, u.ID)
        if !has {
            b.API.Request(tgbotapi.NewCallback(cq.ID, "Сначала отправьте результат"))
            return
        }
        changed, _ := b.DB.UpdateAssigneeStatus(ctx, taskID, u.ID, "done")
        if !changed {
            b.API.Request(tgbotapi.NewCallback(cq.ID, "Уже отмечено"))
            return
        }
        b.API.Request(tgbotapi.NewCallback(cq.ID, "Отмечено как выполнено"))
        t, _ := b.DB.GetTask(ctx, taskID)
        creator, _ := b.DB.GetUserByID(ctx, t.CreatorID)

        name := nullStr(u.Name)
        tag := nullStr(u.Username)
        if tag != "" { tag = "(@" + tag + ")" }
        msg := fmt.Sprintf("✔️ Исполнитель %s %s завершил задачу «%s»",
            strings.TrimSpace(name), strings.TrimSpace(tag), nullStr(t.Title))
        b.API.Send(tgbotapi.NewMessage(creator.TgID, msg))
        b.API.Send(tgbotapi.NewMessage(cq.Message.Chat.ID, "Готово!"))
        b.showMenu(cq.Message.Chat.ID, false)
    case "fail":
        _,_ = b.DB.UpdateAssigneeStatus(ctx, taskID, u.ID, "failed")
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
        out.WriteString(fmt.Sprintf("• «%s»\n", nullStr(t.Title))) // было: • #%d %s
        if t.DueAt.Valid { out.WriteString("  Дедлайн: "+t.DueAt.Time.Format("02.01.2006 15:04")+"\n") }
        ass, _ := b.DB.ListAssigneesWithUsersAny(ctx, t.ID)
        for _, a := range ass {
            if !a.UserID.Valid {
                out.WriteString(fmt.Sprintf("  - [без исполнителя]: %s\n", mapStatus(a.Status)))
                continue
            }
            out.WriteString(fmt.Sprintf("  - %s @%s [%s]: %s\n",
                ifEmpty(a.Name.String, "—"),
                a.Username.String,
                a.Team.String,
                mapStatus(a.Status)))
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
    b.reply(m.Chat.ID, "Сотрудник удалён. Его напоминания удалены, задачи остались без исполнителя.")
}

func (b *Bot) formatTasks(ts []*sqlite.Task, withAssignees bool) string {
    var bld strings.Builder
    for _, t := range ts {
        bld.WriteString(fmt.Sprintf("• %s\n", nullStr(t.Title)))
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
    now := time.Now().In(b.TZ).Add(5 * time.Second)

    var beforeTimes []time.Time
    for _, h := range d.RemindHours {
        t := due.Time.Add(-time.Duration(h) * time.Hour)
        if t.After(now) { beforeTimes = append(beforeTimes, t) } 
    }
    if len(beforeTimes) > 0 { _ = b.DB.CreateReminders(ctx, id, uids, beforeTimes, "before") }

    if due.Time.After(now) {
        _ = b.DB.CreateReminders(ctx, id, uids, []time.Time{due.Time}, "deadline")
    }
    ov := due.Time.Add(15 * time.Minute)
    if ov.After(now) {
        _ = b.DB.CreateReminders(ctx, id, uids, []time.Time{ov}, "overdue")
    }
}


    for _, tg := range d.AssigneeIDs { b.sendTaskToAssignee(tg, id, task) }
    b.reply(chatID, fmt.Sprintf("Задача «%s» создана и отправлена %d исполнителям.",
        nullStr(task.Title), len(d.AssigneeIDs)))

}

func (b *Bot) sendTaskToAssignee(tgID int64, taskID int64, t *sqlite.Task) {
    var text strings.Builder
    fmt.Fprintf(&text, "Задача «%s»\n", nullStr(t.Title))
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

func (b *Bot) userLabel(u *sqlite.User) string {
    if n := nullStr(u.Name); n != "" { return n }
    if un := nullStr(u.Username); un != "" { return "@" + un }
    return fmt.Sprintf("%d", u.TgID)
}

func (b *Bot) cmdDone(m *tgbotapi.Message) {
	ctx := context.Background()
	ts, comps, err := b.DB.ListDoneTasksForBoss(ctx, 30)
	if err != nil || len(ts) == 0 { b.reply(m.Chat.ID, "Выполненных задач пока нет."); return }
	var sb strings.Builder
	sb.WriteString("Выполненные задачи:\n")
	for i, t := range ts {
		sb.WriteString(fmt.Sprintf("• «%s» (готово: %s)\n",
			nullStr(t.Title), comps[i].In(b.TZ).Format("02.01 15:04")))
		execs, _ := b.DB.ListDoneExecutorsForTask(ctx, t.ID)
		if len(execs) > 0 {
			sb.WriteString("  Выполнили: ")
			var names []string
			for _, e := range execs {
				n := strings.TrimSpace(ifEmpty(e.Name.String, "@"+e.Username.String))
				if n == "" { n = "[без имени]" }
				names = append(names, n)
			}
			sb.WriteString(strings.Join(names, ", ") + "\n")
		}
	}
	b.reply(m.Chat.ID, sb.String())
}

func (b *Bot) cmdMyDone(m *tgbotapi.Message) {
    ctx := context.Background()
    u, _ := b.DB.GetUserByTgID(ctx, m.From.ID)
    ts, comps, err := b.DB.ListDoneTasksForUser(ctx, u.ID, 30)
    if err != nil || len(ts) == 0 { b.reply(m.Chat.ID, "У вас пока нет выполненных задач."); return }
    var sb strings.Builder
    sb.WriteString("Ваши выполненные задачи:\n")
    for i, t := range ts {
        sb.WriteString(fmt.Sprintf("• «%s» (готово: %s)\n",
            nullStr(t.Title), comps[i].In(b.TZ).Format("02.01 15:04")))
    }
    b.reply(m.Chat.ID, sb.String())
}


func (b *Bot) startOrphansDailyPing(hour int) {
    go func() {
        for {
            now := time.Now().In(b.TZ)
            next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, b.TZ)
            if !now.Before(next) { next = next.Add(24 * time.Hour) }
            time.Sleep(next.Sub(now))
            b.pingOrphans()
        }
    }()
}

func (b *Bot) pingOrphans() {
    ctx := context.Background()
    ts, err := b.DB.ListTasksWithoutAssignees(ctx)
    if err != nil || len(ts) == 0 { return }

    var sb strings.Builder
    sb.WriteString("⚠️ Есть задачи без исполнителей. Проверьте назначения:\n")
    for _, t := range ts {
        sb.WriteString("• «" + nullStr(t.Title) + "»\n")
    }
    for bossID := range b.BossIDs {
        msg := tgbotapi.NewMessage(bossID, sb.String())
        b.API.Send(msg)
    }
}

func (b *Bot) cmdTaskDel(m *tgbotapi.Message) {
	ctx := context.Background()
	args := strings.TrimSpace(m.CommandArguments())
	if args == "" { b.reply(m.Chat.ID, "Использование: /task_del <id>"); return }
	taskID, err := strconv.ParseInt(args, 10, 64)
	if err != nil { b.reply(m.Chat.ID, "id должен быть числом"); return }

	t, err := b.DB.GetTask(ctx, taskID)
	if err != nil { b.reply(m.Chat.ID, "Задача не найдена."); return }
	tgIDs, _ := b.DB.ListAssigneeTgIDsByTask(ctx, taskID)

	aff, err := b.DB.DeleteTask(ctx, taskID)
	if err != nil || aff == 0 { b.reply(m.Chat.ID, "Не удалось удалить."); return }

	title := nullStr(t.Title)
	for _, tg := range tgIDs {
		b.API.Send(tgbotapi.NewMessage(tg, "❌ Задача «"+title+"» удалена боссом."))
	}
	b.reply(m.Chat.ID, "Удалено.")
}

func (b *Bot) cmdTaskDelAll(m *tgbotapi.Message) {
	ctx := context.Background()

	tasks, _ := b.DB.ListAllTasks(ctx)
	for _, t := range tasks {
		title := nullStr(t.Title)
		tgIDs, _ := b.DB.ListAssigneeTgIDsByTask(ctx, t.ID)
		for _, tg := range tgIDs {
			b.API.Send(tgbotapi.NewMessage(tg, "❌ Задача «"+title+"» удалена боссом."))
		}
	}
	aff, err := b.DB.DeleteAllTasks(ctx)
	if err != nil {
		b.reply(m.Chat.ID, "Ошибка: "+err.Error())
		return
	}
	b.reply(m.Chat.ID, fmt.Sprintf("Удалено задач: %d", aff))
}

func uniqAppend(dst []int64, more ...int64) []int64 {
	set := make(map[int64]struct{}, len(dst)+len(more))
	for _, v := range dst { set[v] = struct{}{} }
	for _, v := range more {
		if _, ok := set[v]; !ok { dst = append(dst, v); set[v] = struct{}{} }
	}
	return dst
}

