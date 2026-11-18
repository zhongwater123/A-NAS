// Package assistantkey is the contract of the AI assistant's model key (ADR 0024,
// docs/decisions/2026-11-17-assistant-v1-cloud-model.md) that the Product Service and the
// root Host Agent share. The administrator pastes the household's DeepSeek key in Settings;
// the Host Agent keeps it as a systemd credential that only the model gateway's unit loads,
// and while it is saved every active member has an assistant. Nobody reads the key back. It
// depends on nothing, so the Host Agent links none of the assistant's API.
package assistantkey

import (
	"context"
	"errors"
	"sync"
)

// State says whether a key is saved and what systemd reports of the model gateway.
type State struct {
	Configured bool `json:"configured"`
	// Gateway is running, starting, stopped or failed.
	Gateway string `json:"gateway"`
}

// The states Gateway takes.
const (
	Running  = "running"
	Starting = "starting"
	Stopped  = "stopped"
	Failed   = "failed"
)

// MinKeyBytes and MaxKeyBytes bound a key: DeepSeek's are "sk-" and 32 hexadecimal digits.
const (
	MinKeyBytes = 16
	MaxKeyBytes = 512
)

// Keeper saves and deletes the key and reports its state.
type Keeper interface {
	AssistantKey(ctx context.Context) (State, error)
	SetAssistantKey(ctx context.Context, key string) (State, error)
	DeleteAssistantKey(ctx context.Context) (State, error)
}

// ValidKey reports whether key can be one: printable ASCII without spaces, of a sane length.
func ValidKey(key string) bool {
	if len(key) < MinKeyBytes || len(key) > MaxKeyBytes {
		return false
	}
	for i := range len(key) {
		if key[i] <= ' ' || key[i] > '~' {
			return false
		}
	}
	return true
}

// Memory is a Keeper for development, where there is no Host Agent: it remembers only
// whether a key was saved.
type Memory struct {
	mu         sync.Mutex
	configured bool
}

func (m *Memory) AssistantKey(context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked(), nil
}

func (m *Memory) SetAssistantKey(_ context.Context, key string) (State, error) {
	if !ValidKey(key) {
		return State{}, errors.New("invalid assistant key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configured = true
	return m.stateLocked(), nil
}

func (m *Memory) DeleteAssistantKey(context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configured = false
	return m.stateLocked(), nil
}

func (m *Memory) stateLocked() State {
	if m.configured {
		return State{Configured: true, Gateway: Running}
	}
	return State{Gateway: Stopped}
}
