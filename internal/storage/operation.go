package storage

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

const (
	// operationCallTimeout bounds one call to the Host Agent about an
	// operation; the operation itself takes as long as the disks need.
	operationCallTimeout = 30 * time.Second
	maxPollInterval      = 30 * time.Second
)

// track records what the host reports of the plan's operation: a finished
// one ends the plan, and a running one is followed until it ends.
func (s *Service) track(planID string, operation VolumeOperation) (ExecutionPlan, error) {
	switch operation.State {
	case OperationSucceeded:
		return s.succeeded(planID, operation)
	case OperationFailed, OperationInterrupted:
		failure := operation.Failure
		if failure == "" {
			failure = "the operation stopped"
		}
		return s.stopped(planID, operation.Step, errors.New(failure))
	}
	s.mu.Lock()
	plan := s.plans[planID]
	// The step is only shown while the plan runs; saving it would write an
	// activity record for every step.
	if plan.State == PlanStateRunning {
		plan.Step = operation.Step
		s.plans[planID] = plan
	}
	s.mu.Unlock()
	s.follow(planID)
	return clonePlan(plan), nil
}

func (s *Service) succeeded(planID string, operation VolumeOperation) (ExecutionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.plans[planID]
	if plan.State != PlanStateRunning {
		return clonePlan(plan), nil
	}
	var volume Volume
	if operation.Volume != nil {
		volume = *operation.Volume
	}
	volume.Layout, volume.Conversion = plan.Layout, nil
	if plan.Kind == PlanKindCreateVolume {
		volume.DiskIDs = diskIDsOf(plan.Disks)
		if volume.ID == "" {
			volume.ID = "volume:data"
		}
	} else {
		current, _ := s.currentVolumeLocked()
		volume.ID, volume.FilesystemUUID = current.ID, current.FilesystemUUID
		volume.DiskIDs = append([]string(nil), current.DiskIDs...)
		for _, id := range diskIDsOf(plan.Disks) {
			if !slices.Contains(volume.DiskIDs, id) {
				volume.DiskIDs = append(volume.DiskIDs, id)
			}
		}
	}
	plan.State, plan.Step, plan.Failure, plan.Volume = PlanStateSucceeded, "", "", &volume
	s.plans[planID] = plan
	if err := s.savePlanLocked(context.Background(), plan); err != nil {
		return ExecutionPlan{}, err
	}
	return clonePlan(plan), nil
}

// follow polls the Host Agent for the plan's operation in the background
// until it ends, also across restarts of the product service: a plan
// stored as running is followed again at startup.
func (s *Service) follow(planID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.following[planID] || s.ctx.Err() != nil {
		return
	}
	s.following[planID] = true
	s.followers.Add(1)
	go s.followOperation(planID)
}

func (s *Service) followOperation(planID string) {
	defer s.followers.Done()
	wait := s.pollInterval
	for {
		timer := time.NewTimer(wait)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(s.ctx, operationCallTimeout)
		operation, err := s.executor.VolumeOperation(ctx, planID)
		cancel()
		switch {
		case errors.Is(err, ErrOperationNotFound):
			_, _ = s.needsAttention(planID, errors.New("the Host Agent never received the operation and changed nothing; retry the plan"))
		case err != nil:
			// The Host Agent may be restarting; the plan stays running
			// until it answers.
			wait = min(2*wait, maxPollInterval)
			continue
		default:
			wait = s.pollInterval
			_, _ = s.track(planID, operation)
		}
		if s.doneFollowing(planID) {
			return
		}
	}
}

// doneFollowing ends following a plan that no longer runs. A plan retried
// just after it stopped runs again, and is followed on by the same loop.
func (s *Service) doneFollowing(planID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plans[planID].State == PlanStateRunning {
		return false
	}
	delete(s.following, planID)
	return true
}

// UnfinishedPlans are the plans that still need the administrator: the one
// running, and one that stopped and has not been retried or replaced by a
// plan that ran since. The settings page shows them again after a reload.
func (s *Service) UnfinishedPlans() []ExecutionPlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	plans := []ExecutionPlan{}
	for _, plan := range s.plans {
		if plan.State == PlanStateRunning || (plan.State == PlanStateNeedsAttention && !s.supersededLocked(plan)) {
			plans = append(plans, clonePlan(plan))
		}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].CreatedAt.After(plans[j].CreatedAt) })
	return plans
}

// supersededLocked reports whether a plan made after this one has run: once
// the administrator moved on, a plan that stopped is history.
func (s *Service) supersededLocked(plan ExecutionPlan) bool {
	for _, other := range s.plans {
		if !other.CreatedAt.After(plan.CreatedAt) {
			continue
		}
		switch other.State {
		case PlanStateRunning, PlanStateSucceeded, PlanStateNeedsAttention, PlanStateFailed:
			return true
		}
	}
	return false
}

// Close stops following operations; the Host Agent carries on with them,
// and the next start follows them again.
func (s *Service) Close() {
	s.stop()
	s.followers.Wait()
}
