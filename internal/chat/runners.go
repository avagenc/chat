package chat

import (
	"fmt"

	zepclient "github.com/getzep/zep-go/v3/client"
	"go.naturallyfunny.dev/adk/zep"
	apitime "go.naturallyfunny.dev/api/time"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/runner"
)

const (    
	appName       = "avagenc"
	historyLength = 8
)

// Channel display names: who "the user" is from the agent's perspective on a
// channel. They make the shared thread read naturally — a human's turn shows as
// "Human", an Ava-delegated turn shows as "Ava" (§5.4).
const (
	humanDisplayName = "Human"
	avaDisplayName   = "Ava"
)

// newRunner builds a runner over an agent and a per-channel Zep session service
// sharing zc. Threads are keyed by session-id, so every channel of every agent
// reads and writes one shared conversation; displayName only sets how this
// channel's inbound "user" turns are attributed.
//
// NOTE: per-channel *instruction* (the old for-human / for-ava prompts that tell
// a specialist how to behave on each channel) is applied here via a
// plugin.BeforeModelCallback (§2.4) — wired in a follow-up. The base identity
// stays in the agent module; only the channel delta is added at the runner.
func newRunner(a adkagent.Agent, zc *zepclient.Client, displayName string) (*runner.Runner, error) {
	ss := zep.NewSessionService(zc, a.Name(),
		zep.WithMessagesHistoryLength(historyLength),
		zep.WithKnowledgeContext(nil),
		zep.WithUserDisplayName(displayName),
		zep.WithTimeHarnessFromContext(apitime.ContextKey),
	)

	// The instruction plugin lets chat append per-run group-chat / channel
	// framing (instruction.go) onto an agent module that knows nothing about it.
	// We use real per-agent runners (not agenttool), so the plugin fires for
	// every agent — sidestepping adk-go #669.
	instructionPlugin, err := newInstructionPlugin()
	if err != nil {
		return nil, fmt.Errorf("chat: instruction plugin for %q: %w", a.Name(), err)
	}

	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             a,
		SessionService:    ss,
		AutoCreateSession: true,
		PluginConfig:      runner.PluginConfig{Plugins: []*plugin.Plugin{instructionPlugin}},
	})
	if err != nil {
		return nil, fmt.Errorf("chat: runner for %q: %w", a.Name(), err)
	}
	return r, nil
}

// HumanRunner builds the channel a human talks to directly (POST /{name}/chat).
func HumanRunner(a adkagent.Agent, zc *zepclient.Client) (*runner.Runner, error) {
	return newRunner(a, zc, humanDisplayName)
}

// AvaRunner builds the channel Ava delegates into. Its inbound turns persist as
// the user "Ava"; this is the runner wrapped by NewSubAgent. Only specialists
// have an Ava channel — Ava does not delegate to itself.
func AvaRunner(a adkagent.Agent, zc *zepclient.Client) (*runner.Runner, error) {
	return newRunner(a, zc, avaDisplayName)
}
