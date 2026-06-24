package agent

import (
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

// runInstructionKey carries per-run extra instruction. The temp: prefix scopes
// it to the current invocation only: ADK applies it to the in-memory session
// state so this run's callbacks see it, then drops it from the persisted event
// — so it never leaks into the next Run on the same session.
const runInstructionKey = "temp:chat_instruction"

// newInstructionPlugin builds the plugin that injects per-run instruction into
// the system prompt. It is registered on every runner; a Run that does not pass
// WithStateDelta(runInstructionKey, …) simply gets no extra instruction.
//
// This is how this package layers the group-chat / channel framing on top
// of an agent module that knows nothing about it: the module owns its base
// identity, this package appends the situational delta at runner.Run.
func newInstructionPlugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name:                "chat-run-instruction",
		BeforeModelCallback: injectRunInstruction,
	})
}

func injectRunInstruction(ctx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
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
