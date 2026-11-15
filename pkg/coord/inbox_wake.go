package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// deliverInboxWake sends references only. A host can use the batch ID to
// deduplicate retries if it handled a notification before Gossip recorded it.
func (r *DeviceRegistry) deliverInboxWake(ctx context.Context, c DeviceConnection) error {
	profile := sha256.Sum256([]byte(c.Profile))
	adapter, err := os.ReadFile(filepath.Join(r.configDir, "wake", hex.EncodeToString(profile[:])+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var executable string
	if json.Unmarshal(adapter, &executable) != nil || !filepath.IsAbs(executable) {
		return errors.New("wake adapter must be a JSON string containing an absolute executable path")
	}
	key := []any{c.Profile, c.HomeID, c.ProjectID, c.PrincipalID}
	payload, err := r.pendingWakeBatch(ctx, c, key)
	if err != nil || payload == nil {
		return err
	}
	var batch wakeBatch
	if err := json.Unmarshal(payload, &batch); err != nil {
		return err
	}
	run, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(run, executable)
	cmd.Stdin = bytes.NewReader(payload)
	if err := cmd.Run(); err != nil {
		return errors.New("wake adapter failed")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range batch.Items {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO service_inbox_wakes SELECT profile,home,project,principal,item FROM service_inbox WHERE profile=? AND home=? AND project=? AND principal=? AND item=?`, append(key, id)...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_wake_batches WHERE profile=? AND home=? AND project=? AND principal=? AND payload=?`, append(key, payload)...); err != nil {
		return err
	}
	return tx.Commit()
}

type wakeBatch struct {
	Profile   string   `json:"profile"`
	Home      string   `json:"home_id"`
	Project   string   `json:"project_id"`
	Principal string   `json:"principal_id"`
	Items     []string `json:"item_ids"`
	ID        string   `json:"batch_id"`
}

func (r *DeviceRegistry) pendingWakeBatch(ctx context.Context, c DeviceConnection, key []any) ([]byte, error) {
	var pending []byte
	err := r.db.QueryRowContext(ctx, `SELECT payload FROM service_wake_batches WHERE profile=? AND home=? AND project=? AND principal=?`, key...).Scan(&pending)
	if !errors.Is(err, sql.ErrNoRows) {
		return pending, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT item FROM service_inbox i WHERE profile=? AND home=? AND project=? AND principal=? AND item<>'' AND payload IS NOT NULL AND NOT EXISTS(SELECT 1 FROM service_inbox_wakes w WHERE w.profile=i.profile AND w.home=i.home AND w.project=i.project AND w.principal=i.principal AND w.item=i.item) ORDER BY item LIMIT 100`, key...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	batch := wakeBatch{Profile: c.Profile, Home: c.HomeID, Project: c.ProjectID, Principal: c.PrincipalID, Items: items}
	payload, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	batch.ID = hex.EncodeToString(digest[:])
	payload, err = json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	_, err = r.db.ExecContext(ctx, `INSERT OR IGNORE INTO service_wake_batches SELECT profile,home,project,principal,? FROM service_inbox WHERE profile=? AND home=? AND project=? AND principal=? AND item=''`, append([]any{payload}, key...)...)
	if err != nil {
		return nil, err
	}
	err = r.db.QueryRowContext(ctx, `SELECT payload FROM service_wake_batches WHERE profile=? AND home=? AND project=? AND principal=?`, key...).Scan(&payload)
	return payload, err
}
