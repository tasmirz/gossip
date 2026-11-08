package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CandidateReport is an unaccepted artifact from an attempt; it never changes task state.
type CandidateReport struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Attempt   int    `json:"attempt"`
	Author    string `json:"author"`
	Baseline  string `json:"baseline"`
	Body      string `json:"body,omitempty"`
	Digest    string `json:"digest"`
	CreatedAt int64  `json:"created_at"`
}

// CandidatePage returns a bounded Unicode-safe excerpt of an immutable
// candidate artifact. The digest lets a reviewer confirm every page belongs
// to the same exact source change.
type CandidatePage struct {
	ID              string `json:"id"`
	TaskID          string `json:"task_id"`
	Attempt         int    `json:"attempt"`
	Author          string `json:"author"`
	Baseline        string `json:"baseline"`
	Digest          string `json:"digest"`
	Excerpt         string `json:"excerpt"`
	Offset          int    `json:"offset"`
	TotalCharacters int    `json:"total_characters"`
	NextOffset      *int   `json:"next_offset,omitempty"`
}

var unifiedHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: .*)?$`)

func validateSourceDiff(body string) error {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	files, i := 0, 0
	for i < len(lines) {
		line := lines[i]
		if !strings.HasPrefix(line, "diff --git a/") || !strings.Contains(line, " b/") {
			i++
			continue
		}
		files++
		i++
		oldHeader, newHeader := false, false
		oldMode, newMode := false, false
		renameFrom, renameTo := false, false
		binary, changed := false, false
		for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
			line = lines[i]
			switch {
			case strings.HasPrefix(line, "--- "):
				oldHeader = true
				i++
			case strings.HasPrefix(line, "+++ "):
				newHeader = true
				i++
			case strings.HasPrefix(line, "old mode "):
				oldMode = true
				i++
			case strings.HasPrefix(line, "new mode "):
				newMode = true
				i++
			case strings.HasPrefix(line, "rename from "):
				renameFrom = true
				i++
			case strings.HasPrefix(line, "rename to "):
				renameTo = true
				i++
			case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
				binary = true
				i++
			case line == "GIT binary patch":
				binary = true
				i++
				if i >= len(lines) || !(strings.HasPrefix(lines[i], "literal ") || strings.HasPrefix(lines[i], "delta ")) {
					return errors.New("full_code review source evidence contains an incomplete binary patch")
				}
				for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					i++
				}
			case strings.HasPrefix(line, "@@"):
				match := unifiedHunkHeader.FindStringSubmatch(line)
				if match == nil || !oldHeader || !newHeader {
					return errors.New("full_code review source evidence contains a malformed unified diff hunk")
				}
				oldRemaining, err := hunkLineCount(match[2])
				if err != nil {
					return err
				}
				newRemaining, err := hunkLineCount(match[4])
				if err != nil {
					return err
				}
				i++
				for oldRemaining > 0 || newRemaining > 0 {
					if i >= len(lines) || strings.HasPrefix(lines[i], "diff --git ") {
						return errors.New("full_code review source evidence ends inside a unified diff hunk")
					}
					line = lines[i]
					if line == "\\ No newline at end of file" {
						i++
						continue
					}
					if line == "" {
						return errors.New("full_code review source evidence contains an invalid empty hunk line")
					}
					switch line[0] {
					case ' ':
						oldRemaining--
						newRemaining--
					case '-':
						oldRemaining--
						changed = true
					case '+':
						newRemaining--
						changed = true
					default:
						return errors.New("full_code review source evidence contains an invalid unified diff hunk line")
					}
					if oldRemaining < 0 || newRemaining < 0 {
						return errors.New("full_code review source evidence has hunk counts that do not match its lines")
					}
					i++
				}
			case strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "):
				changed = true
				i++
			default:
				i++
			}
		}
		if !binary && !changed && !(oldMode && newMode) && !(renameFrom && renameTo) {
			return errors.New("full_code review source evidence contains a file without a source change")
		}
	}
	if files == 0 {
		return errors.New("full_code review source evidence must be a complete unified Git diff with file and change headers")
	}
	return nil
}

func hunkLineCount(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 {
		return 0, errors.New("full_code review source evidence contains an invalid hunk line count")
	}
	return count, nil
}

func (s *Store) SaveCandidate(ctx context.Context, project, token, taskID string, attempt int, baseline, body string) (CandidateReport, error) {
	return s.SaveCandidateWithID(ctx, project, token, taskID, attempt, baseline, body, "")
}

func (s *Store) SaveCandidateWithID(ctx context.Context, project, token, taskID string, attempt int, baseline, body, requestID string) (CandidateReport, error) {
	var c CandidateReport
	if len(requestID) > 128 || !utf8.ValidString(requestID) {
		return c, errors.New("invalid request id")
	}
	if strings.TrimSpace(body) == "" || !utf8.ValidString(body) || strings.ContainsRune(body, '\x00') || len(body) > 1024*1024 || strings.TrimSpace(baseline) == "" || !utf8.ValidString(baseline) || len(baseline) > 4096 {
		return c, errors.New("candidate body and baseline required within size limits")
	}
	id, err := randomID("candidate_")
	if err != nil {
		return c, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionResultSubmit)
	if err != nil {
		return c, err
	}
	payload, err := json.Marshal([]any{taskID, attempt, baseline, body})
	if err != nil {
		return c, err
	}
	payloadDigest := sha256.Sum256(payload)
	if requestID != "" {
		var prior []byte
		err = tx.QueryRowContext(ctx, `SELECT payload_digest FROM candidate_requests WHERE project_id=? AND principal_id=? AND request_id=?`, project, actor.ID, requestID).Scan(&prior)
		if err == nil {
			if !bytes.Equal(prior, payloadDigest[:]) {
				return c, ErrConflict
			}
			err = tx.QueryRowContext(ctx, `SELECT c.id,c.task_id,c.attempt,c.author,c.baseline,c.body,c.digest,c.created_at FROM candidate_requests r JOIN candidate_reports c ON c.id=r.candidate_id WHERE r.project_id=? AND r.principal_id=? AND r.request_id=?`, project, actor.ID, requestID).Scan(&c.ID, &c.TaskID, &c.Attempt, &c.Author, &c.Baseline, &c.Body, &c.Digest, &c.CreatedAt)
			return c, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return c, err
		}
	}
	if err = validateAttemptBaseline(ctx, tx, project, taskID, attempt, actor.ID, baseline); err != nil {
		return c, err
	}
	hash := sha256.Sum256([]byte(body))
	c = CandidateReport{ID: id, TaskID: taskID, Attempt: attempt, Author: actor.ID, Baseline: baseline, Body: body, Digest: hex.EncodeToString(hash[:]), CreatedAt: time.Now().UnixMilli()}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_reports(id,task_id,attempt,author,baseline,body,digest,created_at) VALUES(?,?,?,?,?,?,?,?)`, c.ID, c.TaskID, c.Attempt, c.Author, c.Baseline, c.Body, c.Digest, c.CreatedAt)
	if err != nil {
		return CandidateReport{}, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO candidate_requests(project_id,principal_id,request_id,payload_digest,candidate_id) VALUES(?,?,?,?,?)`, project, actor.ID, requestID, payloadDigest[:], c.ID); err != nil {
			return CandidateReport{}, err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "candidate.saved", id, map[string]any{"task_id": taskID, "attempt": attempt, "digest": c.Digest}); err != nil {
		return CandidateReport{}, err
	}
	if err = tx.Commit(); err != nil {
		return CandidateReport{}, err
	}
	return c, nil
}

func (s *Store) ListCandidates(ctx context.Context, project, token, taskID string, limit int) ([]CandidateReport, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=? AND id=?`, project, taskID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.task_id,c.attempt,c.author,c.baseline,c.digest,c.created_at FROM candidate_reports c JOIN tasks t ON t.id=c.task_id WHERE t.project_id=? AND c.task_id=? ORDER BY c.created_at DESC,c.id DESC LIMIT ?`, project, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CandidateReport{}
	for rows.Next() {
		var c CandidateReport
		if err = rows.Scan(&c.ID, &c.TaskID, &c.Attempt, &c.Author, &c.Baseline, &c.Digest, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCandidate(ctx context.Context, project, token, id string) (CandidateReport, error) {
	var c CandidateReport
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return c, err
	}
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.task_id,c.attempt,c.author,c.baseline,c.body,c.digest,c.created_at FROM candidate_reports c JOIN tasks t ON t.id=c.task_id WHERE t.project_id=? AND c.id=?`, project, id).Scan(&c.ID, &c.TaskID, &c.Attempt, &c.Author, &c.Baseline, &c.Body, &c.Digest, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// GetCandidatePage reads a bounded slice without transferring the full diff.
// SQLite's length/substr count Unicode code points, matching the public cursor.
func (s *Store) GetCandidatePage(ctx context.Context, project, token, id string, offset, limit int) (CandidatePage, error) {
	var page CandidatePage
	if offset < 0 || limit < 1 || limit > 16384 {
		return page, errors.New("invalid candidate offset or limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return page, err
	}
	var probe string
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.task_id,c.attempt,c.author,c.baseline,c.digest,length(c.body),substr(c.body,?,?)
		FROM candidate_reports c JOIN tasks t ON t.id=c.task_id WHERE t.project_id=? AND c.id=?`, offset+1, limit+1, project, id).Scan(
		&page.ID, &page.TaskID, &page.Attempt, &page.Author, &page.Baseline, &page.Digest, &page.TotalCharacters, &probe)
	if errors.Is(err, sql.ErrNoRows) {
		return page, ErrNotFound
	}
	if err != nil {
		return page, err
	}
	if offset > page.TotalCharacters {
		return CandidatePage{}, errors.New("candidate offset exceeds text length")
	}
	runes := []rune(probe)
	if len(runes) > limit {
		runes = runes[:limit]
		next := offset + len(runes)
		page.NextOffset = &next
	}
	page.Excerpt = string(runes)
	page.Offset = offset
	return page, nil
}
