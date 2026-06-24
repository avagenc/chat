package zep

import (
	"context"
	"errors"
	"fmt"

	"github.com/avagenc/chat/internal/memory"
	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
)

// SessionStore implements memory.SessionStore on top of a Zep client. "thread"
// is Zep's term for what the memory domain calls a session; the translation
// stops here.
type SessionStore struct {
	client *client.Client
}

// NewSessionStore wraps a Zep client as a memory.SessionStore.
func NewSessionStore(client *client.Client) *SessionStore {
	return &SessionStore{client: client}
}

var _ memory.SessionStore = (*SessionStore)(nil)

func (s *SessionStore) Get(ctx context.Context, id string, query *memory.MessagesQuery) (*memory.MessageList, error) {
	response, err := s.client.Thread.Get(ctx, id, threadGetRequest(query))
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, memory.ErrNotFound
		}
		return nil, fmt.Errorf("get thread %q: %w", id, err)
	}
	return messageList(response), nil
}

func (s *SessionStore) Delete(ctx context.Context, id string) (*memory.SuccessResponse, error) {
	response, err := s.client.Thread.Delete(ctx, id)
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, memory.ErrNotFound
		}
		return nil, fmt.Errorf("delete thread %q: %w", id, err)
	}
	return successResponse(response), nil
}

func threadGetRequest(q *memory.MessagesQuery) *zep.ThreadGetRequest {
	if q == nil {
		return &zep.ThreadGetRequest{}
	}
	return &zep.ThreadGetRequest{
		Limit:  q.Limit,
		Cursor: q.Cursor,
		Lastn:  q.Lastn,
	}
}

func messageList(r *zep.MessageListResponse) *memory.MessageList {
	if r == nil {
		return nil
	}
	messages := make([]*memory.Message, len(r.Messages))
	for i, m := range r.Messages {
		messages[i] = message(m)
	}
	return &memory.MessageList{
		Messages:   messages,
		RowCount:   r.RowCount,
		CreatedAt:  r.ThreadCreatedAt,
		TotalCount: r.TotalCount,
		UserID:     r.UserID,
	}
}

func message(m *zep.Message) *memory.Message {
	if m == nil {
		return nil
	}
	return &memory.Message{
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
		Metadata:  m.Metadata,
		Name:      m.Name,
		Processed: m.Processed,
		Role:      string(m.Role),
		UUID:      m.UUID,
	}
}
