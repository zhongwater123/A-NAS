// Package containers defines the container management capability exposed to
// the Web desktop: a snapshot of containers and images, a fixed set of
// lifecycle actions and bounded log reads. Adapters implement Manager; only the
// container agent talks to the Docker Engine socket.
package containers

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var (
	ErrNotFound      = errors.New("container not found")
	ErrInvalidID     = errors.New("invalid container ID")
	ErrInvalidAction = errors.New("invalid container action")
	ErrUnavailable   = errors.New("container engine is unavailable")
)

// MaxLogLines bounds a single log read so a chatty container cannot exhaust memory.
const MaxLogLines = 500

type State string

const (
	StateCreated    State = "created"
	StateRunning    State = "running"
	StatePaused     State = "paused"
	StateRestarting State = "restarting"
	StateRemoving   State = "removing"
	StateExited     State = "exited"
	StateDead       State = "dead"
)

func (s State) Valid() bool {
	switch s {
	case StateCreated, StateRunning, StatePaused, StateRestarting, StateRemoving, StateExited, StateDead:
		return true
	default:
		return false
	}
}

// Action is a lifecycle change the desktop may request. Creating, removing or
// reconfiguring containers is deliberately not an Action.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

func (a Action) Valid() bool {
	return a == ActionStart || a == ActionStop || a == ActionRestart
}

type Port struct {
	HostIP        string
	HostPort      uint16
	ContainerPort uint16
	Protocol      string
}

// Usage is resource consumption sampled over roughly one second; it is only
// present for running containers.
type Usage struct {
	CPUPercent       float64
	MemoryBytes      uint64
	MemoryLimitBytes uint64
}

type Container struct {
	ID        string
	Name      string
	Image     string
	State     State
	Status    string
	CreatedAt time.Time
	Ports     []Port
	// Project is the Compose project label, empty for standalone containers.
	Project string
	Usage   *Usage
}

type Image struct {
	ID        string
	Tags      []string
	SizeBytes int64
	CreatedAt time.Time
}

type Engine struct {
	Version    string
	APIVersion string
}

type Snapshot struct {
	ObservedAt time.Time
	Engine     Engine
	Containers []Container
	Images     []Image
}

type LogLine struct {
	Stream string
	Time   time.Time
	Text   string
}

type Manager interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	Act(ctx context.Context, id string, action Action) error
	Logs(ctx context.Context, id string, tail int) ([]LogLine, error)
}

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidID accepts only full engine IDs, so names or short prefixes can never
// address a different container than the one shown in the snapshot.
func ValidID(id string) bool {
	return containerIDPattern.MatchString(id)
}

// ClampTail keeps a requested log tail within (0, MaxLogLines].
func ClampTail(tail int) int {
	if tail <= 0 || tail > MaxLogLines {
		return MaxLogLines
	}
	return tail
}
