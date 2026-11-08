package coord_test

import (
	"context"
	"errors"
	"github.com/tasmirz/gossip/pkg/coord"
	"strings"
	"testing"
	"time"
)

func TestSearchTasksPaginationAndFilters(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		create(t, s, p, owner, nil)
	}
	page, err := s.SearchTasks(ctx, p.ID, owner.Token, coord.TaskFilter{Limit: 2})
	if err != nil || len(page.Tasks) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	seen := map[string]bool{}
	for _, task := range page.Tasks {
		seen[task.ID] = true
	}
	cursor := page.NextCursor
	create(t, s, p, owner, nil) // New work must not shift a cursor or duplicate results.
	for cursor != "" {
		page, err = s.SearchTasks(ctx, p.ID, owner.Token, coord.TaskFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page.Tasks {
			if seen[task.ID] {
				t.Fatal("duplicate across pages")
			}
			seen[task.ID] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 7 {
		t.Fatalf("expected seven original tasks, got %d", len(seen))
	}
	task := create(t, s, p, owner, nil)
	if _, err = s.AssignTask(ctx, p.ID, owner.Token, task.ID, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	page, err = s.SearchTasks(ctx, p.ID, worker.Token, coord.TaskFilter{Status: "running", Assignee: worker.PrincipalID, Kind: "research", Limit: 10})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != task.ID {
		t.Fatalf("filtered: %+v %v", page, err)
	}
	counts, err := s.TaskCounts(ctx, p.ID, worker.Token)
	if err != nil || counts["running"] != 1 || counts["ready"] != 8 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	p2, cred, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SearchTasks(ctx, p2.ID, cred.Token, coord.TaskFilter{Cursor: task.ID, Limit: 2}); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("cross-project cursor: %v", err)
	}
	if _, err = s.SearchTasks(ctx, p.ID, cred.Token, coord.TaskFilter{Limit: 2}); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("cross-project token: %v", err)
	}
}

func TestSearchTasksTextQueryIsBoundedAndProjectScoped(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "Review WAL checkpoint", "Explain replay after restart", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchTasks(ctx, project.ID, owner.Token, coord.TaskFilter{Query: "wal CHECKPOINT", Limit: 10})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != task.ID {
		t.Fatalf("case-insensitive title search: %+v %v", page, err)
	}
	page, err = s.SearchTasks(ctx, project.ID, owner.Token, coord.TaskFilter{Query: "replay after restart", Limit: 10})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != task.ID {
		t.Fatalf("description search: %+v %v", page, err)
	}
	page, err = s.SearchTasks(ctx, project.ID, owner.Token, coord.TaskFilter{Query: "not present", Limit: 10})
	if err != nil || len(page.Tasks) != 0 {
		t.Fatalf("empty search: %+v %v", page, err)
	}
	if _, err = s.SearchTasks(ctx, project.ID, owner.Token, coord.TaskFilter{Query: strings.Repeat("x", 257), Limit: 10}); err == nil {
		t.Fatal("oversized search query was accepted")
	}
}
