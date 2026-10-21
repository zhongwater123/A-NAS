package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ChatModel is an OpenAI-compatible chat completions service: the model
// gateway the AI assistant uses (ADR 0024), which holds the provider's key
// and forwards requests unchanged, so translations and summaries use the
// assistant's DeepSeek model without the Product Service ever reading the
// key. Socket names the gateway's Unix socket.
type ChatModel struct {
	Socket string
	Model  string
	// Timeout bounds one completion; summaries of long recordings take
	// minutes.
	Timeout time.Duration
	client  *http.Client
}

// NewChatModel reaches the gateway at socket.
func NewChatModel(socket, model string) *ChatModel {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second,
	}
	return &ChatModel{Socket: socket, Model: model, Timeout: 10 * time.Minute, client: &http.Client{Transport: transport}}
}

func (m *ChatModel) Name() string { return m.Model }

// ErrTextModelUnavailable: the gateway could not be reached or answered
// with an error the same request would meet again later.
var ErrTextModelUnavailable = errors.New("the text model is unavailable")

func (m *ChatModel) Complete(ctx context.Context, prompt Prompt) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	body := map[string]any{
		"model": m.Model,
		"messages": []map[string]string{
			{"role": "system", "content": prompt.System},
			{"role": "user", "content": prompt.User},
		},
		"temperature": 0.3,
		"stream":      false,
	}
	if prompt.MaxTokens > 0 {
		body["max_tokens"] = prompt.MaxTokens
	}
	if prompt.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gateway/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", ErrTextModelUnavailable, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTextModelUnavailable, err)
	}
	if response.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(data[:min(len(data), 300)]))
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return "", fmt.Errorf("%w: HTTP %d %s", ErrTextModelUnavailable, response.StatusCode, message)
		}
		return "", fmt.Errorf("text model answered HTTP %d: %s", response.StatusCode, message)
	}
	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return "", fmt.Errorf("decode the text model's answer: %w", err)
	}
	if len(answer.Choices) == 0 || strings.TrimSpace(answer.Choices[0].Message.Content) == "" {
		return "", errors.New("the text model answered with nothing")
	}
	if answer.Choices[0].FinishReason == "length" {
		return "", errors.New("the text model's answer was cut off")
	}
	return answer.Choices[0].Message.Content, nil
}
