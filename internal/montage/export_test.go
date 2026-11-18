package montage

import (
	"context"
	"testing"
	"time"
)

// RunOnce runs the job due first, if one is, as RunJobs would.
func (s *Store) RunOnce(ctx context.Context) bool {
	if err := s.SweepRounds(ctx); err != nil {
		return false
	}
	job, found, err := s.claim(ctx)
	if err != nil || !found {
		return false
	}
	s.run(ctx, job)
	return true
}

// SetHeartbeat makes running jobs renew their lease, and look at their
// videos, more often.
func (s *Store) SetHeartbeat(every time.Duration) { s.beat = every }

// Claim marks the job due first running, as a run cut short by a crash
// leaves it.
func (s *Store) Claim(ctx context.Context) string {
	job, _, _ := s.claim(ctx)
	return job.id
}

// SetRoom makes the store see room, or none, for what it writes.
func (s *Store) SetRoom(room bool) {
	s.admits = func(string, int64) (bool, error) { return room, nil }
}

// UseMusic makes jobs take their songs from music rather than the store.
func (s *Store) UseMusic(music Music) { s.jobMusic = music }

// NextDue is how long RunJobs would sleep with no job to claim.
func (s *Store) NextDue(ctx context.Context) time.Duration { return s.nextDue(ctx) }

// Exec changes the database as no request does, for the states only a crash
// or an earlier release leaves behind.
func (s *Store) Exec(t testing.TB, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
