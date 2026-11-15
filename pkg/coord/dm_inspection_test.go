package coord

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentDMInspectionScopesAndAudits(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, owner, err := store.CreateProject(ctx, "Project", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.AddPrincipal(ctx, project.ID, owner.Token, "Alice", "worker")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.AddPrincipal(ctx, project.ID, owner.Token, "Bob", "worker")
	if err != nil {
		t.Fatal(err)
	}
	charlie, err := store.AddPrincipal(ctx, project.ID, owner.Token, "Charlie", "worker")
	if err != nil {
		t.Fatal(err)
	}
	const privateBody = "Private conversation body"
	if _, err := store.SendMessage(ctx, project.ID, alice.Token, bob.PrincipalID, "", "", privateBody); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage(ctx, project.ID, bob.Token, alice.PrincipalID, "", "", "Reply"); err != nil {
		t.Fatal(err)
	}
	threads, err := store.AgentDMThreads(ctx, project.ID, owner.Token, alice.PrincipalID, 0)
	if err != nil || len(threads) != 1 || threads[0].LastActivity == 0 {
		t.Fatalf("owner thread list: %v, %v", threads, err)
	}
	threads, err = store.AgentDMThreads(ctx, project.ID, owner.Token, charlie.PrincipalID, 0)
	if err != nil || len(threads) != 0 {
		t.Fatalf("unrelated profile list: %v, %v", threads, err)
	}
	messages, err := store.InspectAgentDM(ctx, project.ID, owner.Token, alice.PrincipalID, bob.PrincipalID, 0)
	if err != nil || len(messages) != 2 || messages[0].Body != privateBody {
		t.Fatalf("owner inspection: %v, %v", messages, err)
	}
	if _, err := store.InspectAgentDM(ctx, project.ID, charlie.Token, alice.PrincipalID, bob.PrincipalID, 0); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("third-party inspection: %v", err)
	}
	for _, recipient := range []struct {
		token string
		want  int
	}{{bob.Token, 1}, {owner.Token, 0}} {
		results, err := store.SearchConversations(ctx, project.ID, recipient.token, privateBody)
		if err != nil || len(results) != recipient.want {
			t.Fatalf("conversation search visibility: %d, %v", len(results), err)
		}
	}
	messageID := messages[0].ID
	if linked, err := store.ConversationMessage(ctx, project.ID, bob.Token, messageID); err != nil || linked.Body != privateBody {
		t.Fatalf("participant source link: %v, %v", linked, err)
	}
	if _, err := store.ConversationMessage(ctx, project.ID, owner.Token, messageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ordinary source link allowed owner inspection: %v", err)
	}
	messages, err = store.ConversationMessages(ctx, project.ID, owner.Token, alice.PrincipalID, 0, 0, 50, true)
	if err != nil || len(messages) != 0 {
		t.Fatalf("ordinary conversation leaked private messages: %v, %v", messages, err)
	}
	result, err := store.SearchProject(ctx, project.ID, owner.Token, privateBody)
	if err != nil || len(result.Messages) != 0 {
		t.Fatalf("ordinary search leaked private messages: %v, %v", result.Messages, err)
	}
	for _, credential := range []Credential{owner, charlie} {
		events, err := store.RecentEvents(ctx, project.ID, credential.Token, 50)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, entry := range events {
			if entry.Kind != "dm.inspected" {
				continue
			}
			count++
			if entry.Actor != owner.PrincipalID || entry.EntityID != project.ID || entry.CreatedAt == 0 || strings.Contains(string(entry.Payload), privateBody) {
				t.Fatalf("invalid audit entry: %+v", entry)
			}
		}
		if (count == 1) != (credential.PrincipalID == owner.PrincipalID) {
			t.Fatalf("unexpected inspection audit visibility: %d", count)
		}
	}
}
