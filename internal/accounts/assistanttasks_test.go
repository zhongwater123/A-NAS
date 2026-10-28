package accounts_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestAssistantTaskNoticesWaitOnePerTaskAndOnlyForNewRuns(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &viewingRecorder{}, accounts.Options{PasswordCost: cheapPasswords})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := service.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatal(err)
	}
	at := func(minute int) time.Time { return time.Date(2026, 10, 20, 8, minute, 0, 0, time.UTC) }
	waiting := func() []accounts.Notification {
		t.Helper()
		notifications, err := service.Notifications(ctx, alice)
		if err != nil {
			t.Fatal(err)
		}
		return notifications
	}
	record := func(current []string, runs ...accounts.AssistantTaskRun) {
		t.Helper()
		if err := service.RecordAssistantTasks(ctx, alice.ID, runs, current); err != nil {
			t.Fatal(err)
		}
	}
	both := []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb"}

	record(both,
		accounts.AssistantTaskRun{TaskID: "aaaaaaaaaaaa", Name: "每日小结", Status: "ok", RanAt: at(0), SessionID: "cron_aaaaaaaaaaaa_20261020_080000", Summary: "共享空间新增 3 个文件"},
		accounts.AssistantTaskRun{TaskID: "bbbbbbbbbbbb", Name: "看看有没有新照片", Status: "ok", RanAt: at(0), Quiet: true})
	notifications := waiting()
	if len(notifications) != 1 || notifications[0].Kind != accounts.NotificationAssistantTask || notifications[0].Task == nil ||
		notifications[0].Task.Name != "每日小结" || notifications[0].Task.Summary != "共享空间新增 3 个文件" {
		t.Fatalf("notifications = %#v, want the one run with something to report", notifications)
	}
	heard, err := service.AssistantTasksHeard(ctx, alice.ID)
	if err != nil || !heard["aaaaaaaaaaaa"].Equal(at(0)) || !heard["bbbbbbbbbbbb"].Equal(at(0)) {
		t.Fatalf("heard = %v, %v; a quiet run is heard too", heard, err)
	}

	// The same run again, or an older one, is not told twice.
	record(both, accounts.AssistantTaskRun{TaskID: "aaaaaaaaaaaa", Name: "每日小结", Status: "ok", RanAt: at(0)})
	if got := waiting(); len(got) != 1 || got[0].ID != notifications[0].ID {
		t.Fatalf("after the same run notifications = %#v", got)
	}
	// A quiet run leaves the unread notice of its task in place.
	record(both, accounts.AssistantTaskRun{TaskID: "aaaaaaaaaaaa", Name: "每日小结", Status: "ok", RanAt: at(1), Quiet: true})
	if got := waiting(); len(got) != 1 || got[0].ID != notifications[0].ID {
		t.Fatalf("after a quiet run notifications = %#v", got)
	}
	// A later run replaces it: one waits per task.
	record(both, accounts.AssistantTaskRun{TaskID: "aaaaaaaaaaaa", Name: "每日小结", Status: "error", RanAt: at(2), Summary: "模型没有响应"})
	got := waiting()
	if len(got) != 1 || got[0].ID == notifications[0].ID || got[0].Task.Status != "error" || !got[0].Task.RanAt.Equal(at(2)) {
		t.Fatalf("after a later run notifications = %#v", got)
	}
	if err := service.AcknowledgeNotification(ctx, alice, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := waiting(); len(got) != 0 {
		t.Fatalf("after reading notifications = %#v", got)
	}
	if heard, _ := service.AssistantTasksHeard(ctx, alice.ID); !heard["aaaaaaaaaaaa"].Equal(at(2)) {
		t.Fatalf("a read notice is still heard: %v", heard)
	}

	// A deleted task's notices go.
	record([]string{"bbbbbbbbbbbb"})
	if heard, _ := service.AssistantTasksHeard(ctx, alice.ID); len(heard) != 1 || heard["aaaaaaaaaaaa"] != (time.Time{}) {
		t.Fatalf("after deleting a task heard = %v", heard)
	}
	// Another member's notices are their own.
	if heard, _ := service.AssistantTasksHeard(ctx, admin.ID); len(heard) != 0 {
		t.Fatalf("the administrator heard %v", heard)
	}
}
