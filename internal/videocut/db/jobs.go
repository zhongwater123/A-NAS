package db

// The reads and writes of jobs (docs/architecture/montage-center.md, 持久任务).

const (
	// FinishedStates are the states a job ends in, as SQL.
	FinishedStates = "'done', 'failed', 'cancelled'"
	// ActiveStateQuery holds for a job not finished.
	ActiveStateQuery = "state NOT IN (" + FinishedStates + ")"
)
