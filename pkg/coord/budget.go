package coord

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type BudgetReservation struct {
	TaskID string `json:"task_id"`
	Amount int64  `json:"amount"`
	Held   int64  `json:"attempt_holds"`
}
type Budget struct {
	Enabled       bool                `json:"enabled"`
	Revision      int                 `json:"revision"`
	TokenLimit    int64               `json:"token_limit"`
	Reserved      int64               `json:"reserved"`
	Reservations  []BudgetReservation `json:"reservations"`
	Usage         string              `json:"usage"`
	ReportedUsage UsageSummary        `json:"reported_usage"`
}

func readBudget(ctx context.Context, tx *sql.Tx, project string) (Budget, error) {
	reported, err := readReportedUsage(ctx, tx, project)
	if err != nil {
		return Budget{}, err
	}
	b := Budget{Usage: "agent-reported totals are self-reported; provider billing is not independently observed or enforced", ReportedUsage: reported, Reservations: []BudgetReservation{}}
	err = tx.QueryRowContext(ctx, `SELECT revision,token_limit FROM budget_envelopes WHERE project_id=?`, project).Scan(&b.Revision, &b.TokenLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	b.Enabled = true
	rows, err := tx.QueryContext(ctx, `SELECT r.task_id,r.amount,COALESCE((SELECT SUM(h.amount) FROM budget_attempt_holds h WHERE h.task_id=r.task_id),0) FROM budget_reservations r JOIN tasks t ON t.id=r.task_id WHERE t.project_id=? ORDER BY r.task_id`, project)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var r BudgetReservation
		if err = rows.Scan(&r.TaskID, &r.Amount, &r.Held); err != nil {
			return b, err
		}
		b.Reservations = append(b.Reservations, r)
		b.Reserved += max(r.Amount, r.Held)
	}
	return b, rows.Err()
}
func (s *Store) GetBudget(ctx context.Context, project, token string) (Budget, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return Budget{}, err
	}
	return readBudget(ctx, tx, project)
}

// SetBudget enables an envelope or changes its limit using optimistic revision.
func (s *Store) SetBudget(ctx context.Context, project, token string, revision int, limit int64) (Budget, error) {
	if revision < 0 || limit < 1 || limit > 1000000000000 {
		return Budget{}, errors.New("invalid budget limit or revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return Budget{}, err
	}
	b, err := readBudget(ctx, tx, project)
	if err != nil {
		return Budget{}, err
	}
	if b.Revision != revision || limit < b.Reserved {
		return Budget{}, ErrConflict
	}
	if !b.Enabled {
		var running int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=? AND status='running'`, project).Scan(&running); err != nil {
			return Budget{}, err
		}
		if running > 0 {
			return Budget{}, errors.New("cannot enable budget while tasks are running")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO budget_envelopes(project_id,revision,token_limit) VALUES(?,1,?) ON CONFLICT(project_id) DO UPDATE SET revision=budget_envelopes.revision+1,token_limit=excluded.token_limit`, project, limit)
	if err != nil {
		return Budget{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "budget.updated", project, map[string]any{"token_limit": limit}); err != nil {
		return Budget{}, err
	}
	b, err = readBudget(ctx, tx, project)
	if err != nil {
		return Budget{}, err
	}
	if err = tx.Commit(); err != nil {
		return Budget{}, err
	}
	return b, nil
}

// ReserveBudget sets one task's allocation atomically. Zero explicitly releases it.
func (s *Store) ReserveBudget(ctx context.Context, project, token, task string, amount int64) (Budget, error) {
	if task == "" || amount < 0 || amount > 1000000000000 {
		return Budget{}, errors.New("invalid reservation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return Budget{}, err
	}
	b, err := readBudget(ctx, tx, project)
	if err != nil {
		return Budget{}, err
	}
	if !b.Enabled {
		return Budget{}, errors.New("budget is not enabled")
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE project_id=? AND id=?`, project, task).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	if status == "running" {
		return Budget{}, errors.New("cannot change reservation while task is running")
	}
	if amount > 0 && (status == "accepted" || status == "cancelled") {
		return Budget{}, ErrConflict
	}
	var old, held int64
	for _, r := range b.Reservations {
		if r.TaskID == task {
			old = max(r.Amount, r.Held)
			held = r.Held
			break
		}
	}
	newTotal := max(amount, held)
	if amount == 0 {
		newTotal = 0
	}
	if b.Reserved-old+newTotal > b.TokenLimit {
		return Budget{}, ErrConflict
	}
	if amount == 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM budget_attempt_holds WHERE task_id=?`, task); err != nil {
			return Budget{}, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM budget_reservations WHERE task_id=?`, task)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_reservations(task_id,amount,updated_at) VALUES(?,?,?) ON CONFLICT(task_id) DO UPDATE SET amount=excluded.amount,updated_at=excluded.updated_at`, task, amount, time.Now().UnixMilli())
	}
	if err != nil {
		return Budget{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "budget.reserved", task, map[string]any{"amount": amount}); err != nil {
		return Budget{}, err
	}
	b, err = readBudget(ctx, tx, project)
	if err != nil {
		return Budget{}, err
	}
	if err = tx.Commit(); err != nil {
		return Budget{}, err
	}
	return b, nil
}
