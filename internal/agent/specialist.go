package agent

import (
	"encoding/json"
	"net/http"

	apihttp "go.naturallyfunny.dev/api/http"
	"google.golang.org/adk/runner"
)

// SpecialistHandler serves one specialist addressed directly by a human (no Ava
// hop) on its own explicit route, e.g. POST /zee. It runs that specialist's
// runner with the human as the inbound speaker, framed by ranByHuman. One handler
// per specialist: the route names the specialist, not a dispatch map.
type SpecialistHandler struct {
	runner *runner.Runner
}

func NewSpecialistHandler(r *runner.Runner) *SpecialistHandler {
	return &SpecialistHandler{runner: r}
}

// HandleHuman handles the specialist's human channel (e.g. POST /zee). The
// frontend already resolved any @mention into the route, so this does not parse
// the human's message.
func (h *SpecialistHandler) HandleHuman(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}
	userID, sessionID, ok := chatIdentity(w, r.Context())
	if !ok {
		return
	}
	respondRun(w, runContext(r.Context(), humanSpeaker), h.runner,
		userID, sessionID, req.Message, ranByHuman)
}
