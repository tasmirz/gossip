package coord_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	legacyMessages "github.com/tasmirz/gossip/pkg/messages"
	legacySOP "github.com/tasmirz/gossip/pkg/sop"
	legacyState "github.com/tasmirz/gossip/pkg/state"
	legacyTasks "github.com/tasmirz/gossip/pkg/tasks"
)

func TestLegacyMigrationAuditIsReadOnlyAndDetectsInconsistencies(t *testing.T) {
	dir := t.TempDir()
	state := legacyState.ProjectState{Version: "1", ProjectName: "Migrated", Agents: map[string]legacyState.AgentSession{
		"worker-1": {ID: "worker-1", Name: "Worker", Role: "implementer"},
	}}
	taskFile := legacyTasks.TasksContainer{Version: "1", Tasks: map[string]legacyTasks.Task{
		"task-1": {ID: "task-1", Title: "A", Status: legacyTasks.StatusTodo, BlockedBy: []string{"missing"}},
	}}
	stateData, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	tasksData, err := json.Marshal(taskFile)
	if err != nil {
		t.Fatal(err)
	}
	messageData := []byte("{bad json}\n")
	for name, data := range map[string][]byte{"state.json": stateData, "tasks.json": tasksData, "messages.jsonl": messageData} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	report, err := coord.AuditLegacyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Counts["agents"] != 1 || report.Counts["tasks"] != 1 || report.Counts["messages"] != 1 {
		t.Fatalf("unexpected audit summary: %+v", report)
	}
	issueCodes := map[string]bool{}
	for _, issue := range report.Issues {
		issueCodes[issue.Code] = true
	}
	for _, code := range []string{"unverified_role", "missing_dependency", "malformed_message"} {
		if !issueCodes[code] {
			t.Errorf("missing audit issue %q: %+v", code, report.Issues)
		}
	}
	for name, original := range map[string][]byte{"state.json": stateData, "tasks.json": tasksData, "messages.jsonl": messageData} {
		actual, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(original) {
			t.Fatalf("audit modified %s", name)
		}
	}
}

func TestLegacyMigrationAuditAcceptsConsistentRecordsAndFlagsCycles(t *testing.T) {
	dir := t.TempDir()
	state := legacyState.ProjectState{Version: "1", ProjectName: "Migrated", Agents: map[string]legacyState.AgentSession{}}
	tasks := legacyTasks.TasksContainer{Version: "1", Tasks: map[string]legacyTasks.Task{
		"task-1": {ID: "task-1", Title: "A", Status: legacyTasks.StatusTodo},
		"task-2": {ID: "task-2", Title: "B", Status: legacyTasks.StatusTodo, BlockedBy: []string{"task-1"}},
	}}
	message := legacyMessages.Message{ID: "message-1", From: "worker-1", To: "all", Role: "worker", Content: "history", CreatedAt: time.Now()}
	writeJSON := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON("state.json", state)
	writeJSON("tasks.json", tasks)
	messageData, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "messages.jsonl"), append(messageData, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := coord.AuditLegacyStore(dir)
	if err != nil || !report.Ready {
		t.Fatalf("consistent legacy data did not pass audit: %+v %v", report, err)
	}
	tasks.Tasks["task-1"], tasks.Tasks["task-2"] = func() (legacyTasks.Task, legacyTasks.Task) {
		a, b := tasks.Tasks["task-1"], tasks.Tasks["task-2"]
		a.BlockedBy = []string{"task-2"}
		return a, b
	}()
	// Put the reverse edge on task-2 and persist a dependency cycle.
	b := tasks.Tasks["task-2"]
	b.BlockedBy = []string{"task-1"}
	tasks.Tasks["task-2"] = b
	writeJSON("tasks.json", tasks)
	report, err = coord.AuditLegacyStore(dir)
	if err != nil || report.Ready {
		t.Fatalf("dependency cycle unexpectedly passed audit: %+v %v", report, err)
	}
	foundCycle := false
	for _, issue := range report.Issues {
		foundCycle = foundCycle || issue.Code == "dependency_cycle"
	}
	if !foundCycle {
		t.Fatalf("cycle diagnosis missing: %+v", report.Issues)
	}
}

func TestLegacyMigrationAuditHashesArtifactsAndRejectsTraversalMetadata(t *testing.T) {
	dir := t.TempDir()
	artifactsDir := filepath.Join(dir, "artifacts")
	if err := os.Mkdir(artifactsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "review.md"), []byte("review evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	sopStore := legacySOP.SOPStore{Workflows: map[string]legacySOP.SOPWorkflow{
		"wf-1": {ID: "wf-1", Artifacts: map[string]legacySOP.SOPArtifact{
			"art-1": {ID: "art-1", FilePath: filepath.Join(".local", "gossip", "artifacts", "review.md"), Content: "review evidence"},
		}},
	}}
	writeSOP := func() {
		t.Helper()
		data, err := json.Marshal(sopStore)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "sop.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSOP()
	report, err := coord.AuditLegacyStore(dir)
	if err != nil || !report.Ready || report.Counts["workflows"] != 1 || report.Counts["artifacts"] != 1 || report.Counts["sop_artifacts"] != 1 {
		t.Fatalf("artifact inventory failed: %+v %v", report, err)
	}
	if len(report.Files) != 2 || report.Files[0].SHA256 == "" || report.Files[1].SHA256 == "" {
		t.Fatalf("artifact and SOP hashes missing: %+v", report.Files)
	}
	sopStore.Workflows["wf-1"].Artifacts["art-1"] = legacySOP.SOPArtifact{ID: "art-1", FilePath: `..\..\outside.md`}
	writeSOP()
	report, err = coord.AuditLegacyStore(dir)
	if err != nil || report.Ready {
		t.Fatalf("path traversal metadata passed audit: %+v %v", report, err)
	}
}
