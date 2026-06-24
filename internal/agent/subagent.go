package agent

import (
	"context"
	"fmt"

	"go.avagenc.com/ava"
	apisess "go.naturallyfunny.dev/api/session"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

// runnerSubAgent adapts a specialist's Ava-channel runner to ava.SubAgent. This
// is the in-process successor to the old HTTP AgentClient: instead of POSTing to
// the specialist's /chat/agent, it runs the specialist's runner directly on the
// shared session. The inbound message persists as a user turn (authored "Ava" by
// that runner's session service) prefixed with @name; the specialist then reads
// the full shared history and replies independently as itself — or returns ""
// to stay silent (an empty reply is never persisted).
type runnerSubAgent struct {
	name        string
	description string
	runner      *runner.Runner
}

var _ ava.SubAgent = (*runnerSubAgent)(nil)

// NewSubAgent wraps a specialist's Ava-addressed runner. description is what
// Ava's model sees when deciding whether to delegate (the specialist's
// capabilities + the group-chat conventions), so it lives at composition time,
// with the roster.
func NewSubAgent(name, description string, avaChannelRunner *runner.Runner) ava.SubAgent {
	return &runnerSubAgent{
		name:        name,
		description: description,
		runner:      avaChannelRunner,
	}
}

func (s *runnerSubAgent) Name() string        { return s.name }
func (s *runnerSubAgent) Description() string { return s.description }

func (s *runnerSubAgent) Run(ctx context.Context, message string) (string, error) {
	userID, err := apiuser.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: user identity: %w", s.name, err)
	}
	sessionID, err := apisess.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: session identity: %w", s.name, err)
	}

	// message is Ava's verbatim delegation text. Ava is instructed to tag @name
	// itself (lowercase, anywhere) — the package does not prepend it (no rule-based
	// tagging). The specialist also reads the shared history, so the tag is just
	// a readability cue, never the routing mechanism.
	return collect(s.runner.Run(ctx, userID, sessionID,
		genai.NewContentFromText(message, genai.RoleUser), adkagent.RunConfig{},
		withInstruction(specialistGroupChatInstruction)...))
}
