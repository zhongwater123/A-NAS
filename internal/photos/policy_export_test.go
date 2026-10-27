package photos

import "context"

// AssetRuleForms lists by ID the untrashed assets of every library that the
// asset rules let p see, once by their SQL form and once by their form on a
// record, for a test to compare.
func AssetRuleForms(ctx context.Context, s *Service, p Principal) (listed, opened []string, err error) {
	if listed, err = s.strings(ctx, "SELECT a.id FROM assets a WHERE "+listedAssets(p, "a")+" ORDER BY a.id"); err != nil {
		return nil, nil, err
	}
	records, err := s.queryAssets(ctx, s.db, "WHERE a.trashed_at IS NULL ORDER BY a.id")
	if err != nil {
		return nil, nil, err
	}
	for _, record := range records {
		if seesAsset(p, record) {
			opened = append(opened, record.ID)
		}
	}
	return listed, opened, nil
}
