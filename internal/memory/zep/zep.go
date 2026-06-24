// Package zep is the Zep implementation of the memory domain's ports. It is the
// single place in the codebase that imports the Zep module: SessionStore (in
// session.go) implements memory.SessionStore and KnowledgeStore (in
// knowledge.go) implements memory.KnowledgeStore, mapping our domain types to
// and from Zep's and translating Zep's not-found error into memory.ErrNotFound.
// Swapping providers means writing a sibling package, not editing memory.
package zep

import (
	"github.com/avagenc/chat/internal/memory"
	zep "github.com/getzep/zep-go/v3"
)

// successResponse maps Zep's success body to the memory domain's.
func successResponse(r *zep.SuccessResponse) *memory.SuccessResponse {
	if r == nil {
		return nil
	}
	return &memory.SuccessResponse{Message: r.Message}
}
