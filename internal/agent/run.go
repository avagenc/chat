// Package agent supplies the Avagenc roster's in-process group chat over a shared
// Zep thread: one runner per agent, Ava's sub-agent adapter, and the two HTTP
// handlers. It does NOT hide the wiring behind a factory — composition lives in
// the consumer (cmd/.../main.go), the same as every other feature: main builds
// the shared zep SessionService/MemoryService, a runner per agent (NewRunner),
// Ava's delegation graph (ForAva + ava.New), and the handlers (NewAvaHandler,
// NewSpecialistHandler).
//
// Model: every agent's runner reads and writes the SAME shared Zep thread (keyed
// by session-id), so the human and all agents share one conversation. Agent
// identity is not baked into the services — assistant turns are authored by each
// agent's own name, and the inbound user-turn speaker ("human" vs "ava") is set
// per run from context (runContext) and read by zep.NameFromContext.
//
// Three entry points into the thread, three per-run framings layered on top of
// the shared SessionInstruction:
//
//   - SpecialistHandler.Chat — a human addresses a specialist directly. Speaker
//     "human", framed by ranByHuman. (specialist.go)
//   - avaSubAgent.Run        — Ava delegates to a specialist inside its own run.
//     Speaker "ava", framed by ranByAva. (ava.go)
//   - AvaHandler.Awaken      — Ava is woken by its own scheduled postera note.
//     Speaker "ava", framed by ranByPostera. (ava.go)
//
// AvaHandler.Chat (human → Ava) gets no per-run framing: Ava owns its group-chat
// behavior in its own module.
package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apisess "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	appName      = "avagenc"
	humanSpeaker = "human" // inbound human turn, attributed in the shared thread
	avaSpeaker   = "ava"   // inbound Ava turn (delegation or self-recall)
)

// SessionInstruction is the agent-agnostic group-chat framing every agent gets on
// every run. It is embedded here (next to the per-channel instruction text) but
// applied by the consumer, which owns the shared SessionService:
//
//	adkzep.NewSessionService(zc, adkzep.WithSessionInstruction(agent.SessionInstruction), ...)
//
//go:embed session-instruction.txt
var SessionInstruction string

// Per-run framing, one file per channel. The agent modules know nothing about
// these; the instruction plugin (below) appends the right one per run.
//
//go:embed ran-by-human-instruction.txt
var ranByHuman string

//go:embed ran-by-ava-instruction.txt
var ranByAva string

//go:embed ran-by-postera-instruction.txt
var ranByPostera string

// NewRunner builds a runner over an agent and the shared session + memory
// services. Threads are keyed by session-id, so every runner reads and writes the
// one shared conversation; speaker and timezone come from the run context
// (runContext). The chat-instruction plugin is registered here so either channel
// can append per-run framing via withInstruction — that is the one piece that
// must live at runner-construction time, which is why this constructor exists
// instead of a bare runner.New in the consumer.
func NewRunner(a adkagent.Agent, sessions session.Service, memories memory.Service) (*runner.Runner, error) {
	instructionPlugin, err := newInstructionPlugin()
	if err != nil {
		return nil, fmt.Errorf("agent: instruction plugin for %q: %w", a.Name(), err)
	}
	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             a,
		SessionService:    sessions,
		MemoryService:     memories,
		AutoCreateSession: true,
		PluginConfig:      runner.PluginConfig{Plugins: []*plugin.Plugin{instructionPlugin}},
	})
	if err != nil {
		return nil, fmt.Errorf("agent: runner for %q: %w", a.Name(), err)
	}
	return r, nil
}

type chatRequest struct {
	Message string `json:"message"`
}

type chatResponse struct {
	Response string `json:"response"`
}

// runContext scopes ctx for one run on the shared thread: it sets the inbound
// speaker (read by zep.NameFromContext) and lifts the request timezone from the
// api context key into zep's (read by zep.ZoneFromContext).
func runContext(ctx context.Context, speaker string) context.Context {
	ctx = zep.ContextWithName(ctx, speaker)
	if tz, err := apitime.ZoneFromContext(ctx); err == nil && tz != "" {
		ctx = zep.ContextWithTimezone(ctx, tz)
	}
	return ctx
}

// chatIdentity pulls the user and session IDs every run needs from ctx, writing
// the matching problem response and returning ok=false if either is missing.
func chatIdentity(w http.ResponseWriter, ctx context.Context) (userID, sessionID string, ok bool) {
	userID, err := apiuser.IDFromContext(ctx)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return "", "", false
	}
	sessionID, err = apisess.IDFromContext(ctx)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return "", "", false
	}
	return userID, sessionID, true
}

// respondRun runs message on the shared thread and writes the JSON reply. ctx must
// already carry the inbound speaker (call runContext first); instruction is the
// per-run framing ("" for none, e.g. Ava's human channel).
func respondRun(w http.ResponseWriter, ctx context.Context, run *runner.Runner, userID, sessionID, message, instruction string) {
	response, err := collect(run.Run(ctx, userID, sessionID,
		genai.NewContentFromText(message, genai.RoleUser), adkagent.RunConfig{},
		withInstruction(instruction)...))
	switch {
	case errors.Is(err, zep.ErrSessionOwnerMismatch):
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
	case err != nil:
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "agent run failed"})
	default:
		apihttp.WriteJSON(w, http.StatusOK, chatResponse{Response: response})
	}
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

// --- per-run instruction plugin ---------------------------------------------
//
// How the package layers situational framing on top of an agent module that
// knows nothing about it: the module owns its base identity, this appends the
// delta per run via a before-model plugin registered on every runner. A run that
// passes no withInstruction simply gets no delta.

// runInstructionKey carries per-run extra instruction. The temp: prefix scopes it
// to the current invocation only: ADK applies it to in-memory session state so
// this run's callbacks see it, then drops it from the persisted event.
const runInstructionKey = "temp:chat_instruction"

func newInstructionPlugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name:                "chat-run-instruction",
		BeforeModelCallback: injectRunInstruction,
	})
}

func injectRunInstruction(ctx adkagent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
	v, err := ctx.State().Get(runInstructionKey)
	if err != nil {
		return nil, nil // not set for this run — no extra instruction.
	}
	extra, ok := v.(string)
	if !ok || extra == "" {
		return nil, nil
	}

	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}
	if req.Config.SystemInstruction == nil {
		req.Config.SystemInstruction = &genai.Content{Role: "system"}
	}
	req.Config.SystemInstruction.Parts = append(
		req.Config.SystemInstruction.Parts,
		&genai.Part{Text: "\n\n" + extra},
	)
	return nil, nil // continue to the model as usual.
}

// withInstruction returns the RunOptions that inject extra into this run, or no
// options when extra is empty (so the agent runs on its base instruction alone).
func withInstruction(extra string) []runner.RunOption {
	if extra == "" {
		return nil
	}
	return []runner.RunOption{runner.WithStateDelta(map[string]any{runInstructionKey: extra})}
}
