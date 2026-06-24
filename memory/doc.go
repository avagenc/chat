// Package memory defines the gateway's memory domain as pure ports and types. It
// spans two members of the same family: episodic memory — a user's sessions and
// their messages (SessionStore, in session.go) — and semantic memory — a user's
// knowledge graph (KnowledgeStore, in knowledge.go). Everything here is
// provider-agnostic, so this package imports nothing internal and can be imported
// freely by both adapters and consumers.
//
// It is deliberately public (not under internal/) so an adapter such as
// internal/zep can implement these ports without one internal package importing
// another. The Zep implementation lives in internal/zep; the HTTP handler and
// services that drive these ports live in internal/memory. Swapping providers
// means writing a sibling adapter, not touching this package.
//
// This file (doc.go) holds the package comment plus ErrNotFound, the one
// declaration that is cross-cutting — used by both ports and belonging to neither
// sub-file alone.
package memory

import "errors"

// ErrNotFound is the port-level sentinel an adapter returns when the backend has
// no such session, user, or graph. Services and handlers branch on it via
// errors.Is to map it to a 404.
var ErrNotFound = errors.New("not found")
