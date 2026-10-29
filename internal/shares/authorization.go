package shares

import "context"

type activeLookup func(context.Context, string) (bool, error)

// activeAccounts is local to one synchronous operation, never a stored task
// context. A later cycle or publication checks the account's current status.
func (s *Store) activeAccounts(ctx context.Context) (activeLookup, error) {
	if s.options.ActiveAccounts != nil {
		accounts, err := s.options.ActiveAccounts(ctx)
		if err != nil {
			return nil, err
		}
		return func(_ context.Context, id string) (bool, error) { return accounts[id], nil }, nil
	}
	// Local adapters may only have a per-user lookup. Deduplicate it within
	// the operation; production supplies the batch source above.
	known := map[string]bool{}
	return func(ctx context.Context, id string) (bool, error) {
		if active, ok := known[id]; ok {
			return active, nil
		}
		active, err := s.options.Active(ctx, id)
		if err == nil {
			known[id] = active
		}
		return active, err
	}, nil
}
