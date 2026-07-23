package ava

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"strings"

	"github.com/avagenc/chat/internal/agent"
	"github.com/avagenc/chat/internal/wallet"
	"go.avagenc.com/ava"
	adkzep "go.naturallyfunny.dev/adk/zep"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

type subAgent struct {
	name            string
	description     string
	runner          *runner.Runner
	biller          *wallet.Biller
	kindInstruction string
	runInstruction  string
}

var _ ava.SubAgent = (*subAgent)(nil)

func NewSubAgent(a adkagent.Agent, r *runner.Runner, b *wallet.Biller, kindInstruction, runInstruction string) ava.SubAgent {
	return &subAgent{
		name:            a.Name(),
		description:     a.Description(),
		runner:          r,
		biller:          b,
		kindInstruction: kindInstruction,
		runInstruction:  runInstruction,
	}
}

func (s *subAgent) Name() string        { return s.name }
func (s *subAgent) Description() string { return s.description }

func (s *subAgent) Run(ctx context.Context, message string) (string, error) {
	userID, err := apiuser.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: user identity: %w", s.name, err)
	}
	sessID := agent.SessionID(userID)
	tz, err := apitime.ZoneFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: timezone: %w", s.name, err)
	}
	ctx = adkzep.WithTimezone(ctx, tz)
	ctx = adkzep.WithSpeaker(ctx, adkzep.Speaker{Name: "ava"})
	msg := genai.NewContentFromText(message, genai.RoleUser)
	runEvents := s.runner.Run(
		ctx,
		userID,
		sessID,
		msg,
		adkagent.RunConfig{},
		runner.WithStateDelta(map[string]any{
			agent.ChannelInstructionDeltaKey:      "",
			agent.KindSpecificInstructionDeltaKey: s.kindInstruction,
			agent.RunInstructionDeltaKey:          s.runInstruction,
		}),
	)
	// Charge on every exit path: tokens consumed before an error are spent too.
	var usage wallet.Usage
	defer func() {
		if err := s.biller.Charge(ctx, userID, wallet.Run{Agent: s.name, Session: sessID, Trigger: "ava"}, usage); err != nil {
			log.Printf("error: charge user %s for %s run: %v", userID, s.name, err)
		}
	}()
	for event, err := range runEvents {
		if err != nil {
			return "", fmt.Errorf("subagent %s: %w", s.name, err)
		}
		usage.Add(event)
		if event.IsFinalResponse() {
			// An empty final response is valid — the specialist may act without
			// anything to say back to Ava. Return it (possibly empty) instead of
			// treating a silent turn as a failure.
			var resp strings.Builder
			if event.Content != nil {
				for _, p := range event.Content.Parts {
					resp.WriteString(p.Text)
				}
			}
			return resp.String(), nil
		}
	}
	return "", fmt.Errorf("subagent %s: no final response", s.name)
}
