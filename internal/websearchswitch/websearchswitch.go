// Package websearchswitch is the contract of the administrator's switch of
// web search (ADR 0030) that the Product Service and the root Host Agent
// share: the Host Agent holds the switch and starts or stops the service;
// internal/websearch reads and sets it. It depends on nothing, so the Host
// Agent links none of web search itself.
package websearchswitch

import "context"

// State is the switch and what systemd reports of the service.
type State struct {
	Enabled bool `json:"enabled"`
	// Running is running, starting, stopped or failed.
	Running string `json:"running"`
}

// The states Running takes.
const (
	Running  = "running"
	Starting = "starting"
	Stopped  = "stopped"
	Failed   = "failed"
)

// Switch turns the service on and off.
type Switch interface {
	WebSearch(ctx context.Context) (State, error)
	SetWebSearch(ctx context.Context, enabled bool) (State, error)
}
