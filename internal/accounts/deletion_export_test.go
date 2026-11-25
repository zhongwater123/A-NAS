package accounts

import (
	"context"
	"log/slog"
	"time"
)

// RunDueDeletions runs one round of RunDeletions and says how long the loop
// would wait before the next.
func (s *Service) RunDueDeletions(ctx context.Context, steps []DeletionStep) time.Duration {
	return s.runDueDeletions(ctx, steps, slog.Default())
}
