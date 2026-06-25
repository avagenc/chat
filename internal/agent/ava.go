package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.avagenc.com/ava"
	apihttp "go.naturallyfunny.dev/api/http"
	apisess "go.naturallyfunny.dev/api/session"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

// ForAva adapts a specialist as one of Ava's sub-agents. Name and Description are
// taken from the specialist's own agent (single source of truth), so the
// delegation tool Ava's model sees matches the specialist's identity; the run is
// driven by the specialist's own runner over the shared session.
func ForAva(a adkagent.Agent, r *runner.Runner) ava.SubAgent {
	return &avaSubAgent{name: a.Name(), description: a.Description(), runner: r}
}

// avaSubAgent is the in-process successor to the old HTTP AgentClient: instead of
// POSTing to a specialist's /chat, Ava runs the specialist's runner directly on
// the shared session with Ava as the inbound speaker. The message persists as a
// user turn authored "ava"; the specialist then reads the full shared history and
// replies independently as itself, or returns "" to stay silent (an empty reply
// is never persisted).
type avaSubAgent struct {
	name        string
	description string
	runner      *runner.Runner
}

var _ ava.SubAgent = (*avaSubAgent)(nil)

func (s *avaSubAgent) Name() string        { return s.name }
func (s *avaSubAgent) Description() string { return s.description }

func (s *avaSubAgent) Run(ctx context.Context, message string) (string, error) {
	userID, err := apiuser.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: user identity: %w", s.name, err)
	}
	sessionID, err := apisess.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: session identity: %w", s.name, err)
	}

	// message is Ava's verbatim delegation text. Ava is instructed to tag @name
	// itself — the package does not prepend it. The specialist also reads the
	// shared history, so the tag is a readability cue, never the routing mechanism.
	return collect(s.runner.Run(runContext(ctx, avaSpeaker), userID, sessionID,
		genai.NewContentFromText(message, genai.RoleUser), adkagent.RunConfig{},
		withInstruction(ranByAva)...))
}

// AvaHandler serves Ava's two non-delegated entry points into the shared thread:
// HandleHuman (a human messages Ava) and HandleSelfAwaken (Ava's own scheduled postera note
// fires). Specialist delegation happens inside Ava's run via avaSubAgent, not
// here.
type AvaHandler struct {
	runner *runner.Runner
}

func NewAvaHandler(r *runner.Runner) *AvaHandler { return &AvaHandler{runner: r} }

// HandleHuman handles POST /ava — a human addressing Ava directly. Ava gets no per-run
// framing: it owns its group-chat behavior in its own module.
func (h *AvaHandler) HandleHuman(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}
	userID, sessionID, ok := chatIdentity(w, r.Context())
	if !ok {
		return
	}
	respondRun(w, runContext(r.Context(), humanSpeaker), h.runner,
		userID, sessionID, req.Message, "")
}

// HandleSelfAwaken handles the Cloud Tasks callback that fires one of Ava's scheduled
// postera notes (prospective memory / self-recall). Unlike HandleHuman, the body is the
// raw note text Ava saved at creation time, not a JSON envelope; user-id,
// session-id and time-zone arrive as headers (set by the enqueuer) and are read
// from context by the same identity/timezone middleware. The run is attributed to
// Ava itself ("ava") and framed by ranByPostera.
//
// TODO(auth): this endpoint is currently behind the same temp header-identity
// middleware as the human routes. Cloud Tasks callbacks need their own auth
// (OIDC) before any untrusted deployment.
//
// TODO(delivery): there is no push-notification channel yet — Ava's reply is
// persisted to the shared session (so it surfaces when the human opens the app)
// but not actively delivered.
func (h *AvaHandler) HandleSelfAwaken(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "failed to read body"})
		return
	}
	message := strings.TrimSpace(string(raw))
	if message == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "empty recall body"})
		return
	}
	userID, sessionID, ok := chatIdentity(w, r.Context())
	if !ok {
		return
	}
	respondRun(w, runContext(r.Context(), avaSpeaker), h.runner,
		userID, sessionID, message, ranByPostera)
}
