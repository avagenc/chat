package ava

import (
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/avagenc/chat/internal/agent"
	"github.com/avagenc/chat/internal/wallet"
	zep "github.com/getzep/zep-go/v3"
	adkzep "go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apisess "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

//go:embed instruction.txt
var kindInstruction string

type Handler struct {
	runner *runner.Runner
	biller *wallet.Biller
}

func NewHandler(r *runner.Runner, b *wallet.Biller) *Handler {
	return &Handler{runner: r, biller: b}
}

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
	sessID, err := apisess.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}
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
			agent.KindSpecificInstructionDeltaKey: kindInstruction,
			agent.RunInstructionDeltaKey:          "",
		}),
	)
	var usage wallet.Usage
	defer func() {
		if err := h.biller.Charge(r.Context(), userID, wallet.Run{Agent: "ava", Session: sessID, Trigger: "human"}, usage); err != nil {
			log.Printf("error: charge user %s for ava run: %v", userID, err)
		}
	}()
	for event, err := range runEvents {
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
			return
		}
		usage.Add(event)
		if event.IsFinalResponse() {
			var resp strings.Builder
			if event.Content != nil {
				for _, p := range event.Content.Parts {
					resp.WriteString(p.Text)
				}
			}
			apihttp.WriteJSON(w, http.StatusOK, struct {
				Response string `json:"response"`
			}{
				resp.String(),
			})
			return
		}
	}
	apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "no final response from agent"})
}

//go:embed ava-ran-by-postera-instruction.txt
var avaRanByPosteraInstruction string

func (h *Handler) HandleSelfAwaken(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "failed to read body"})
		return
	}
	message := strings.TrimSpace(string(raw))
	if message == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "empty recall body"})
		return
	}
	userID, err := apiuser.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusUnauthorized, map[string]any{"detail": "missing user identity"})
		return
	}
	sessID, err := apisess.IDFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "session ID required"})
		return
	}
	tz, err := apitime.ZoneFromContext(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "timezone required"})
		return
	}
	ctx := adkzep.WithTimezone(r.Context(), tz)
	ctx = adkzep.WithSpeaker(ctx, adkzep.Speaker{Name: "ava", Role: zep.RoleTypeSystemRole})
	msg := genai.NewContentFromText(message, genai.RoleUser)
	runEvents := h.runner.Run(
		ctx,
		userID,
		sessID,
		msg,
		adkagent.RunConfig{},
		runner.WithStateDelta(map[string]any{
			agent.KindSpecificInstructionDeltaKey: kindInstruction,
			agent.RunInstructionDeltaKey:          avaRanByPosteraInstruction,
		}),
	)
	var usage wallet.Usage
	defer func() {
		if err := h.biller.Charge(r.Context(), userID, wallet.Run{Agent: "ava", Session: sessID, Trigger: "postera"}, usage); err != nil {
			log.Printf("error: charge user %s for ava run: %v", userID, err)
		}
	}()
	for event, err := range runEvents {
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
			return
		}
		usage.Add(event)
		if event.IsFinalResponse() {
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
