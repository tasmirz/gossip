package coord

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	legacyMessages "github.com/tasmirz/gossip/pkg/messages"
	legacySOP "github.com/tasmirz/gossip/pkg/sop"
	legacyState "github.com/tasmirz/gossip/pkg/state"
	legacyTasks "github.com/tasmirz/gossip/pkg/tasks"
)

const maxLegacyJSONBytes = 32 << 20
const maxLegacyMessageBytes = 256 << 20
const maxLegacyMessageLine = 8 << 20
const maxLegacyArtifactBytes = 32 << 20
const maxLegacyArtifactTotalBytes = 512 << 20
const maxLegacyArtifactFiles = 10000

type MigrationIssue struct {
	File     string `json:"file"`
	Record   string `json:"record,omitempty"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type MigrationFile struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	Records int    `json:"records"`
}

// MigrationAudit is a read-only assessment. It never creates or modifies a
// coordination database, legacy file, credential, or snapshot.
type MigrationAudit struct {
	SourceDir string           `json:"source_dir"`
	Files     []MigrationFile  `json:"files"`
	Counts    map[string]int   `json:"counts"`
	Issues    []MigrationIssue `json:"issues"`
	Ready     bool             `json:"ready_for_import"`
}

func (a *MigrationAudit) issue(file, record, severity, code, message string) {
	a.Issues = append(a.Issues, MigrationIssue{File: file, Record: record, Severity: severity, Code: code, Message: message})
}

func (a *MigrationAudit) addFile(name string, data []byte, records int) {
	sum := sha256.Sum256(data)
	a.Files = append(a.Files, MigrationFile{Name: name, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Records: records})
}

// AuditLegacyStore validates the legacy JSON stores before a future import.
// It intentionally treats old actor names and role labels as historical data,
// not as identities or permissions in the new project system.
func AuditLegacyStore(source string) (MigrationAudit, error) {
	a := MigrationAudit{Counts: map[string]int{}, Issues: []MigrationIssue{}, Files: []MigrationFile{}}
	if strings.TrimSpace(source) == "" {
		return a, errors.New("legacy source directory is required")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return a, err
	}
	rootInfo, err := os.Lstat(absolute)
	if err != nil {
		return a, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return a, errors.New("legacy source must be a real directory, not a symlink")
	}
	a.SourceDir = absolute
	legacyActors := map[string]bool{"orchestrator": true}

	stateFile, statePresent := readLegacyFile(absolute, "state.json", maxLegacyJSONBytes, &a)
	var oldState legacyState.ProjectState
	if statePresent {
		if err := json.Unmarshal(stateFile, &oldState); err != nil {
			a.issue("state.json", "", "error", "invalid_json", err.Error())
		} else {
			a.Counts["goals"] = len(oldState.Goals)
			a.Counts["agents"] = len(oldState.Agents)
			for id, agent := range oldState.Agents {
				if id == "" || agent.ID == "" {
					a.issue("state.json", id, "error", "missing_actor_id", "agent record has no stable ID")
				} else if id != agent.ID {
					a.issue("state.json", id, "warning", "actor_id_mismatch", "map key and embedded agent ID differ; import needs an explicit mapping")
				}
				legacyActors[strings.ToLower(id)] = true
				if agent.ID != "" {
					legacyActors[strings.ToLower(agent.ID)] = true
				}
				if agent.Role != "" {
					a.issue("state.json", id, "warning", "unverified_role", "legacy role is historical metadata and will not grant permissions")
				}
			}
			seenGoals := map[string]bool{}
			for _, goal := range oldState.Goals {
				if strings.TrimSpace(goal.ID) == "" {
					a.issue("state.json", "", "error", "missing_goal_id", "goal record has no stable ID")
				} else if seenGoals[goal.ID] {
					a.issue("state.json", goal.ID, "error", "duplicate_goal_id", "goal ID occurs more than once")
				}
				seenGoals[goal.ID] = true
				switch goal.Status {
				case "active", "completed", "archived":
				default:
					a.issue("state.json", goal.ID, "warning", "unknown_goal_status", fmt.Sprintf("goal status %q needs explicit mapping", goal.Status))
				}
			}
			if strings.TrimSpace(oldState.ProjectName) == "" {
				a.issue("state.json", "", "warning", "missing_project_name", "project name must be supplied before import")
			}
		}
	}

	taskFile, tasksPresent := readLegacyFile(absolute, "tasks.json", maxLegacyJSONBytes, &a)
	var oldTasks legacyTasks.TasksContainer
	if tasksPresent {
		if err := json.Unmarshal(taskFile, &oldTasks); err != nil {
			a.issue("tasks.json", "", "error", "invalid_json", err.Error())
		} else {
			a.Counts["tasks"] = len(oldTasks.Tasks)
			validateLegacyTasks(&a, oldTasks.Tasks, legacyActors)
			for _, goal := range oldState.Goals {
				for _, milestone := range goal.Milestones {
					for _, taskID := range milestone.TaskIDs {
						if _, ok := oldTasks.Tasks[taskID]; !ok {
							a.issue("state.json", goal.ID+"/"+milestone.ID, "error", "missing_milestone_task", fmt.Sprintf("milestone references missing task %q", taskID))
						}
					}
				}
			}
		}
	}
	if statePresent && !tasksPresent {
		for _, goal := range oldState.Goals {
			for _, milestone := range goal.Milestones {
				for _, taskID := range milestone.TaskIDs {
					a.issue("state.json", goal.ID+"/"+milestone.ID, "error", "missing_milestone_task", fmt.Sprintf("milestone references %q but tasks.json is absent", taskID))
				}
			}
		}
	}

	messageFile, messagesPresent := readLegacyFile(absolute, "messages.jsonl", maxLegacyMessageBytes, &a)
	if messagesPresent {
		validateLegacyMessages(&a, messageFile, legacyActors)
	}

	artifactPaths := auditLegacyArtifacts(absolute, &a)
	sopFile, sopPresent := readLegacyFile(absolute, "sop.json", maxLegacyJSONBytes, &a)
	if sopPresent {
		var oldSOP legacySOP.SOPStore
		if err := json.Unmarshal(sopFile, &oldSOP); err != nil {
			a.issue("sop.json", "", "error", "invalid_json", err.Error())
		} else {
			a.Counts["workflows"] = len(oldSOP.Workflows)
			for id, workflow := range oldSOP.Workflows {
				if workflow.ID == "" || workflow.ID != id {
					a.issue("sop.json", id, "warning", "workflow_id_mismatch", "workflow map key and embedded ID differ; import needs an explicit mapping")
				}
				for artifactID, artifact := range workflow.Artifacts {
					a.Counts["sop_artifacts"]++
					if artifact.ID == "" || artifact.ID != artifactID {
						a.issue("sop.json", id+"/"+artifactID, "warning", "artifact_id_mismatch", "artifact map key and embedded ID differ")
					}
					storedPath := path.Clean(strings.ReplaceAll(artifact.FilePath, "\\", "/"))
					if path.IsAbs(storedPath) || storedPath == ".." || strings.HasPrefix(storedPath, "../") {
						a.issue("sop.json", id+"/"+artifactID, "error", "unsafe_artifact_path", "artifact metadata contains a path outside the legacy store")
					}
					file := path.Base(storedPath)
					if file == "." || file == "/" || !artifactPaths[file] {
						a.issue("sop.json", id+"/"+artifactID, "warning", "missing_artifact_file", "artifact record has no matching file under artifacts/; embedded content remains available")
					}
				}
			}
		}
	}
	vaultIndex := filepath.Join(absolute, "vault", "index.json")
	if info, err := os.Lstat(vaultIndex); err == nil && info.Mode().IsRegular() {
		a.issue("vault/index.json", "", "warning", "vault_excluded", "vault entries are not included in migration; review and transfer secrets through a separate approved process")
	}
	hasLegacyData := statePresent || tasksPresent || messagesPresent || sopPresent || len(artifactPaths) > 0
	if !hasLegacyData {
		a.issue("", "", "error", "no_legacy_data", "no recognized legacy state, task, message, workflow, or artifact data found")
	} else if !statePresent {
		a.issue("state.json", "", "warning", "missing_identity_registry", "project and actor identity metadata are absent; supply an explicit mapping before import")
	}
	sort.SliceStable(a.Issues, func(i, j int) bool {
		if a.Issues[i].File != a.Issues[j].File {
			return a.Issues[i].File < a.Issues[j].File
		}
		if a.Issues[i].Record != a.Issues[j].Record {
			return a.Issues[i].Record < a.Issues[j].Record
		}
		return a.Issues[i].Code < a.Issues[j].Code
	})
	sort.SliceStable(a.Files, func(i, j int) bool { return a.Files[i].Name < a.Files[j].Name })
	a.Ready = true
	for _, issue := range a.Issues {
		if issue.Severity == "error" {
			a.Ready = false
			break
		}
	}
	return a, nil
}

func auditLegacyArtifacts(root string, audit *MigrationAudit) map[string]bool {
	dir := filepath.Join(root, "artifacts")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}
	}
	if err != nil {
		audit.issue("artifacts", "", "error", "read_failed", err.Error())
		return map[string]bool{}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		audit.issue("artifacts", "", "error", "unsafe_directory", "expected a real directory; symlinks and files are not traversed")
		return map[string]bool{}
	}
	filesByName := map[string]bool{}
	var total int64
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			audit.issue("artifacts", path, "error", "read_failed", walkErr.Error())
			return nil
		}
		if path == dir || entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			audit.issue("artifacts", path, "error", "unsafe_path", relErr.Error())
			return nil
		}
		record := filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			audit.issue(record, "", "error", "unsafe_file", "artifact symlinks are not followed")
			return nil
		}
		fileInfo, statErr := entry.Info()
		if statErr != nil {
			audit.issue(record, "", "error", "read_failed", statErr.Error())
			return nil
		}
		if !fileInfo.Mode().IsRegular() {
			audit.issue(record, "", "error", "unsafe_file", "artifact is not a regular file")
			return nil
		}
		if audit.Counts["artifacts"] >= maxLegacyArtifactFiles {
			audit.issue("artifacts", "", "error", "too_many_artifacts", fmt.Sprintf("artifact count exceeds limit %d", maxLegacyArtifactFiles))
			return filepath.SkipAll
		}
		if fileInfo.Size() > maxLegacyArtifactBytes {
			audit.issue(record, "", "error", "file_too_large", fmt.Sprintf("artifact exceeds %d bytes", maxLegacyArtifactBytes))
			return nil
		}
		if total+fileInfo.Size() > maxLegacyArtifactTotalBytes {
			audit.issue("artifacts", "", "error", "artifact_data_too_large", fmt.Sprintf("total artifact bytes exceed %d", maxLegacyArtifactTotalBytes))
			return filepath.SkipAll
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			audit.issue(record, "", "error", "read_failed", openErr.Error())
			return nil
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(f, maxLegacyArtifactBytes+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n != fileInfo.Size() {
			err := copyErr
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = errors.New("artifact changed while it was being read")
			}
			audit.issue(record, "", "error", "read_failed", err.Error())
			return nil
		}
		audit.Files = append(audit.Files, MigrationFile{Name: record, Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil)), Records: 1})
		audit.Counts["artifacts"]++
		total += n
		if filepath.Dir(relative) == "artifacts" {
			filesByName[filepath.Base(relative)] = true
		}
		return nil
	})
	if err != nil {
		audit.issue("artifacts", "", "error", "walk_failed", err.Error())
	}
	return filesByName
}

func readLegacyFile(root, name string, limit int64, audit *MigrationAudit) ([]byte, bool) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		audit.issue(name, "", "error", "read_failed", err.Error())
		return nil, false
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		audit.issue(name, "", "error", "unsafe_file", "expected a regular file; symlinks and special files are not read")
		return nil, true
	}
	if info.Size() > limit {
		audit.issue(name, "", "error", "file_too_large", fmt.Sprintf("file is %d bytes; limit is %d", info.Size(), limit))
		return nil, true
	}
	f, err := os.Open(path)
	if err != nil {
		audit.issue(name, "", "error", "read_failed", err.Error())
		return nil, true
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		audit.issue(name, "", "error", "read_failed", err.Error())
		return nil, true
	}
	if int64(len(data)) > limit {
		audit.issue(name, "", "error", "file_too_large", fmt.Sprintf("file exceeds %d bytes", limit))
		return nil, true
	}
	if name == "messages.jsonl" {
		audit.addFile(name, data, 0)
	} else {
		audit.addFile(name, data, 1)
	}
	return data, true
}

func validateLegacyTasks(a *MigrationAudit, tasks map[string]legacyTasks.Task, actors map[string]bool) {
	ids := make([]string, 0, len(tasks))
	for id, task := range tasks {
		ids = append(ids, id)
		if id == "" || task.ID == "" {
			a.issue("tasks.json", id, "error", "missing_task_id", "task record has no stable ID")
		} else if id != task.ID {
			a.issue("tasks.json", id, "error", "task_id_mismatch", "map key and embedded task ID differ")
		}
		switch task.Status {
		case legacyTasks.StatusBacklog, legacyTasks.StatusTodo, legacyTasks.StatusInProgress, legacyTasks.StatusBlocked, legacyTasks.StatusInReview, legacyTasks.StatusCompleted, legacyTasks.StatusFailed:
		default:
			a.issue("tasks.json", id, "error", "unknown_task_status", fmt.Sprintf("unsupported legacy status %q", task.Status))
		}
		seen := map[string]bool{}
		for _, dependency := range task.BlockedBy {
			if dependency == id {
				a.issue("tasks.json", id, "error", "self_dependency", "task depends on itself")
			} else if seen[dependency] {
				a.issue("tasks.json", id, "warning", "duplicate_dependency", "duplicate prerequisite will need reconciliation")
			}
			seen[dependency] = true
			if _, ok := tasks[dependency]; !ok {
				a.issue("tasks.json", id, "error", "missing_dependency", fmt.Sprintf("prerequisite %q does not exist", dependency))
			}
		}
		for _, dependency := range task.Blocks {
			if _, ok := tasks[dependency]; !ok {
				a.issue("tasks.json", id, "warning", "missing_inverse_dependency", fmt.Sprintf("dependent task %q does not exist", dependency))
			}
		}
		if task.AssignedTo != "" {
			a.issue("tasks.json", id, "warning", "unverified_assignment", "legacy assignee is provenance only; import will not restore authority or credentials")
			actor := strings.TrimPrefix(task.AssignedTo, "agent:")
			if !legacyActorExists(actors, actor) {
				a.issue("tasks.json", id, "warning", "unresolved_assignment", fmt.Sprintf("legacy assignee %q is not registered in state.json", task.AssignedTo))
			}
		}
		if task.Status == legacyTasks.StatusCompleted || task.Status == legacyTasks.StatusInReview {
			a.issue("tasks.json", id, "warning", "unverified_completion", "legacy review state is historical metadata; import will not treat it as verified acceptance")
		}
	}
	sort.Strings(ids)
	visited := map[string]uint8{}
	for _, id := range ids {
		if legacyTaskCycle(id, tasks, visited) {
			a.issue("tasks.json", id, "error", "dependency_cycle", "task dependency graph contains a cycle")
			break
		}
	}
}

func legacyTaskCycle(id string, tasks map[string]legacyTasks.Task, state map[string]uint8) bool {
	if state[id] == 1 {
		return true
	}
	if state[id] == 2 {
		return false
	}
	state[id] = 1
	for _, dependency := range tasks[id].BlockedBy {
		if _, ok := tasks[dependency]; ok && legacyTaskCycle(dependency, tasks, state) {
			return true
		}
	}
	state[id] = 2
	return false
}

func validateLegacyMessages(a *MigrationAudit, data []byte, actors map[string]bool) {
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), maxLegacyMessageLine)
	line, count := 0, 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		count++
		var message legacyMessages.Message
		if err := json.Unmarshal(raw, &message); err != nil {
			a.issue("messages.jsonl", fmt.Sprint(line), "error", "malformed_message", err.Error())
			continue
		}
		if strings.TrimSpace(message.ID) == "" {
			a.issue("messages.jsonl", fmt.Sprint(line), "error", "missing_message_id", "message has no stable ID")
		} else if seen[message.ID] {
			a.issue("messages.jsonl", message.ID, "error", "duplicate_message_id", "message ID occurs more than once")
		}
		seen[message.ID] = true
		if strings.TrimSpace(message.From) == "" {
			a.issue("messages.jsonl", fmt.Sprint(line), "warning", "missing_sender", "message sender is unknown historical provenance")
		} else {
			a.issue("messages.jsonl", message.ID, "warning", "unverified_sender", "legacy sender and role are historical metadata, not authenticated identities")
			actor := strings.TrimPrefix(message.From, "agent:")
			if !legacyActorExists(actors, actor) {
				a.issue("messages.jsonl", message.ID, "warning", "unresolved_sender", fmt.Sprintf("legacy sender %q is not registered in state.json", message.From))
			}
		}
		target := strings.TrimSpace(message.To)
		targetLower := strings.ToLower(target)
		if target != "" && targetLower != "all" && targetLower != "*" && !strings.HasPrefix(targetLower, "role:") {
			actor := strings.TrimPrefix(target, "agent:")
			if !legacyActorExists(actors, actor) {
				a.issue("messages.jsonl", message.ID, "warning", "unresolved_recipient", fmt.Sprintf("legacy recipient %q is not registered in state.json", target))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		a.issue("messages.jsonl", "", "error", "read_failed", err.Error())
	}
	a.Counts["messages"] = count
	for i := range a.Files {
		if a.Files[i].Name == "messages.jsonl" {
			a.Files[i].Records = count
			break
		}
	}
}

func legacyActorExists(actors map[string]bool, actor string) bool {
	return actors[strings.ToLower(actor)]
}
