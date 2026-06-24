// Package chat composes the agent modules into the monolith's in-process group
// chat and serves the human-facing chat API.
//
// Model (matches the proven Avagenc behavior, now in-process):
//
//   - Every agent runs on its own runner over a shared Zep thread keyed by
//     session-id, so all participants — human and agents — read and write one
//     conversation.
//   - A human talks to an agent directly at POST /{name}/chat (the "human"
//     channel: the user is the human).
//   - Ava delegates to a specialist by calling an ADK tool inside its own run.
//     That tool runs the specialist's "Ava" channel runner on the same session:
//     the inbound persists as a user turn authored "Ava" (prefixed @name), and
//     the specialist replies independently as itself, or stays silent.
//
// There is no chat-side @mention dispatch loop: delegation is a tool call within
// Ava's react loop, not a parse of Ava's output.
package chat

import (
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apisess "go.naturallyfunny.dev/api/session"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// humanChannel is an agent's human-facing runner plus the per-run instruction
// chat injects on that channel. Ava (the orchestrator) needs none; specialists
// get the group-chat framing since their modules are channel-agnostic.
type humanChannel struct {
	runner      *runner.Runner
	instruction string
}

// Service serves POST /{name}/chat by running the agent's human-channel runner.
// Specialist delegation happens inside Ava's run, not here.
type Service struct {
	channels map[string]humanChannel
}

func NewService(channels map[string]humanChannel) *Service {
	return &Service{channels: channels}
}

type chatRequest struct {
	Message string `json:"message"`
}

type chatResponse struct {
	Response string `json:"response"`
}

// Chat handles POST /{agent}/chat. The agent name is the URL dispatch key; the
// frontend already resolved any @mention into the URL (§2.6), so chat does not
// parse the human's message.
func (s *Service) Chat(w http.ResponseWriter, r *http.Request) {
	channel, ok := s.channels[chi.URLParam(r, "agent")]
	if !ok {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "unknown agent"})
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}

	ctx := r.Context()
	userID, err := apiuser.IDFromContext(ctx)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return
	}
	sessionID, err := apisess.IDFromContext(ctx)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}

	response, err := collect(channel.runner.Run(ctx, userID, sessionID,
		genai.NewContentFromText(req.Message, genai.RoleUser), adkagent.RunConfig{},
		withInstruction(channel.instruction)...))
	switch {
	case errors.Is(err, zep.ErrSessionOwnerMismatch): // §6.3
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	case err != nil:
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "agent run failed"})
		return
	}

	apihttp.WriteJSON(w, http.StatusOK, chatResponse{Response: response})
}

// collect drains a run and returns the concatenated text of its events. An empty
// result (e.g. an agent that chose to stay silent) yields "".
func collect(events iter.Seq2[*session.Event, error]) (string, error) {
	var b strings.Builder
	for ev, err := range events {
		if err != nil {
			return "", err
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p != nil && p.Text != "" {
				b.WriteString(p.Text)
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}
