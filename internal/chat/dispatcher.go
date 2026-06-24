package chat

import (
	"fmt"

	zepclient "github.com/getzep/zep-go/v3/client"
	"go.avagenc.com/ava"
	"go.avagenc.com/zee"
	tuya "go.naturallyfunny.dev/tuya"
	"google.golang.org/adk/model"
)

// Specialist descriptions are orchestration metadata — what Ava's model sees
// when deciding whether to delegate. They live with the roster (here), not in
// the specialist module, because they describe the specialist *as a teammate in
// this group chat*, including the group-chat conventions Ava must respect.
const zeeDescription = "Specialist for the user's entire Tuya smart home: lights, " +
	"curtains, switches, sensors, cameras, and any connected device — status and " +
	"control. Zee is a participant in this same group-chat session and reads the " +
	"full history, so don't repeat context already there. Do NOT prefix '@zee'; it " +
	"is added automatically. You may call with an empty message — Zee will read the " +
	"session and act. Zee has NO self-recall: never give it a scheduled/future-time " +
	"task; schedule it on your own recall and delegate a present-tense action when it fires."

// Build composes the in-process group chat: bare agents → channel runners →
// Ava's delegation wiring → the human-facing Service. This is the only place
// that knows the roster and the orchestration graph (§2.3).
//
// avaTools are Ava's own domain capabilities (self-recall, music, …), built by
// the caller from their dependencies and passed through.
func Build(llm model.LLM, zc *zepclient.Client, tuyaClient *tuya.Client) (*Service, error) {
	// 1. Specialists — bare agents from their modules.
	zeeAgent, err := zee.New(zee.Config{Model: llm, TuyaClient: tuyaClient})
	if err != nil {
		return nil, fmt.Errorf("compose zee: %w", err)
	}

	// 2. Specialist channel runners over the shared Zep client.
	zeeHuman, err := HumanRunner(zeeAgent, zc) // POST /zee/chat
	if err != nil {
		return nil, err
	}
	zeeAva, err := AvaRunner(zeeAgent, zc) // Ava delegation target
	if err != nil {
		return nil, err
	}

	// 3. Wrap specialists' Ava-channel runners as Ava's sub-agents.
	subAgents := []ava.SubAgent{
		NewSubAgent("zee", zeeDescription, zeeAva),
		// + rafal, yori once their modules adopt the bare-agent shape.
	}

	// 4. Ava — bare orchestrator agent with the roster.
	avaAgent, err := ava.New(ava.Config{Model: llm, SubAgents: subAgents})
	if err != nil {
		return nil, fmt.Errorf("compose ava: %w", err)
	}
	avaHuman, err := HumanRunner(avaAgent, zc) // POST /ava/chat
	if err != nil {
		return nil, err
	}

	// 5. Human-facing dispatch map. Ava is the orchestrator and owns its
	// group-chat behavior in its module, so no injected instruction; specialists
	// are channel-agnostic, so chat injects the human-channel framing.
	return NewService(map[string]humanChannel{
		"ava": {runner: avaHuman},
		"zee": {runner: zeeHuman, instruction: specialistGroupChatInstruction},
	}), nil
}
