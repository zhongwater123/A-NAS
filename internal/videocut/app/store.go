// Package app keeps VideoCut's records (剪辑中心, ADR 0038):
// projects, plans, timelines and jobs, in videocut.db on the VideoCut store.
// The videos, their analysis and the films stay in the photo library.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhongwater123/A-NAS/internal/capacity"
	"github.com/zhongwater123/A-NAS/internal/keylock"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
)

// Store is VideoCut's database. Every record names the member it
// belongs to, and every read is for one member.
type Store struct {
	db      *db.DB
	root    string
	grants  Grants
	media   Media
	music   MusicAnalyzer
	compute MusicComputer
	now     func() time.Time
	logger  *slog.Logger
	// songs serialises the song copies: a copy arriving and the copies
	// no row names any more being removed.
	songs sync.Mutex
	// wake tells AnalyzeSongs a song arrived.
	wake chan struct{}
	// What jobs work with: the compute Worker's VideoCut operations, the
	// FFmpeg worker's editing, the planner, and the songs (the store's
	// own, Chosen, Excerpt and Audio, unless a test sets another).
	jobCompute Compute
	editing    Editing
	planner    Planner
	jobMusic   Music
	// search finds windows by text for editing (#596); without it searching
	// and shuffling are not available.
	search WindowSearch
	// thumbnails holds a place for each thumbnail being taken.
	thumbnails chan struct{}
	// jobWake tells RunJobs a job is due; running holds the running job's
	// cancel.
	jobWake chan struct{}
	runMu   sync.Mutex
	running map[string]context.CancelCauseFunc
	// beat is how often a running job's lease is renewed (heartbeat).
	beat time.Duration
	// admits is capacity.Admits, which tests replace.
	admits func(path string, incoming int64) (bool, error)
	// markers are the planning markers, one per member with a job wanting
	// a round; nil leaves none.
	markers Markers
	// opened is when the store was opened: rounds that stopped short of
	// planning before it do not hold a job back (retryWait).
	opened time.Time
	// plans is how many films, 方案, a job started now makes to choose
	// from; a job keeps the number it started with (jobs.plans).
	plans int
	// members holds a member's lock while a request or reconciling changes
	// that member's projects and grants.
	members keylock.Map[string]
	// reconciling runs one round of Reconcile at a time. reconciled is set
	// once a round went through; seen are the unfinished batches the last
	// round found, which the next one settles.
	reconciling sync.Mutex
	reconciled  atomic.Bool
	seen        map[string]bool

	// Revocations a request asked for run in the background until Close.
	mu         sync.Mutex
	closed     bool
	base       context.Context
	stop       context.CancelFunc
	background sync.WaitGroup
}

// DefaultPlans is how many 方案 a job makes unless Options says otherwise:
// two films to choose from (docs/specs/montage-center.md, 两个方案). MaxPlans
// is the most a store can be asked for: the contract names the steps of two
// (api/components/videocut.yaml, VideoCutJobStep).
const (
	DefaultPlans = 2
	MaxPlans     = 2
)

// Options are what a store works with.
type Options struct {
	// Grants reaches the photo library's project grants.
	Grants Grants
	// Media decodes songs (the FFmpeg worker); Music finds their structure
	// (the music analysis Worker); Compute picks their excerpts (the
	// compute Worker). Without them songs wait.
	Media   Media
	Music   MusicAnalyzer
	Compute MusicComputer
	// JobCompute, Editing and Planner are what jobs work with besides the
	// songs: the compute Worker's VideoCut operations, the FFmpeg worker's
	// editing and the planner. A store without them keeps projects and
	// songs but makes no film.
	JobCompute Compute
	Editing    Editing
	Planner    Planner
	// WindowSearch finds a grant's windows by text, for editing a film
	// (#596); without it editing cannot search. videocut.go gives the
	// grants' own search (WindowSearchOf).
	WindowSearch WindowSearch
	// Markers are the planning markers the waker reads
	// (external.PlanMarkers); without them no member's VNAS is asked to
	// plan.
	Markers Markers
	// Plans is how many films, 方案, each job started makes: 0 is
	// DefaultPlans, more than MaxPlans is refused.
	Plans  int
	Now    func() time.Time
	Logger *slog.Logger
}

// Open opens, creating when missing, the store's database in root. Commits
// wait until they are on disk: a job that was told it advanced must still
// have advanced after a power cut.
func Open(ctx context.Context, root string, options Options) (*Store, error) {
	if options.Grants == nil {
		return nil, errors.New("the VideoCut store needs the photo library's grants")
	}
	if options.Plans < 0 || options.Plans > MaxPlans {
		return nil, fmt.Errorf("the VideoCut store makes at most %d 方案 a job, not %d", MaxPlans, options.Plans)
	}
	database, err := db.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	store := &Store{db: database, root: root, grants: options.Grants, media: options.Media, music: options.Music, compute: options.Compute,
		now: options.Now, logger: options.Logger, wake: make(chan struct{}, 1),
		jobCompute: options.JobCompute, editing: options.Editing, planner: options.Planner, search: options.WindowSearch, thumbnails: make(chan struct{}, MaxThumbnails),
		jobWake: make(chan struct{}, 1), running: map[string]context.CancelCauseFunc{}, beat: heartbeat, admits: capacity.Admits,
		markers: options.Markers, plans: options.Plans}
	if store.plans == 0 {
		store.plans = DefaultPlans
	}
	store.jobMusic = store
	// Without a stand-in, the member's VNAS plans, where markers reach it.
	if store.planner == nil && store.markers != nil {
		store.planner = store.VNASPlanner()
	}
	if store.now == nil {
		store.now = time.Now
	}
	store.opened = store.now()
	if store.logger == nil {
		store.logger = slog.Default()
	}
	store.base, store.stop = context.WithCancel(context.Background())
	return store, nil
}

// StartWorkers recovers durable work before accepting requests. Close cancels
// and joins the workers before closing their database.
func (s *Store) StartWorkers(ctx context.Context) error {
	if err := s.RecoverJobs(ctx); err != nil {
		return err
	}
	if err := s.ReclaimSongs(ctx); err != nil {
		return err
	}
	s.soon(s.AnalyzeSongs)
	s.soon(s.RunJobs)
	return nil
}

// Close stops the revocations, the song analysis and the job still running,
// which stay queued, and closes the database.
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	s.background.Wait()
	return s.db.Close()
}

// soon runs work in the background unless the store is closing.
func (s *Store) soon(work func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.background.Go(func() { work(s.base) })
}
