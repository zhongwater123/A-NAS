package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// NotificationAssistantTask tells a member that one of their assistant's
// scheduled tasks ran (ADR 0024 decision 8). The desktop shows it as a toast,
// not with the account notices.
const NotificationAssistantTask NotificationKind = "assistant_task"

// AssistantTaskRun is a finished run of one of a member's scheduled tasks.
type AssistantTaskRun struct {
	TaskID string `json:"taskId"`
	Name   string `json:"name"`
	// Status is Hermes's: ok, or what went wrong (error, ...).
	Status string    `json:"status"`
	RanAt  time.Time `json:"ranAt"`
	// SessionID is the run's conversation, when it has one.
	SessionID string `json:"sessionId,omitempty"`
	// Summary is the start of what the run answered, or of its error.
	Summary string `json:"summary,omitempty"`
	// Quiet is a run that had nothing to report: recorded as heard, not shown.
	Quiet bool `json:"-"`
}

// AssistantTasksHeard returns, by task, when the latest run the member was
// told of ran, whether they read it or not.
func (s *Service) AssistantTasksHeard(ctx context.Context, userID string) (map[string]time.Time, error) {
	notices, err := s.assistantTaskNotices(ctx, s.store.db, userID)
	if err != nil {
		return nil, err
	}
	heard := map[string]time.Time{}
	for _, notice := range notices {
		if latest, ok := heard[notice.run.TaskID]; !ok || notice.run.RanAt.After(latest) {
			heard[notice.run.TaskID] = notice.run.RanAt
		}
	}
	return heard, nil
}

// RecordAssistantTasks tells the member of finished runs of their scheduled
// tasks. A run replaces the earlier notices of its task, so at most one waits
// per task, and is ignored when the member already heard of it or of a later
// run; a quiet run leaves an unread notice in place. Notices of tasks not in
// current, which no longer exist, are dropped.
func (s *Service) RecordAssistantTasks(ctx context.Context, userID string, runs []AssistantTaskRun, current []string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	notices, err := s.assistantTaskNotices(ctx, tx, userID)
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, id := range current {
		exists[id] = true
	}
	drop := func(id string) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE id = ?`, id)
		return err
	}
	latest := map[string]time.Time{}
	held := map[string][]assistantTaskNotice{}
	for _, notice := range notices {
		if !exists[notice.run.TaskID] {
			if err := drop(notice.id); err != nil {
				return err
			}
			continue
		}
		held[notice.run.TaskID] = append(held[notice.run.TaskID], notice)
		if at, ok := latest[notice.run.TaskID]; !ok || notice.run.RanAt.After(at) {
			latest[notice.run.TaskID] = notice.run.RanAt
		}
	}
	now := formatTime(s.now().UTC())
	for _, run := range runs {
		if at, ok := latest[run.TaskID]; ok && !run.RanAt.After(at) {
			continue
		}
		var kept []assistantTaskNotice
		for _, notice := range held[run.TaskID] {
			if run.Quiet && !notice.read {
				kept = append(kept, notice)
				continue
			}
			if err := drop(notice.id); err != nil {
				return err
			}
		}
		detail, _ := json.Marshal(notificationDetail{Task: &run})
		var acknowledged any
		if run.Quiet {
			acknowledged = now
		}
		id := s.randomID("notification")
		if _, err := tx.ExecContext(ctx, `INSERT INTO notifications(id, user_id, kind, actor_username, detail, created_at, acknowledged_at)
VALUES(?,?,?,?,?,?,?)`, id, userID, NotificationAssistantTask, "", string(detail), now, acknowledged); err != nil {
			return err
		}
		held[run.TaskID] = append(kept, assistantTaskNotice{id: id, run: run, read: run.Quiet})
		latest[run.TaskID] = run.RanAt
	}
	return tx.Commit()
}

type sqlQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type assistantTaskNotice struct {
	id   string
	run  AssistantTaskRun
	read bool
}

func (s *Service) assistantTaskNotices(ctx context.Context, db sqlQuerier, userID string) ([]assistantTaskNotice, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, detail, acknowledged_at IS NOT NULL FROM notifications WHERE user_id = ? AND kind = ?`,
		userID, NotificationAssistantTask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notices []assistantTaskNotice
	for rows.Next() {
		var id, detail string
		var read bool
		if err := rows.Scan(&id, &detail, &read); err != nil {
			return nil, err
		}
		var decoded notificationDetail
		if json.Unmarshal([]byte(detail), &decoded) == nil && decoded.Task != nil {
			notices = append(notices, assistantTaskNotice{id: id, run: *decoded.Task, read: read})
		}
	}
	return notices, rows.Err()
}
