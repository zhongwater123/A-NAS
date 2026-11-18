package wire

// AssistantKeyPath reads (GET), saves (PUT) and deletes (DELETE) the administrator's model
// key of the AI assistant, which turns every member's assistant on and off (ADR 0024).
const AssistantKeyPath = "/v1/assistant-key"

// AssistantKeyRequest saves a key.
type AssistantKeyRequest struct {
	Key string `json:"key"`
}
