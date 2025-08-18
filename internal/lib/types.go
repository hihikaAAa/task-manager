package lib

const (
    StateIdle             = "idle"
    StateRegName          = "reg_name"
    StateRegTeam          = "reg_team"
    StateNewTaskWaitBody  = "newtask_body"
    StateNewTaskAssignees = "newtask_assignees"
    StateNewTaskDeadline  = "newtask_deadline"
    StateNewTaskReminders = "newtask_reminders"
    StateAwaitResult      = "await_result"
)

type NewTaskDraft struct {
    Title       string   `json:"title"`
    Description string   `json:"description"`
    VoiceFileID string   `json:"voice_file_id"`
    AssigneeIDs []int64  `json:"assignee_ids"`
    DueAt       string   `json:"due_at"`
    RemindHours []int    `json:"remind_hours"`
    TaskID      int64    `json:"task_id"`
}
