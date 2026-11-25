package transfers

import "testing"

// A deleted account's transfers go (docs/specs/account-enable-and-delete.md);
// everyone else's stay.
func TestRemovingADeletedAccountForgetsItsTransfers(t *testing.T) {
	f := newFixture(t)
	f.beginUpload(t, f.alice, "alice.bin", 4)
	f.beginUpload(t, f.bob, "bob.bin", 4)
	if err := f.service.RemoveAccount(f.ctx, f.alice.ID); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	owners := map[string]int{}
	rows, err := f.service.store.db.QueryContext(f.ctx, "SELECT owner_id FROM transfers")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			t.Fatal(err)
		}
		owners[owner]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if owners[f.alice.ID] != 0 || owners[f.bob.ID] != 1 {
		t.Fatalf("transfers by owner = %v, want only bob's", owners)
	}
	if err := f.service.RemoveAccount(f.ctx, f.alice.ID); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
}
