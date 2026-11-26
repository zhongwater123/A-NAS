//go:build linux

package videocut

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
)

// servedStore is an open store with the assistant socket that answers from
// it; closing it takes the socket's API away first.
type servedStore struct {
	*app.Store
	assistant *protocol.AssistantGate
}

func (s *servedStore) Close() error {
	s.assistant.Serve(nil)
	return s.Store.Close()
}

// listenAssistant serves the assistant socket from gate as config says
// (protocol.ListenAssistant).
func listenAssistant(ctx context.Context, config Config, gate *protocol.AssistantGate) (func(), error) {
	return protocol.ListenAssistant(ctx, config.Accounts, config.AssistantSocket, config.Logger, gate)
}
