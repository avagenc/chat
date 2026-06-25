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

// avaSpeaker is how an Ava-delegated inbound turn is attributed in the shared
// thread: it persists as a user turn authored "ava", so the specialist (and the
// human, who reads the same chat) see who asked.
const avaSpeaker = "ava"

// zeeDescription is orchestration metadata — what Ava's model sees when deciding
// whether to delegate. It lives with the roster wiring (here), not in the zee
// module, because it describes zee *as a teammate in this group chat*.
const zeeDescription = "Specialist for the user's entire Tuya smart home: lights, " +
	"curtains, switches, sensors, cameras, and any connected device — status and " +
	"control. Zee is a participant in this same group-chat session and reads the " +
	"full history, so don't repeat context already there. Tag @zee yourself when you " +
	"delegate. You may call with an empty message — Zee will read the session and act. " +
	"Zee has NO self-recall: never give it a scheduled/future-time task; schedule it on " +
	"your own recall and delegate a present-tense action when it fires."

// specialistAvaInstruction frames a specialist run triggered by Ava's delegation
// (a tool call inside Ava's own run). It shares specialistBase with the human
// channel; only the addressing differs.
const specialistAvaInstruction = `[RUN_INFO]
Giliran ini adalah delegasi dari Ava. Saya tetap anggota grup yang nyata, bukan pelayan Ava: balas secara natural ke anggota yang paling tepat (umumnya Human, yang juga membaca chat ini), bukan sekadar lapor balik ke Ava.
[/RUN_INDO]

` + specialistBase

// runnerSubAgent adapts a specialist's runner to ava.SubAgent — the in-process
// successor to the old HTTP AgentClient. Instead of POSTing to the specialist's
// /chat, it runs the specialist's runner directly on the shared session with Ava
// as the inbound speaker: the message persists as a user turn authored "ava"; the
// specialist then reads the full shared history and replies independently as
// itself, or returns "" to stay silent (an empty reply is never persisted).
type runnerSubAgent struct {
	name        string
	description string
	runner      *runner.Runner
}

var _ ava.SubAgent = (*runnerSubAgent)(nil)

// toSubAgent wraps a specialist's runner as one of Ava's sub-agents. description
// is what Ava's model sees when deciding whether to delegate, so it lives at
// composition time with the roster (Build).
func toSubAgent(name, description string, r *runner.Runner) ava.SubAgent {
	return &runnerSubAgent{name: name, description: description, runner: r}
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
	// itself — the package does not prepend it. The specialist also reads the
	// shared history, so the tag is a readability cue, never the routing mechanism.
	return collect(s.runner.Run(runContext(ctx, avaSpeaker), userID, sessionID,
		genai.NewContentFromText(message, genai.RoleUser), adkagent.RunConfig{},
		withInstruction(specialistAvaInstruction)...))
}
