package montageapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"golang.org/x/time/rate"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/montage"
)

// The montage center's assistant socket (docs/architecture/montage-planning-session.md):
// bound only into each member's planning instance, where the montage MCP
// server reaches it, and reached outside the sandboxes by root's planner,
// which claims and reports the rounds. The montage service knows the member
// from the peer's UID, or the account root names, and puts them in the
// request's context (accounts.WithUser); every call answers only for that
// member's jobs.

// AssistantPrefix is where the assistant socket's API lives.
const AssistantPrefix = "/v1"

// Limits on the assistant socket: the member's terminal in the sandbox runs
// as the same UID, so the socket bounds what it may send.
const (
	maxAssistantBytes = 80 << 10
	assistantRate     = 5
	assistantBurst    = 20
)

type assistantHandler struct {
	store  *montage.Store
	logger *slog.Logger
	mux    *http.ServeMux
	mu     sync.Mutex
	limits map[string]*rate.Limiter
}

// Assistant returns the assistant socket's API.
func Assistant(store *montage.Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &assistantHandler{store: store, logger: logger, mux: http.NewServeMux(), limits: map[string]*rate.Limiter{}}
	routes := map[string]func(http.ResponseWriter, *http.Request, string){
		"POST /rounds/claim":             h.claim,
		"POST /rounds/{roundID}/started": h.started,
		"POST /rounds/{roundID}/abandon": h.abandon,
		"POST /rounds/{roundID}/end":     h.end,
		"POST /tools/brief":              h.tool(montage.StepOutline, false),
		"POST /tools/outline":            h.tool(montage.StepOutline, true),
		"POST /tools/shots-brief":        h.tool(montage.StepShots, false),
		"POST /tools/shots":              h.tool(montage.StepShots, true),
	}
	for pattern, serve := range routes {
		h.mux.HandleFunc(prefixed(pattern), h.withMember(serve))
	}
	h.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// prefixed puts a "METHOD /path" pattern under AssistantPrefix.
func prefixed(pattern string) string {
	for i := range len(pattern) {
		if pattern[i] == ' ' {
			return pattern[:i+1] + AssistantPrefix + pattern[i+1:]
		}
	}
	return AssistantPrefix + pattern
}

func (h *assistantHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *assistantHandler) limiter(owner string) *rate.Limiter {
	h.mu.Lock()
	defer h.mu.Unlock()
	limiter, ok := h.limits[owner]
	if !ok {
		limiter = rate.NewLimiter(assistantRate, assistantBurst)
		h.limits[owner] = limiter
	}
	return limiter
}

func (h *assistantHandler) withMember(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.UserFrom(r.Context())
		if !ok || user.ID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if !h.limiter(user.ID).Allow() {
			httpjson.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxAssistantBytes)
		next(w, r, user.ID)
	}
}

func (h *assistantHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, montage.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, montage.ErrNoPlanWanted):
		httpjson.WriteError(w, http.StatusConflict, "no_plan_wanted", "no job waits for planning")
	case errors.Is(err, montage.ErrRoundOver):
		httpjson.WriteError(w, http.StatusConflict, "round_over", "this round is over")
	case errors.Is(err, montage.ErrInvalid):
		httpjson.WriteError(w, http.StatusBadRequest, "validation_failed", "the request is not valid")
	default:
		httpjson.WriteInternal(w, r, h.logger, err)
	}
}

func (h *assistantHandler) claim(w http.ResponseWriter, r *http.Request, owner string) {
	claim, err := h.store.ClaimRound(r.Context(), owner)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, claim)
}

type startedRequest struct {
	RunRef string `json:"runRef"`
}

func (h *assistantHandler) started(w http.ResponseWriter, r *http.Request, owner string) {
	var request startedRequest
	if !httpjson.Read(w, r, maxAssistantBytes, &request) {
		return
	}
	if err := h.store.StartRound(r.Context(), owner, r.PathValue("roundID"), request.RunRef); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// outcomeRequest is how a round stopped: in the planner's words (Outcome),
// whether it never started for something it could not reach or for an error
// (Failure, "unreachable" or "error"), and how a conversation that started
// ended (Result, "complete" or what stopped it).
type outcomeRequest struct {
	Outcome string `json:"outcome"`
	Failure string `json:"failure"`
	Result  string `json:"result"`
}

func (h *assistantHandler) abandon(w http.ResponseWriter, r *http.Request, owner string) {
	var request outcomeRequest
	if !httpjson.Read(w, r, maxAssistantBytes, &request) {
		return
	}
	if err := h.store.AbandonRound(r.Context(), owner, r.PathValue("roundID"), request.Outcome, request.Failure); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *assistantHandler) end(w http.ResponseWriter, r *http.Request, owner string) {
	var request outcomeRequest
	if !httpjson.Read(w, r, maxAssistantBytes, &request) {
		return
	}
	if err := h.store.EndRound(r.Context(), owner, r.PathValue("roundID"), request.Outcome, request.Result); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type toolRequest struct {
	JobID        string          `json:"jobId"`
	AttemptToken string          `json:"attemptToken"`
	Answer       json.RawMessage `json:"answer,omitempty"`
}

// tool serves a montage tool: a step's brief, or a submission of it.
func (h *assistantHandler) tool(step string, submit bool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, owner string) {
		var request toolRequest
		if !httpjson.Read(w, r, maxAssistantBytes, &request) {
			return
		}
		var answer any
		var err error
		if submit {
			answer, err = h.store.Submit(r.Context(), owner, request.JobID, request.AttemptToken, step, request.Answer)
		} else {
			answer, err = h.store.Brief(r.Context(), owner, request.JobID, request.AttemptToken, step)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpjson.Write(w, http.StatusOK, answer)
	}
}
