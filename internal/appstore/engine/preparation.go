package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

const preparationTimeout = time.Minute

type installPreparation struct {
	id       string
	identity appstore.Identity
	ready    chan error
	deadline time.Time
}

// ReserveInstall validates the plan and conflicts and records a running job,
// but does not call Compose until the host preparation has been committed.
// Lost clients time out; Stop and restart use the existing job recovery path.
func (s *Store) ReserveInstall(ctx context.Context, id, digest string, identity appstore.Identity) (appstore.InstallReservation, error) {
	token := rand.Text()
	pending := &installPreparation{id: id, identity: identity, ready: make(chan error, 1), deadline: time.Now().Add(preparationTimeout)}
	s.mu.Lock()
	s.preparations[token] = pending
	s.mu.Unlock()
	forget := func() {
		s.mu.Lock()
		delete(s.preparations, token)
		s.mu.Unlock()
	}
	job, err := s.install(ctx, id, digest, identity, func(ctx context.Context) error {
		defer forget()
		timed, cancel := context.WithDeadline(ctx, pending.deadline)
		defer cancel()
		select {
		case err := <-pending.ready:
			return err
		case <-timed.Done():
			// Commit and expiry must choose one outcome under the same lock.
			// A committed result wins even when select also observes timeout.
			s.mu.Lock()
			defer s.mu.Unlock()
			select {
			case err := <-pending.ready:
				return err
			default:
				delete(s.preparations, token)
			}
			return fmt.Errorf("app host preparation did not complete: %w", context.Cause(timed))
		}
	})
	if err != nil {
		forget()
		return appstore.InstallReservation{}, err
	}
	return appstore.InstallReservation{Token: token, Job: job}, nil
}

// CommitInstall is called only after the agent has looked up the actual Linux
// account. Its identity must be the one shown in the reserved plan.
func (s *Store) CommitInstall(_ context.Context, id, token string, identity appstore.Identity) (appstore.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.preparations[token]
	if !ok || pending.id != id || pending.identity != identity || s.active != id || s.stopped {
		return appstore.Job{}, appstore.ErrPlanChanged
	}
	if !time.Now().Before(pending.deadline) {
		return appstore.Job{}, fmt.Errorf("%w: app host preparation timed out", appstore.ErrUnavailable)
	}
	delete(s.preparations, token)
	pending.ready <- nil
	return copyJob(s.jobs[id]), nil
}

func (s *Store) CancelInstall(_ context.Context, id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.preparations[token]
	if !ok || pending.id != id {
		return appstore.ErrPlanChanged
	}
	delete(s.preparations, token)
	pending.ready <- errors.New("app host preparation was cancelled")
	return nil
}
