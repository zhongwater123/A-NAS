package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/assistantkey"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
)

// assistantKeyTimeout covers encrypting the key and preparing every member's assistant.
const assistantKeyTimeout = 2 * time.Minute

// AssistantKey reports whether the AI assistant's model key is saved (ADR 0024).
func (c *Client) AssistantKey(ctx context.Context) (assistantkey.State, error) {
	ctx, cancel := context.WithTimeout(ctx, assistantKeyTimeout)
	defer cancel()
	var state assistantkey.State
	err := c.doJSON(ctx, c.operationClient, http.MethodGet, wire.AssistantKeyPath, nil, &state)
	return state, err
}

// SetAssistantKey saves the key, which turns every member's assistant on.
func (c *Client) SetAssistantKey(ctx context.Context, key string) (assistantkey.State, error) {
	ctx, cancel := context.WithTimeout(ctx, assistantKeyTimeout)
	defer cancel()
	var state assistantkey.State
	err := c.doJSON(ctx, c.operationClient, http.MethodPut, wire.AssistantKeyPath, wire.AssistantKeyRequest{Key: key}, &state)
	return state, err
}

// DeleteAssistantKey deletes the key, which turns every member's assistant off.
func (c *Client) DeleteAssistantKey(ctx context.Context) (assistantkey.State, error) {
	ctx, cancel := context.WithTimeout(ctx, assistantKeyTimeout)
	defer cancel()
	var state assistantkey.State
	err := c.doJSON(ctx, c.operationClient, http.MethodDelete, wire.AssistantKeyPath, nil, &state)
	return state, err
}

var _ assistantkey.Keeper = (*Client)(nil)
