package coord_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestPlanVariantsStayWithTheirSourceRevision(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, store, project, owner, nil)
	if _, err := store.CreatePlanVariant(ctx, project.ID, worker.Token, 1, "Unauthorized", "Draft"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker variant creation: %v", err)
	}
	variant, err := store.CreatePlanVariant(ctx, project.ID, owner.Token, 1, "Alternate", "An alternate approach.")
	if err != nil {
		t.Fatal(err)
	}
	if current, err := store.CurrentPlan(ctx, project.ID, owner.Token); err != nil || current.Revision != 1 {
		t.Fatalf("variant modified the active revision: %+v, %v", current, err)
	}
	if _, err := store.CreatePlan(ctx, project.ID, owner.Token, "Second plan", "Second plan", 1); err != nil {
		t.Fatal(err)
	}
	for revision, expected := range map[int]int{1: 1, 2: 0} {
		variants, err := store.PlanVariants(ctx, project.ID, worker.Token, revision)
		if err != nil || len(variants) != expected {
			t.Fatalf("revision %d variants: %+v, %v", revision, variants, err)
		}
		if expected > 0 && variants[0].ID != variant.ID {
			t.Fatalf("wrong variant: %+v", variants[0])
		}
	}
	page, err := store.SearchTasks(ctx, project.ID, worker.Token, coord.TaskFilter{Revision: 1, Limit: 20})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != task.ID {
		t.Fatalf("revision tasks: %+v, %v", page, err)
	}
	page, err = store.SearchTasks(ctx, project.ID, worker.Token, coord.TaskFilter{Revision: 2, Limit: 20})
	if err != nil || len(page.Tasks) != 0 {
		t.Fatalf("unrelated revision tasks: %+v, %v", page, err)
	}
}
