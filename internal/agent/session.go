package agent

// ChatSessionID is the Zep thread ID for a user's chat. Avagenc Chat is
// single-session per user: one durable thread keyed by the Firebase UID, not a
// per-request session. Every entry point derives the same ID from the user —
// human → Ava/specialist, Ava → specialist, and the postera awaken callback —
// so they all read and write the one shared conversation. Centralized here so
// the prefix never drifts across the handlers that build it.
func ChatSessionID(userID string) string { return "chat-" + userID }
