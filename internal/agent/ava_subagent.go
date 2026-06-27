package agent

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"go.avagenc.com/ava"
	adkzep "go.naturallyfunny.dev/adk/zep"
	apisess "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

type avaSubAgent struct {
	name        string
	description string
	runner      *runner.Runner
}

var _ ava.SubAgent = (*avaSubAgent)(nil)

func ToAvaSubAgent(a adkagent.Agent, r *runner.Runner) ava.SubAgent {
	return &avaSubAgent{name: a.Name(), description: a.Description(), runner: r}
}

func (s *avaSubAgent) Name() string        { return s.name }
func (s *avaSubAgent) Description() string { return s.description }

//go:embed specialist-ran-by-ava-instruction.txt
var specialistRanByAvaInstruction string

func (s *avaSubAgent) Run(ctx context.Context, message string) (string, error) {
	userID, err := apiuser.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: user identity: %w", s.name, err)
	}
	sessID, err := apisess.IDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: session identity: %w", s.name, err)
	}
	tz, err := apitime.ZoneFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("subagent %s: timezone: %w", s.name, err)
	}
	ctx = adkzep.WithTimezone(ctx, tz)
	ctx = adkzep.WithSpeakerName(ctx, "ava")
	msg := genai.NewContentFromText(message, genai.RoleUser)
	runEvents := s.runner.Run(
		ctx,
		userID,
		sessID,
		msg,
		adkagent.RunConfig{},
		runner.WithStateDelta(map[string]any{RunInstructionDeltaKey: specialistRanByAvaInstruction}),
	)
	for event, err := range runEvents {
		if err != nil {
			return "", fmt.Errorf("subagent %s: %w", s.name, err)
		}
		if event.IsFinalResponse() && event.Content != nil {
			var resp strings.Builder
			for _, p := range event.Content.Parts {
				resp.WriteString(p.Text)
			}
			return resp.String(), nil
		}
	}
	return "", fmt.Errorf("subagent %s: no final response", s.name)
}
