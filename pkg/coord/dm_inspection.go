package coord

import (
	"context"
	"errors"
)

type AgentDMThread struct {
	First        string `json:"first"`
	Second       string `json:"second"`
	LastSequence int64  `json:"last_sequence"`
	LastActivity int64  `json:"last_activity"`
}

func (s *Store) AgentDMThreads(ctx context.Context, project, token, participant string, before int64) ([]AgentDMThread, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, project, token, "owner"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT MIN(m.sender,m.recipient),MAX(m.sender,m.recipient),MAX(m.seq),MAX(m.created_at)
 FROM messages m JOIN principals a ON a.id=m.sender AND a.project_id=m.project_id
 JOIN principals b ON b.id=m.recipient AND b.project_id=m.project_id
 WHERE m.project_id=? AND a.role<>'owner' AND b.role<>'owner' AND a.id<>b.id
 AND (?='' OR m.sender=? OR m.recipient=?)
 GROUP BY MIN(m.sender,m.recipient),MAX(m.sender,m.recipient)
 HAVING (?=0 OR MAX(m.seq)<?) ORDER BY MAX(m.seq) DESC LIMIT 50`, project, participant, participant, participant, before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentDMThread{}
	for rows.Next() {
		var item AgentDMThread
		if err := rows.Scan(&item.First, &item.Second, &item.LastSequence, &item.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) InspectAgentDM(ctx context.Context, project, token, first, second string, before int64) ([]Message, error) {
	if first == "" || second == "" || first == second || before < 0 {
		return nil, errors.New("invalid agent conversation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return nil, err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE project_id=? AND role<>'owner' AND id IN (?,?)`, project, first, second).Scan(&count)
	if err != nil {
		return nil, err
	}
	if count != 2 {
		return nil, ErrNotFound
	}
	messages, err := queryRoomMessagesTx(ctx, tx, "", `project_id=? AND ((sender=? AND recipient=?) OR (sender=? AND recipient=?))`, []any{project, first, second, second, first}, 0, before, 50, true)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 && before == 0 {
		return nil, ErrNotFound
	}
	if err := event(ctx, tx, project, actor.ID, "dm.inspected", project, map[string]any{"first": first, "second": second, "before": before}); err != nil {
		return nil, err
	}
	return messages, tx.Commit()
}
