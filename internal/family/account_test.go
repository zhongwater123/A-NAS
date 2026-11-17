package family

import (
	"context"
	"testing"
)

func TestRemovingADeletedAccountKeepsWhatItWrote(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	record := must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindJournal, Text: "今天会笑了", OccurredOn: "2026-10-28"}))
	// The account is deleted: it leaves the directory, then the family.
	delete(f.accounts, mom.AccountID)
	if err := f.store.RemoveAccount(ctx, mom.AccountID); err != nil {
		t.Fatal(err)
	}
	overview := must(f.store.Overview(ctx, dad))
	if !overview.Me.Guardian {
		t.Fatalf("the family has no guardian after its only one was deleted: %+v", overview.Me)
	}
	for _, member := range overview.Members {
		if member.AccountID == mom.AccountID {
			t.Fatalf("the deleted account is still a member: %+v", overview.Members)
		}
	}
	if got := must(f.store.Record(ctx, dad, record.ID)); got.AuthorName != "mom" {
		t.Fatalf("the deleted account's record = %+v", got)
	}
	// A new account given the name joins as anyone new does.
	f.accounts["u-mom-2"] = Account{ID: "u-mom-2", Username: "mom"}
	again := Actor{AccountID: "u-mom-2", Username: "mom"}
	if me := must(f.store.Join(ctx, again, "妈妈")); me.Status != StatusPending {
		t.Fatalf("the new account = %+v, want a pending request", me)
	}
	if err := f.store.RemoveAccount(ctx, mom.AccountID); err != nil {
		t.Fatalf("removing again: %v", err)
	}
}
