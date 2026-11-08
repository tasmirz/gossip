package coord_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestNormalizeRepositoryRemoteRemovesCredentialsAndAliases(t *testing.T) {
	inputs := []string{
		"https://alice:secret@GitHub.com/org/repo.git?access_token=private",
		"ssh://git@github.com/org/repo.git",
		"git@github.com:org/repo.git",
	}
	for _, input := range inputs {
		key, err := coord.NormalizeRepositoryRemote(input)
		if err != nil {
			t.Fatalf("normalize %q: %v", input, err)
		}
		if key != "github.com/org/repo" {
			t.Fatalf("unexpected normalized hint for %q: %q", input, key)
		}
	}
	for _, input := range []string{"file:///tmp/repo", "https://github.com/org/../private", "https://github.com/", "not a remote"} {
		if _, err := coord.NormalizeRepositoryRemote(input); err == nil {
			t.Fatalf("accepted invalid remote %q", input)
		}
	}
}

func TestRepositoryRemoteHintsAreSanitizedAndOwnerManaged(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	repo, err := s.CreateRepository(ctx, project.ID, owner.Token, "service")
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.AddRepositoryRemoteHint(ctx, project.ID, owner.Token, repo.ID, "https://alice:secret@GitHub.com/org/service.git?token=private")
	if err != nil {
		t.Fatal(err)
	}
	if len(added.RemoteHints) != 1 || added.RemoteHints[0] != "github.com/org/service" {
		t.Fatalf("credential-bearing remote was not sanitized: %+v", added)
	}
	if _, err = s.AddRepositoryRemoteHint(ctx, project.ID, owner.Token, repo.ID, "git@github.com:org/service.git"); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListRepositories(ctx, project.ID, worker.Token)
	if err != nil || len(listed) != 1 || len(listed[0].RemoteHints) != 1 {
		t.Fatalf("aliases were not deduplicated: %+v %v", listed, err)
	}
	if _, err = s.AddRepositoryRemoteHint(ctx, project.ID, worker.Token, repo.ID, "https://github.com/another/repo"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-owner added a project recognition hint: %v", err)
	}
	if err = s.RemoveRepositoryRemoteHint(ctx, project.ID, owner.Token, repo.ID, "ssh://git@github.com/org/service"); err != nil {
		t.Fatal(err)
	}
	listed, err = s.ListRepositories(ctx, project.ID, owner.Token)
	if err != nil || len(listed[0].RemoteHints) != 0 {
		t.Fatalf("removed remote hint remained searchable: %+v %v", listed, err)
	}
}

func TestRepositoryRecognitionFindsBeyondDashboardPage(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	ctx := context.Background()
	for i := 0; i < 201; i++ {
		name := "repo-" + strconv.Itoa(i)
		if i == 200 {
			name = "zz-last"
		}
		repo, err := s.CreateRepository(ctx, project.ID, owner.Token, name)
		if err != nil {
			t.Fatal(err)
		}
		if i == 200 {
			if _, err = s.AddRepositoryRemoteHint(ctx, project.ID, owner.Token, repo.ID, "git@example.com:org/last.git"); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := s.ListRepositories(ctx, project.ID, owner.Token)
	if err != nil || len(page) != 200 {
		t.Fatalf("dashboard repository page: %d %v", len(page), err)
	}
	matches, err := s.FindRepositoriesByRemote(ctx, project.ID, owner.Token, "https://example.com/org/last.git")
	if err != nil || len(matches) != 1 || matches[0].Name != "zz-last" {
		t.Fatalf("recognition missed a repository beyond the dashboard page: %+v %v", matches, err)
	}
}
