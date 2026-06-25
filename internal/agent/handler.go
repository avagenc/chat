// Package agent composes the Avagenc agent roster into one in-process group chat
// over a shared Zep backend, and serves the human-facing chat API. Agents are a
// feature of this module, not the other way around.
//
// Model: every agent runs its own runner over the SAME shared Zep thread (keyed
// by session-id), so the human and all agents read and write one conversation.
// A single zep.SessionService and zep.MemoryService back the whole roster — agent
// identity is no longer baked into the service: assistant turns are authored by
// each agent's own name, and the inbound user-turn speaker ("human" vs "ava") is
// resolved per run from context via zep.NameFromContext.
//
// Two vertical slices, two ways into the roster:
//
//   - handler.go — the human channel. POST /{agent} runs that agent's runner with
//     the human as the inbound speaker. This file is also the composition spine:
//     the roster wiring (Build), the shared runner builder, the per-run
//     instruction plugin, the run-context helper, and the event collector — all
//     shared by both channels.
//   - ava.go — the Ava channel. Adapts a specialist's runner to ava.SubAgent so
//     Ava can delegate to it inside its own run, on the same shared session, with
//     Ava as the inbound speaker.
package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	zepclient "github.com/getzep/zep-go/v3/client"
	"github.com/go-chi/chi/v5"
	"go.avagenc.com/ava"
	"go.avagenc.com/zee"
	"go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apisess "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/tuya"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	appName       = "avagenc"
	historyLength = 16
	humanSpeaker  = "human" // how a human's inbound turn is attributed in the shared thread (§5.4)
)

// sessionInstruction is the agent-agnostic group-chat framing every agent gets
// on every run, injected by the shared SessionService (WithSessionInstruction).
// Per-agent identity stays in each agent's own module; specialist-only and
// per-channel framing is layered on per run (see specialistBase, ava.go).
//
//go:embed session-instruction.txt
var sessionInstruction string

// humanChannel is an agent's runner plus the per-run instruction the human
// channel injects. Ava (the orchestrator) owns its group-chat behavior in its
// module, so it gets none; specialists get the human-addressed framing.
type humanChannel struct {
	runner      *runner.Runner
	instruction string
}

// Handler serves POST /{agent} by running the agent's runner with the human as
// the inbound speaker. Specialist delegation happens inside Ava's run (ava.go).
type Handler struct {
	channels map[string]humanChannel
}

// Build composes the in-process group chat: one shared session + memory service,
// one runner per agent over them, Ava's delegation wiring, and the human-facing
// Handler. This is the only place that knows the roster and orchestration graph.
func Build(llm model.LLM, zc *zepclient.Client, tuyaClient *tuya.Client) (*Handler, error) {
	// Shared backing services for the whole roster. Speaker and timezone are
	// resolved per run from context; the session instruction is the shared base.
	sessions := zep.NewSessionService(zc,
		zep.WithSpeakerResolver(zep.NameFromContext()),
		zep.WithSessionInstruction(sessionInstruction),
		zep.WithMessagesHistoryLength(historyLength),
		zep.WithTimeHarness(zep.ZoneFromContext()),
	)
	memories := zep.NewMemoryService(zc)

	// Specialists — bare agents from their modules, one runner each.
	zeeAgent, err := zee.New(zee.Config{Model: llm, TuyaClient: tuyaClient})
	if err != nil {
		return nil, fmt.Errorf("compose zee: %w", err)
	}
	zeeRunner, err := newRunner(zeeAgent, sessions, memories)
	if err != nil {
		return nil, err
	}

	// Ava — orchestrator that delegates to specialists' runners as sub-agents.
	avaAgent, err := ava.New(ava.Config{
		Model: llm,
		SubAgents: []ava.SubAgent{
			toSubAgent("zee", zeeDescription, zeeRunner),
			// + rafal, yori once their modules adopt the bare-agent shape.
		},
	})
	if err != nil {
		return nil, fmt.Errorf("compose ava: %w", err)
	}
	avaRunner, err := newRunner(avaAgent, sessions, memories)
	if err != nil {
		return nil, err
	}

	return &Handler{channels: map[string]humanChannel{
		"ava": {runner: avaRunner},
		"zee": {runner: zeeRunner, instruction: specialistHumanInstruction},
	}}, nil
}

type chatRequest struct {
	Message string `json:"message"`
}

type chatResponse struct {
	Response string `json:"response"`
}

// Chat handles POST /{agent}. The agent name is the URL dispatch key; the
// frontend already resolved any @mention into the URL, so this does not parse
// the human's message.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	channel, ok := h.channels[chi.URLParam(r, "agent")]
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

	response, err := collect(channel.runner.Run(runContext(ctx, humanSpeaker), userID, sessionID,
		genai.NewContentFromText(req.Message, genai.RoleUser), adkagent.RunConfig{},
		withInstruction(channel.instruction)...))
	switch {
	case errors.Is(err, zep.ErrSessionOwnerMismatch):
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	case err != nil:
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "agent run failed"})
		return
	}

	apihttp.WriteJSON(w, http.StatusOK, chatResponse{Response: response})
}

// newRunner builds a runner over an agent and the shared session + memory
// services. Threads are keyed by session-id, so every agent's runner reads and
// writes one shared conversation; the speaker and timezone come from the run
// context (see runContext). The instruction plugin lets either channel append
// per-run framing onto an agent module that knows nothing about it.
func newRunner(a adkagent.Agent, sessions *zep.SessionService, memories *zep.MemoryService) (*runner.Runner, error) {
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

// runContext scopes ctx for a run on the shared Zep session: it sets the inbound
// user-turn speaker (read by zep.NameFromContext) and translates the request
// timezone from the api context key into zep's own (read by zep.ZoneFromContext).
// Both channels call it — the human channel with "human", the Ava channel with
// "ava".
func runContext(ctx context.Context, speaker string) context.Context {
	ctx = zep.ContextWithName(ctx, speaker)
	if tz, err := apitime.ZoneFromContext(ctx); err == nil && tz != "" {
		ctx = zep.ContextWithTimezone(ctx, tz)
	}
	return ctx
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

// runInstructionKey carries per-run extra instruction. The temp: prefix scopes
// it to the current invocation only: ADK applies it to in-memory session state
// so this run's callbacks see it, then drops it from the persisted event.
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

// --- specialist framing ------------------------------------------------------

// specialistBase is the per-run framing every specialist gets on top of the
// shared session instruction (sessionInstruction): it is a real, warm group
// member — not a data terminal or Ava's servant — and it has no self-recall.
// Each channel prepends who addressed it this turn (specialistHumanInstruction
// here, specialistAvaInstruction in ava.go), since that is the one thing the
// specialist cannot tell from the shared history alone.
const specialistBase = `Kamu tetap dirimu sendiri — anggota grup yang nyata dan hangat yang kebetulan punya kapabilitas domainmu, bukan terminal data atau pelayan Ava. Setelah mengeksekusi atau mengecek sesuatu, sampaikan hasilnya secara natural ke anggota yang paling tepat (paling sering Human, yang juga membaca chat ini). Kalau maksudnya belum jelas, tanya dulu.

Kamu TIDAK punya self-recall — kamu hanya bisa bertindak untuk saat ini. Kalau diminta sesuatu yang terjadwal atau untuk nanti, jangan coba menjadwalkan; sampaikan dengan natural bahwa itu di luar kemampuanmu dan minta Ava yang mengatur waktunya lalu memanggilmu lagi saat waktunya tiba.`

// specialistHumanInstruction frames a specialist run triggered directly by the
// human (POST /{name}, no Ava hop).
const specialistHumanInstruction = `[CHANNEL]
Giliran ini datang LANGSUNG dari Human — Human menyebut namamu sendiri, tanpa lewat Ava. Balas langsung ke Human.
[/CHANNEL]

` + specialistBase
