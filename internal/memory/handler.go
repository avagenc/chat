package memory

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	apihttp "go.naturallyfunny.dev/api/http"
	apiuser "go.naturallyfunny.dev/api/user"
	"go.naturallyfunny.dev/postera"
)

// Handler is the HTTP glue for the whole memory domain. It fronts all three
// memory types of the family: episodic (sessions) via SessionService, semantic
// (knowledge graph) via KnowledgeService, and prospective (self-addressed future
// messages) via Postera's Postarius. The episodic and semantic slices live in
// session.go and knowledge.go alongside their services; postera has no service of
// its own — its orchestrator is the external postera.Postarius — so its glue
// stays here with the spine.
type Handler struct {
	sessions  *SessionService
	knowledge *KnowledgeService
	postarius *postera.Postarius
}

func NewHandler(sessions *SessionService, knowledge *KnowledgeService, postarius *postera.Postarius) *Handler {
	return &Handler{
		sessions:  sessions,
		knowledge: knowledge,
		postarius: postarius,
	}
}

// ErrForbidden is the gateway's authorization sentinel: the caller has no
// identity, or the requested session belongs to someone else. It is a gateway
// concern, not a port one, so it lives here rather than in the public memory
// package. (The not-found sentinels, memory.ErrSessionNotFound and
// memory.ErrKnowledgeNotFound, come from the adapter.) Both SessionService and
// KnowledgeService return it, so it sits on the spine rather than in either slice.
var ErrForbidden = errors.New("forbidden")

// --- prospective memory: postera ---

func (h *Handler) ListUpcoming(w http.ResponseWriter, r *http.Request) {
	if _, err := apiuser.IDFromContext(r.Context()); err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	entries, err := h.postarius.ListUpcoming(r.Context())
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}
	apihttp.WriteJSON(w, http.StatusOK, entries)
}

func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "posterum-id")
	if id == "" {
		apihttp.WriteProblem(w, http.StatusBadRequest, map[string]any{"detail": "posterum ID required"})
		return
	}

	if _, err := apiuser.IDFromContext(r.Context()); err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, map[string]any{"detail": "forbidden"})
		return
	}

	err := h.postarius.Cancel(r.Context(), id)
	if errors.Is(err, postera.ErrNotFound) {
		apihttp.WriteProblem(w, http.StatusNotFound, map[string]any{"detail": "posterum not found"})
		return
	}
	if err != nil {
		apihttp.WriteProblem(w, http.StatusBadGateway, map[string]any{"detail": "upstream error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- shared query helpers ---

func optionalInt(value string) (*int, error) {
	if value == "" {
		return nil, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil, errors.New("not an integer")
	}
	if parsed < 0 {
		return nil, errors.New("must be non-negative")
	}

	return &parsed, nil
}

func optionalInt64(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, errors.New("not an integer")
	}
	if parsed < 0 {
		return nil, errors.New("must be non-negative")
	}

	return &parsed, nil
}
