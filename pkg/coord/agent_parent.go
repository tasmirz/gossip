package coord

import (
	"context"
	"errors"
	"strings"
	"time"
)

// SetAgentParent changes the explicit presentation hierarchy for one project.
// Parentage grants no permissions and never merges agent identities.
func (s *Store) SetAgentParent(ctx context.Context, project, token, childID, parentID string) error {
	childID, parentID = strings.TrimSpace(childID), strings.TrimSpace(parentID)
	if childID == "" || childID == parentID {
		return errors.New("child and parent must be different project members")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE project_id=? AND id IN (?,?) AND revoked=0`, project, childID, parentID).Scan(&count)
	if err != nil {
		return err
	}
	want := 2
	if parentID == "" {
		want = 1
	}
	if count != want {
		return ErrNotFound
	}
	if parentID == "" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM agent_parent_links WHERE project_id=? AND child_id=?`, project, childID); err != nil {
			return err
		}
	} else {
		var cycle bool
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (
 SELECT child_id FROM agent_parent_links WHERE project_id=? AND parent_id=?
 UNION ALL
 SELECT links.child_id FROM agent_parent_links links JOIN descendants d ON links.parent_id=d.id WHERE links.project_id=?
) SELECT EXISTS(SELECT 1 FROM descendants WHERE id=?)`, project, childID, project, parentID).Scan(&cycle)
		if err != nil {
			return err
		}
		if cycle {
			return errors.New("parent link would create a cycle")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_parent_links(project_id,child_id,parent_id,linked_by,linked_at) VALUES(?,?,?,?,?)
 ON CONFLICT(project_id,child_id) DO UPDATE SET parent_id=excluded.parent_id,linked_by=excluded.linked_by,linked_at=excluded.linked_at`, project, childID, parentID, actor.ID, time.Now().UnixMilli())
		if err != nil {
			return err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "agent.parent.changed", childID, map[string]string{"parent_id": parentID}); err != nil {
		return err
	}
	return tx.Commit()
}
