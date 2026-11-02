package wire

// WebSearchPath reads (GET) and sets (PUT) the administrator's switch of web
// search, which starts and stops its service (ADR 0030).
const WebSearchPath = "/v1/web-search"

// WebSearchRequest turns web search on or off.
type WebSearchRequest struct {
	Enabled bool `json:"enabled"`
}
