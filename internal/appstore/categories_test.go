package appstore

import "testing"

// A category assignment for an app that left the catalog is a review leftover.
func TestCategoriesOnlyNameCatalogApps(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, entry := range entries {
		listed[entry.App.ID] = true
	}
	for id := range appCategories {
		if !listed[id] {
			t.Errorf("%s has a category but is not in the catalog", id)
		}
	}
}
