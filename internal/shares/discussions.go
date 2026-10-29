package shares

import (
	"context"
	"sort"
)

func (s *Store) thread(ctx context.Context, u User, shareID, recipientID string) (*record, *Recipient, error) {
	r, err := s.access(ctx, u, shareID)
	if err != nil {
		return nil, nil, err
	}
	p := recipient(r, recipientID)
	if p == nil || (u.ID != r.Share.Sender.ID && u.ID != recipientID) {
		return nil, nil, ErrNotFound
	}
	return r, p, nil
}
func pageLimit(limit int) int {
	if limit <= 0 || limit > MaxPage {
		return MaxPage
	}
	return limit
}
func (s *Store) Messages(ctx context.Context, u User, shareID, recipientID string, after int64, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, _, err := s.thread(ctx, u, shareID, recipientID)
	if err != nil {
		return nil, err
	}
	out := []Message{}
	for _, m := range r.Threads[recipientID] {
		if m.ID > after {
			out = append(out, m)
			if len(out) >= pageLimit(limit) {
				break
			}
		}
	}
	return out, nil
}
func (s *Store) SendMessage(ctx context.Context, u User, shareID, recipientID, requestID, text string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, p, err := s.thread(ctx, u, shareID, recipientID)
	if err != nil {
		return Message{}, err
	}
	if !validKey(requestID) || !validText(text, false) {
		return Message{}, ErrInvalid
	}
	key := requestKey(u.ID, "message", requestID)
	digest := hash([]string{shareID, recipientID, text})
	for _, existing := range s.records {
		if old, ok := existing.Requests[key]; ok {
			if old.Hash != digest {
				return Message{}, ErrConflict
			}
			for _, m := range r.Threads[recipientID] {
				if m.ID == old.MessageID {
					return m, nil
				}
			}
			return Message{}, ErrConflict
		}
	}
	if !s.isActive(r) {
		return Message{}, ErrInactive
	}
	other := recipientID
	if other == u.ID {
		other = r.Share.Sender.ID
	}
	active, err := s.options.Active(ctx, other)
	if err != nil {
		return Message{}, err
	}
	if !active {
		return Message{}, ErrRecipientInactive
	}
	m := Message{ID: p.LastMessage + 1, Sender: u, Text: text, CreatedAt: s.now()}
	previousMessages := len(r.Threads[recipientID])
	previousEvents := len(r.Events)
	previousLast := p.LastMessage
	cursor := &p.ReadThrough
	if u.ID == r.Share.Sender.ID {
		cursor = &p.SenderReadThrough
	}
	previousCursor := *cursor
	r.Threads[recipientID] = append(r.Threads[recipientID], m)
	p.LastMessage = m.ID
	// The author has read their own message. The cursor follows it only when
	// nothing unread comes before it, so it never skips the other side's reply.
	if *cursor >= previousLast {
		*cursor = m.ID
	}
	r.Requests[key] = requestRecord{Hash: digest, MessageID: m.ID}
	s.event(r, other, recipientID, "message", m.ID)
	if err = s.persist(r); err != nil {
		r.Threads[recipientID] = r.Threads[recipientID][:previousMessages]
		r.Events = r.Events[:previousEvents]
		p.LastMessage = previousLast
		*cursor = previousCursor
		delete(r.Requests, key)
		return Message{}, err
	}
	return m, nil
}

// MarkRead is explicit: clients pass the highest message they actually rendered,
// never the latest notification cursor. Cursors are monotonic and thread scoped.
func (s *Store) MarkRead(ctx context.Context, u User, shareID, recipientID string, through int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, p, err := s.thread(ctx, u, shareID, recipientID)
	if err != nil {
		return err
	}
	if through < 0 || through > p.LastMessage {
		return ErrInvalid
	}
	cursor := &p.ReadThrough
	if u.ID == r.Share.Sender.ID {
		cursor = &p.SenderReadThrough
	}
	if through > *cursor {
		previous := *cursor
		*cursor = through
		if err = s.persist(r); err != nil {
			*cursor = previous
			return err
		}
	}
	return nil
}
func (s *Store) Notifications(ctx context.Context, u User, after int64, limit int) ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorize(ctx, u); err != nil {
		return nil, err
	}
	out := []Notification{}
	for _, r := range s.records {
		for _, e := range r.Events {
			if e.To == u.ID && e.Notification.ID > after {
				out = append(out, e.Notification)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > pageLimit(limit) {
		out = out[:pageLimit(limit)]
	}
	return out, nil
}
