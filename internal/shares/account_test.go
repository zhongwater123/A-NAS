package shares

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovingASenderDeletesItsSharesAndTheirContent(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	ctx := context.Background()
	v := publish(t, s)
	content := filepath.Join(s.dir, v.ID)
	if _, err := os.Stat(content); err != nil {
		t.Fatalf("published content: %v", err)
	}
	if err := s.RemoveAccount(ctx, sender.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, bob, v.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted sender's share is still there: %v", err)
	}
	if _, err := os.Stat(content); !os.IsNotExist(err) {
		t.Fatalf("its content is still there: %v", err)
	}
	var documents int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM share_documents").Scan(&documents); err != nil || documents != 0 {
		t.Fatalf("stored shares = %d, %v", documents, err)
	}
	if err := s.RemoveAccount(ctx, sender.ID); err != nil {
		t.Fatalf("removing again: %v", err)
	}
}

func TestRemovingARecipientLeavesTheShareToTheOthers(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	ctx := context.Background()
	v := publish(t, s)
	if _, err := s.SendMessage(ctx, bob, v.ID, bob.ID, "bob-1", "收到"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAccount(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	kept, err := s.Get(ctx, sender, v.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Recipients) != 1 || kept.Recipients[0].UserID != carol.ID {
		t.Fatalf("recipients = %+v, want only carol", kept.Recipients)
	}
	if _, err := s.Get(ctx, carol, v.ID, false); err != nil {
		t.Fatalf("carol lost the share: %v", err)
	}
	if _, err := s.Messages(ctx, sender, v.ID, bob.ID, 0, 10); err == nil {
		t.Fatal("the deleted recipient's conversation is still there")
	}
}
