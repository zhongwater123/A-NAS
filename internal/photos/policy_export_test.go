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

// RecheckHits returns which of assetIDs the page of ranked hits still shows p,
// as if an index had found them all.
func RecheckHits(ctx context.Context, s *Service, p Principal, assetIDs ...string) ([]string, error) {
	hits := make([]searchHit, len(assetIDs))
	for i, id := range assetIDs {
		hits[i] = searchHit{id: id, score: float64(len(assetIDs) - i)}
	}
	assets, _, err := s.hitPage(ctx, p, hits, nil, len(hits))
	if err != nil {
		return nil, err
	}
	shown := make([]string, len(assets))
	for i, asset := range assets {
		shown[i] = asset.ID
	}
	return shown, nil
}

// SearchCandidates lists by ID what searches, similar photos and image
// searches over the libraries of scope consider for p.
func SearchCandidates(ctx context.Context, s *Service, p Principal, scope ...string) ([]string, error) {
	args := make([]any, len(scope))
	for i, id := range scope {
		args[i] = id
	}
	candidates, err := s.searchCandidates(ctx, p, args)
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}
	return ids, err
}
