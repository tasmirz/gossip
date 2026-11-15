package coord

import "context"

// ConversationActivity carries only the latest incoming sequence in each visible room.
// The browser tracks which of those messages it has displayed independently of acknowledgments.
type ConversationActivity struct {
	Room     string `json:"room"`
	Sequence int64  `json:"sequence"`
}

func (s *Store) ConversationActivity(ctx context.Context, project, token string) ([]ConversationActivity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT CASE
		WHEN m.recipient IS NOT NULL THEN 'dm:' || m.sender
		WHEN m.thread_id LIKE 'channel:%' THEN m.thread_id
		ELSE 'general' END AS room, MAX(m.seq)
		FROM messages m
		WHERE m.project_id=? AND m.sender<>? AND `+visibleMessageSQL("m")+`
		GROUP BY room`, project, actor.ID, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	activity := []ConversationActivity{}
	for rows.Next() {
		var item ConversationActivity
		if err := rows.Scan(&item.Room, &item.Sequence); err != nil {
			return nil, err
		}
		activity = append(activity, item)
	}
	return activity, rows.Err()
}

func (r *Remote) ConversationActivity(ctx context.Context, project, token string) ([]ConversationActivity, error) {
	return remoteCall[[]ConversationActivity](ctx, r, token, rpcRequest{Operation: "message.activity", Project: project})
}
