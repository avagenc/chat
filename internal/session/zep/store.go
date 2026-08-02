// Package zep implements the parent session package's Store port on top of
// Zep, mapping Zep's types and not-found errors onto session's. It lives
// beside its contract: swapping providers means writing a sibling adapter
// under internal/session, not editing session itself. (The agent side has its own Zep surface, adkzep — this package only
// covers episodic memory.)
package zep

import (
	"context"
	"errors"
	"fmt"

	"github.com/avagenc/chat/internal/session"
	zep "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
)

// Store implements session.Store on top of a Zep client. "thread" is Zep's
// term for what the session domain calls a session; the translation stops
// here.
type Store struct {
	client *client.Client
}

// NewStore wraps a Zep client as a session.Store.
func NewStore(client *client.Client) *Store {
	return &Store{client: client}
}

var _ session.Store = (*Store)(nil)

func (s *Store) Get(ctx context.Context, id string, query *session.MessagesQuery) (*session.MessageList, error) {
	response, err := s.client.Thread.Get(ctx, id, threadGetRequest(query))
	if err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return nil, session.ErrNotFound
		}
		return nil, fmt.Errorf("get thread %q: %w", id, err)
	}
	return messageList(response), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.client.Thread.Delete(ctx, id); err != nil {
		var notFound *zep.NotFoundError
		if errors.As(err, &notFound) {
			return session.ErrNotFound
		}
		return fmt.Errorf("delete thread %q: %w", id, err)
	}
	return nil
}

func threadGetRequest(q *session.MessagesQuery) *zep.ThreadGetRequest {
	if q == nil {
		return &zep.ThreadGetRequest{}
	}
	return &zep.ThreadGetRequest{
		Limit:  q.Limit,
		Cursor: q.Cursor,
		Lastn:  q.Lastn,
	}
}

func messageList(r *zep.MessageListResponse) *session.MessageList {
	if r == nil {
		return nil
	}
	messages := make([]*session.Message, len(r.Messages))
	for i, m := range r.Messages {
		messages[i] = message(m)
	}
	return &session.MessageList{
		Messages:   messages,
		RowCount:   r.RowCount,
		CreatedAt:  r.ThreadCreatedAt,
		TotalCount: r.TotalCount,
		UserID:     r.UserID,
	}
}

func message(m *zep.Message) *session.Message {
	if m == nil {
		return nil
	}
	return &session.Message{
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
		Metadata:  m.Metadata,
		Name:      m.Name,
		Processed: m.Processed,
		Role:      string(m.Role),
		UUID:      m.UUID,
	}
}
