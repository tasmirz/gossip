package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	legacyMessages "github.com/tasmirz/gossip/pkg/messages"
	legacySOP "github.com/tasmirz/gossip/pkg/sop"
	legacyState "github.com/tasmirz/gossip/pkg/state"
	legacyTasks "github.com/tasmirz/gossip/pkg/tasks"
)

func TestImportLegacyArchivePreservesHistoryWithoutRestoringPermissions(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	legacy := legacyState.ProjectState{
		Version: "1", ProjectName: "Old project", Goals: []legacyState.Goal{{ID: "goal-1", Title: "Ship", Description: "Historical goal", Status: "active", CreatedAt: created}},
		Agents: map[string]legacyState.AgentSession{"old-agent": {ID: "old-agent", Name: "Old agent", Role: "owner", SessionID: "never-import-this-session", RemoteEndpoint: "https://internal.invalid", RegisteredAt: created}},
	}
	taskContainer := legacyTasks.TasksContainer{Version: "1", Tasks: map[string]legacyTasks.Task{
		"task-1": {ID: "task-1", Title: "Historical task", Description: "Not an active claim", Status: legacyTasks.StatusCompleted, CreatedAt: created, AssignedTo: "agent:old-agent"},
	}}
	message := legacyMessages.Message{ID: "message-1", From: "agent:old-agent", Role: "owner", To: "all", ThreadID: "thread-1", Subject: "History", Content: "Historical discussion — বাংলা 🧭", CreatedAt: created}
	workflow := legacySOP.SOPStore{Workflows: map[string]legacySOP.SOPWorkflow{"wf-1": {ID: "wf-1", Title: "Legacy workflow", Artifacts: map[string]legacySOP.SOPArtifact{
		"artifact-1": {ID: "artifact-1", Title: "Review", Content: "Embedded review", FilePath: filepath.Join(".local", "gossip", "artifacts", "review.md"), CreatedAt: created},
	}}}}
	writeJSON := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON("state.json", legacy)
	writeJSON("tasks.json", taskContainer)
	writeJSON("sop.json", workflow)
	messageBytes, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "messages.jsonl"), append(messageBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "artifacts", "review.md"), []byte("file review"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, audit, err := s.ImportLegacyArchive(ctx, project.ID, owner.Token, source, false); err == nil || len(audit.Issues) == 0 {
		t.Fatalf("warnings were imported without explicit review: %+v %v", audit, err)
	}
	imported, audit, err := s.ImportLegacyArchive(ctx, project.ID, owner.Token, source, true)
	if err != nil {
		t.Fatalf("import archive: %+v %v", audit.Issues, err)
	}
	if imported.Existing || imported.Counts["goals"] != 1 || imported.Counts["tasks"] != 1 || imported.Counts["messages"] != 1 || imported.Counts["artifacts"] != 1 {
		t.Fatalf("incomplete import summary: %+v", imported)
	}
	imports, err := s.ListLegacyImports(ctx, project.ID, owner.Token)
	if err != nil || len(imports) != 1 || imports[0].ID != imported.ID {
		t.Fatalf("owner cannot list imported history: %+v %v", imports, err)
	}
	actors, err := s.ReadLegacyRecords(ctx, project.ID, owner.Token, imported.ID, "actor", "", 10)
	if err != nil || len(actors) != 1 {
		t.Fatalf("read historical actor: %+v %v", actors, err)
	}
	actorPayload, err := readLegacyRecord(ctx, s, project.ID, owner.Token, actors[0])
	if err != nil {
		t.Fatal(err)
	}
	actorJSON := string(actorPayload)
	if strings.Contains(actorJSON, "never-import-this-session") || strings.Contains(actorJSON, "internal.invalid") || !strings.Contains(actorJSON, "unverified_historical_actor") {
		t.Fatalf("unsafe or unauthenticated legacy identity payload: %s", actorJSON)
	}
	messages, err := s.ReadLegacyRecords(ctx, project.ID, owner.Token, imported.ID, "message", "", 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("message history missing: %+v %v", messages, err)
	}
	messagePayload, chunkErr := readLegacyRecord(ctx, s, project.ID, owner.Token, messages[0])
	if chunkErr != nil || !strings.Contains(string(messagePayload), "Historical discussion — বাংলা 🧭") {
		t.Fatalf("message history missing: %+v %v", messages, err)
	}
	archivedTasks, err := s.ReadLegacyRecords(ctx, project.ID, owner.Token, imported.ID, "task", "", 10)
	if err != nil || len(archivedTasks) != 1 {
		t.Fatalf("task history missing: %+v %v", archivedTasks, err)
	}
	taskPayload, chunkErr := readLegacyRecord(ctx, s, project.ID, owner.Token, archivedTasks[0])
	if chunkErr != nil || !strings.Contains(string(taskPayload), "completed") {
		t.Fatalf("task history missing: %+v %v", archivedTasks, err)
	}
	firstChunk, err := s.ReadLegacyRecordChunk(ctx, project.ID, owner.Token, imported.ID, "message", "message-1", 0, 9)
	if err != nil || firstChunk.Done || firstChunk.NextOffset != len([]rune(firstChunk.Content)) || firstChunk.Offset != 0 {
		t.Fatalf("invalid first history chunk: %+v %v", firstChunk, err)
	}
	if _, err = s.ReadLegacyRecordChunk(ctx, project.ID, owner.Token, imported.ID, "message", "message-1", len([]rune(string(messagePayload)))+1, 9); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("out-of-range history offset was accepted: %v", err)
	}
	activeTasks, err := s.ListTasks(ctx, project.ID, owner.Token, 20)
	if err != nil || len(activeTasks) != 0 {
		t.Fatalf("legacy task was incorrectly restored as active work: %+v %v", activeTasks, err)
	}
	if _, _, err = s.ImportLegacyArchive(ctx, project.ID, worker.Token, source, true); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-owner imported legacy data: %v", err)
	}
	repeated, _, err := s.ImportLegacyArchive(ctx, project.ID, owner.Token, source, true)
	if err != nil || !repeated.Existing || repeated.ID != imported.ID {
		t.Fatalf("same source was not idempotent: %+v %v", repeated, err)
	}
	otherProject, otherOwner, err := s.CreateProject(ctx, "another", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadLegacyRecords(ctx, otherProject.ID, otherOwner.Token, imported.ID, "message", "", 10); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("history leaked across projects: %v", err)
	}
	if _, err = s.ReadLegacyRecordChunk(ctx, otherProject.ID, otherOwner.Token, imported.ID, "message", "message-1", 0, 9); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("history chunk leaked across projects: %v", err)
	}
}

func readLegacyRecord(ctx context.Context, store *coord.Store, projectID, token string, record coord.LegacyRecord) ([]byte, error) {
	var content strings.Builder
	offset := 0
	for {
		chunk, err := store.ReadLegacyRecordChunk(ctx, projectID, token, record.ImportID, record.Type, record.ID, offset, 7)
		if err != nil {
			return nil, err
		}
		if chunk.Digest != record.Digest || chunk.Offset != offset || chunk.NextOffset-offset != len([]rune(chunk.Content)) {
			return nil, errors.New("invalid or inconsistent history chunk")
		}
		content.WriteString(chunk.Content)
		offset = chunk.NextOffset
		if chunk.Done {
			return []byte(content.String()), nil
		}
	}
}

func TestImportLegacyArchiveRejectsStructuralErrors(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tasks.json"), []byte(`{"tasks":{"bad":{"id":"bad","status":"todo","blocked_by":["missing"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, audit, err := s.ImportLegacyArchive(context.Background(), project.ID, owner.Token, source, true); err == nil || audit.Ready {
		t.Fatalf("structurally invalid source imported: %+v %v", audit, err)
	}
	imports, err := s.ListLegacyImports(context.Background(), project.ID, owner.Token)
	if err != nil || len(imports) != 0 {
		t.Fatalf("failed import left partial archive: %+v %v", imports, err)
	}
}
