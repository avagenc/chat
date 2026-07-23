package specialist

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/avagenc/chat/internal/agent"
	"github.com/avagenc/chat/internal/wallet"
	adkzep "go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

//go:embed instruction.txt
var KindInstruction string

//go:embed ran-by-ava-instruction.txt
var RanByAvaInstruction string

type Handler struct {
	runner    *runner.Runner
	biller    *wallet.Biller
	agentName string
}

func NewHandler(r *runner.Runner, b *wallet.Biller, agentName string) *Handler {
	return &Handler{runner: r, biller: b, agentName: agentName}
}

//go:embed ran-by-human-instruction.txt
var ranByHumanInstruction string

func (h *Handler) HandleHuman(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return
	}
	sessID := agent.SessionID(userID)
	tz, err := apitime.ZoneFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "timezone required"})
		return
	}
	ctx := adkzep.WithTimezone(r.Context(), tz)
	ctx = adkzep.WithSpeaker(ctx, adkzep.Speaker{Name: "human"})
	msg := genai.NewContentFromText(req.Message, genai.RoleUser)
	runEvents := h.runner.Run(
		ctx,
		userID,
		sessID,
		msg,
		adkagent.RunConfig{},
		runner.WithStateDelta(map[string]any{
			agent.ChannelInstructionDeltaKey:      "",
			agent.KindSpecificInstructionDeltaKey: KindInstruction,
			agent.RunInstructionDeltaKey:          ranByHumanInstruction,
		}),
	)
	// Charge on every exit path: tokens consumed before an error are spent too.
	var usage wallet.Usage
	defer func() {
		if err := h.biller.Charge(r.Context(), userID, wallet.Run{Agent: h.agentName, Session: sessID, Trigger: "human"}, usage); err != nil {
			log.Printf("error: charge user %s for %s run: %v", userID, h.agentName, err)
		}
	}()
	for event, err := range runEvents {
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
			return
		}
		usage.Add(event)
		if event.IsFinalResponse() {
			// An empty final response is a valid outcome (the agent acted but
			// had nothing to say). Return 200 with an empty body rather than
			// treating a silent turn as a failure; a textless response is not
			// persisted to the thread (see adk/zep).
			var resp strings.Builder
			if event.Content != nil {
				for _, p := range event.Content.Parts {
					resp.WriteString(p.Text)
				}
			}
			apihttp.WriteJSON(w, http.StatusOK, struct {
				Response string `json:"response"`
			}{resp.String()})
			return
		}
	}
	apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "no final response from agent"})
}
