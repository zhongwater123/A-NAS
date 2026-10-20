package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The 记忆 page (docs/specs/ai-assistant.md): what the assistant remembers and
// the skills it made, from Hermes's own learning graph — the routes Hermes
// Desktop's star map uses (docs/research/hermes-agent-runtime.md, "记忆与自建技能").
// Only reading and deleting are passed; Hermes keeps a deleted skill in its
// archive, as it does everywhere. A-NAS's own skills come from
// skills.external_dirs, which Hermes leaves out of the graph and refuses to
// delete; the graph is taken as it is, whatever a skill is named: the
// assistant may well call one it made anas-photos.

// memoryIDPattern bounds a learning-graph id: memory:<store>:<n>:<fingerprint>
// or a skill's name. An id must also be in the graph just read.
var memoryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,199}$`)

type memoryItem struct {
	ID string `json:"id"`
	// Store is "memory" for the assistant's notes, "profile" for what it
	// noted about the member.
	Store     string `json:"store"`
	Text      string `json:"text"`
	UpdatedAt *int64 `json:"updatedAt,omitempty"`
}

type learnedSkill struct {
	ID       string `json:"id"`
	Category string `json:"category,omitempty"`
	// CreatedBy is "learn" when the member asked for it, "agent" when the
	// assistant made it on its own, empty when it only used it.
	CreatedBy string `json:"createdBy,omitempty"`
	UseCount  int    `json:"useCount"`
	UpdatedAt *int64 `json:"updatedAt,omitempty"`
}

type learned struct {
	Memories []memoryItem   `json:"memories"`
	Skills   []learnedSkill `json:"skills"`
}

// learning reads the member's learning graph.
func (h *handler) learning(ctx context.Context, account string) (learned, error) {
	status, data, err := h.rest(ctx, account, http.MethodGet, "/api/learning/graph", nil)
	if err != nil {
		return learned{}, err
	}
	if status != http.StatusOK {
		return learned{}, fmt.Errorf("learning graph answered %d: %s", status, truncate(data))
	}
	var graph struct {
		Nodes []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Category  string `json:"category"`
			CreatedBy string `json:"createdBy"`
			UseCount  int    `json:"useCount"`
			Timestamp *int64 `json:"timestamp"`
			Source    string `json:"memorySource"`
		} `json:"nodes"`
		Memory []struct {
			Source      string `json:"source"`
			Body        string `json:"body"`
			Fingerprint string `json:"fingerprint"`
		} `json:"memory"`
	}
	if err := json.Unmarshal(data, &graph); err != nil {
		return learned{}, err
	}
	bodies := map[string]string{}
	for _, card := range graph.Memory {
		bodies[card.Source+":"+card.Fingerprint] = card.Body
	}
	result := learned{Memories: []memoryItem{}, Skills: []learnedSkill{}}
	for _, node := range graph.Nodes {
		if !memoryIDPattern.MatchString(node.ID) {
			continue
		}
		switch node.Kind {
		case "memory":
			// memory:<store>:<n>:<fingerprint>
			parts := strings.Split(node.ID, ":")
			text := bodies[node.Source+":"+parts[len(parts)-1]]
			result.Memories = append(result.Memories, memoryItem{ID: node.ID, Store: node.Source, Text: text, UpdatedAt: node.Timestamp})
		case "skill":
			result.Skills = append(result.Skills, learnedSkill{ID: node.ID, Category: node.Category, CreatedBy: node.CreatedBy,
				UseCount: node.UseCount, UpdatedAt: node.Timestamp})
		}
	}
	return result, nil
}

func (l learned) has(id string) bool {
	for _, item := range l.Memories {
		if item.ID == id {
			return true
		}
	}
	for _, skill := range l.Skills {
		if skill.ID == id {
			return true
		}
	}
	return false
}

func (h *handler) getMemory(w http.ResponseWriter, r *http.Request, account string) {
	result, err := h.learning(r.Context(), account)
	if err != nil {
		h.unavailable(w, account, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// memoryID reads the path's id and checks that it is in the member's graph now.
func (h *handler) memoryID(w http.ResponseWriter, r *http.Request, account string) (string, bool) {
	id := r.PathValue("id")
	if !memoryIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_memory", "invalid memory id")
		return "", false
	}
	current, err := h.learning(r.Context(), account)
	if err != nil {
		h.unavailable(w, account, err)
		return "", false
	}
	if !current.has(id) {
		writeError(w, http.StatusNotFound, "memory_not_found", "the assistant no longer has this")
		return "", false
	}
	return id, true
}

func (h *handler) getMemoryItem(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := h.memoryID(w, r, account)
	if !ok {
		return
	}
	var detail struct {
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if !h.call(w, r, account, http.MethodGet, "/api/learning/node?"+url.Values{"id": {id}}.Encode(), nil, &detail) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "kind": detail.Kind, "content": detail.Content})
}

func (h *handler) deleteMemoryItem(w http.ResponseWriter, r *http.Request, account string) {
	id, ok := h.memoryID(w, r, account)
	if !ok {
		return
	}
	switch err := h.forget(r.Context(), account, id); {
	case errors.Is(err, errMemoryChanged):
		writeError(w, http.StatusConflict, "memory_changed", "the assistant's memory changed; reload it")
	case err != nil:
		h.unavailable(w, account, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

var errMemoryChanged = errors.New("the learning graph changed")

// forget deletes one node. Hermes removes a memory inside the memory tool's own
// lock, finding it again by its text, and moves a skill into its archive.
func (h *handler) forget(ctx context.Context, account, id string) error {
	payload, _ := json.Marshal(map[string]string{"id": id})
	status, data, err := h.rest(ctx, account, http.MethodDelete, "/api/learning/node", payload)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if strings.Contains(strings.ToLower(string(data)), "stale") || status == http.StatusNotFound {
		return errMemoryChanged
	}
	return fmt.Errorf("learning node delete answered %d: %s", status, truncate(data))
}

// clearMemory forgets everything in the graph and, when asked, deletes every
// conversation too: only then is it gone for good, since a conversation keeps
// the memory it started with and the assistant can search earlier ones.
func (h *handler) clearMemory(w http.ResponseWriter, r *http.Request, account string) {
	var body struct {
		Conversations bool `json:"conversations"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	current, err := h.learning(ctx, account)
	if err != nil {
		h.unavailable(w, account, err)
		return
	}
	result := map[string]int{"memories": 0, "skills": 0, "conversations": 0, "busy": 0}
	for _, item := range current.Memories {
		if err := h.forget(ctx, account, item.ID); err == nil {
			result["memories"]++
		} else if !errors.Is(err, errMemoryChanged) {
			h.unavailable(w, account, err)
			return
		}
	}
	for _, skill := range current.Skills {
		if err := h.forget(ctx, account, skill.ID); err == nil {
			result["skills"]++
		} else if !errors.Is(err, errMemoryChanged) {
			h.unavailable(w, account, err)
			return
		}
	}
	if body.Conversations {
		deleted, busy, err := h.deleteConversations(ctx, account)
		if err != nil {
			h.unavailable(w, account, err)
			return
		}
		result["conversations"], result["busy"] = deleted, busy
	}
	h.logger.Info("assistant memory cleared", "account", account, "memories", result["memories"], "skills", result["skills"],
		"conversations", result["conversations"])
	writeJSON(w, http.StatusOK, result)
}

// deleteConversations deletes every stored conversation; one with a turn
// running is left and counted as busy.
func (h *handler) deleteConversations(ctx context.Context, account string) (int, int, error) {
	var ids []string
	for offset := 0; ; offset += sessionLimit {
		query := url.Values{"limit": {fmt.Sprint(sessionLimit)}, "offset": {fmt.Sprint(offset)}, "archived": {"include"}}
		status, data, err := h.rest(ctx, account, http.MethodGet, "/api/sessions?"+query.Encode(), nil)
		if err != nil {
			return 0, 0, err
		}
		var page struct {
			Sessions []hermesSession `json:"sessions"`
		}
		if status != http.StatusOK || json.Unmarshal(data, &page) != nil {
			return 0, 0, fmt.Errorf("listing conversations answered %d", status)
		}
		for _, session := range page.Sessions {
			if storedIDPattern.MatchString(session.ID) {
				ids = append(ids, session.ID)
			}
		}
		if len(page.Sessions) < sessionLimit {
			break
		}
	}
	deleted, busy := 0, 0
	for _, id := range ids {
		status, _, err := h.rest(ctx, account, http.MethodDelete, "/api/sessions/"+id, nil)
		switch {
		case err != nil:
			return deleted, busy, err
		case status == http.StatusOK || status == http.StatusNoContent:
			deleted++
		default:
			busy++
		}
	}
	return deleted, busy, nil
}
