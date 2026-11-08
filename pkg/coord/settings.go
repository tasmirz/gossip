package coord

import (
	"context"
	"database/sql"
	"errors"
)

type Settings struct {
	Revision           int    `json:"revision"`
	MaxParallel        int    `json:"max_parallel"`
	MaxLeaseSeconds    int    `json:"max_lease_seconds"`
	SummaryTokenTarget int    `json:"summary_token_target"`
	DefaultReviewMode  string `json:"default_review_mode"`
}

func readSettings(ctx context.Context, tx *sql.Tx, project string) (Settings, error) {
	var s Settings
	err := tx.QueryRowContext(ctx, `SELECT revision,max_parallel,max_lease_seconds,summary_token_target,default_review_mode FROM project_settings WHERE project_id=?`, project).Scan(&s.Revision, &s.MaxParallel, &s.MaxLeaseSeconds, &s.SummaryTokenTarget, &s.DefaultReviewMode)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}
func (s *Store) GetSettings(ctx context.Context, project, token string) (Settings, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return Settings{}, err
	}
	return readSettings(ctx, tx, project)
}
func (s *Store) UpdateSettings(ctx context.Context, project, token string, settings Settings) (Settings, error) {
	if settings.MaxParallel < 1 || settings.MaxParallel > 128 || settings.MaxLeaseSeconds < 1 || settings.MaxLeaseSeconds > 3600 || settings.SummaryTokenTarget < 1 || settings.SummaryTokenTarget > 100000 {
		return Settings{}, errors.New("invalid project limits")
	}
	switch settings.DefaultReviewMode {
	case "summary", "report_first", "targeted_source", "full_source":
	default:
		return Settings{}, errors.New("invalid review mode")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionSettingsEdit)
	if err != nil {
		return Settings{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE project_settings SET revision=revision+1,max_parallel=?,max_lease_seconds=?,summary_token_target=?,default_review_mode=? WHERE project_id=? AND revision=?`, settings.MaxParallel, settings.MaxLeaseSeconds, settings.SummaryTokenTarget, settings.DefaultReviewMode, project, settings.Revision)
	if err != nil {
		return Settings{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Settings{}, err
	}
	if n != 1 {
		return Settings{}, ErrConflict
	}
	settings.Revision++
	if err = event(ctx, tx, project, actor.ID, "settings.updated", project, settings); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}
