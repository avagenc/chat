package agent

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	adkzep "go.naturallyfunny.dev/adk/zep"
	apihttp "go.naturallyfunny.dev/api/http"
	apisess "go.naturallyfunny.dev/api/session"
	apitime "go.naturallyfunny.dev/api/time"
	apiuser "go.naturallyfunny.dev/api/user"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

type AvaHandler struct {
	runner *runner.Runner
}

func NewAvaHandler(r *runner.Runner) *AvaHandler { return &AvaHandler{runner: r} }

func (h *AvaHandler) HandleHuman(w http.ResponseWriter, r *http.Request) {
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
	ctx := adkzep.WithSpeakerName(r.Context(), "human")
	if tz, err := apitime.ZoneFromContext(r.Context()); err == nil && tz != "" {
		ctx = adkzep.WithTimezone(ctx, tz)
	}
	msg := genai.NewContentFromText(req.Message, genai.RoleUser)
	runEvents := h.runner.Run(ctx, userID, sessID, msg, adkagent.RunConfig{})
	for event, err := range runEvents {
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
			return
		}
		if event.IsFinalResponse() && event.Content != nil {
			var resp strings.Builder
			for _, p := range event.Content.Parts {
				resp.WriteString(p.Text)
			}
			apihttp.WriteJSON(w, http.StatusOK, struct {
				Response string `json:"response"`
			}{resp.String()})
			return
		}
	}
	apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "no final response from agent"})
}

//go:embed specialist-ran-by-postera-instruction.txt
var specialistRanByPosteraInstruction string

func (h *AvaHandler) HandleSelfAwaken(w http.ResponseWriter, r *http.Request) {
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
	ctx := adkzep.WithSpeakerName(r.Context(), "ava")
	if tz, err := apitime.ZoneFromContext(r.Context()); err == nil && tz != "" {
		ctx = adkzep.WithTimezone(ctx, tz)
	}
	msg := genai.NewContentFromText(message, genai.RoleUser)
	runEvents := h.runner.Run(
		ctx,
		userID,
		sessID,
		msg,
		adkagent.RunConfig{},
		runner.WithStateDelta(map[string]any{RunInstructionDeltaKey: specialistRanByPosteraInstruction}),
	)
	for event, err := range runEvents {
		if err != nil {
			apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
			return
		}
		if event.IsFinalResponse() && event.Content != nil {
			var resp strings.Builder
			for _, p := range event.Content.Parts {
				resp.WriteString(p.Text)
			}
			apihttp.WriteJSON(w, http.StatusOK, struct {
				Response string `json:"response"`
			}{resp.String()})
			return
		}
	}
	apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "no final response from agent"})
}
