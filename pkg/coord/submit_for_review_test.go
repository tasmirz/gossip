package coord_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestSubmitForReviewAtomicallyPinsCandidateAndRetries(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "review", "review exact code", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/pkg/worker.go b/pkg/worker.go\n--- a/pkg/worker.go\n+++ b/pkg/worker.go\n@@ -1 +1 @@\n-old\n+new"
	candidate, err := s.SaveCandidateWithID(ctx, project.ID, worker.Token, task.ID, claimed.Attempt, "commit:abc", diff, "capture-1")
	if err != nil {
		t.Fatal(err)
	}
	report, candidate, err := s.SubmitForReview(ctx, project.ID, worker.Token, task.ID, claimed.Attempt, "commit:abc", "Reviewed the change.", candidate.ID, candidate.Digest, "submit-review-1")
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceCandidateID != candidate.ID || report.SourceCandidateDigest != candidate.Digest || report.SourceCandidateBase != candidate.Baseline {
		t.Fatalf("report did not pin exact candidate: report=%+v candidate=%+v", report, candidate)
	}
	retry, retryCandidate, err := s.SubmitForReview(ctx, project.ID, worker.Token, task.ID, claimed.Attempt, "commit:abc", "Reviewed the change.", candidate.ID, candidate.Digest, "submit-review-1")
	if err != nil || retry.ID != report.ID || retryCandidate.ID != candidate.ID {
		t.Fatalf("retry was not idempotent: %s %s %v", retry.ID, retryCandidate.ID, err)
	}
	if _, _, err = s.SubmitForReview(ctx, project.ID, worker.Token, task.ID, claimed.Attempt, "commit:abc", "changed report", candidate.ID, candidate.Digest, "submit-review-1"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("changed idempotency payload was accepted: %v", err)
	}
	events, err := s.ReadEvents(ctx, project.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seenCandidate, seenReview, seenReport := false, false, false
	for _, e := range events {
		if e.EntityID == candidate.ID && e.Kind == "candidate.saved" {
			seenCandidate = true
		}
		if e.Kind == "review.requested" && e.EntityID == task.ID {
			seenReview = true
		}
		if e.EntityID == report.ID && e.Kind == "report.submitted" {
			seenReport = true
		}
	}
	if !seenCandidate || !seenReview || !seenReport {
		t.Fatalf("atomic submission omitted handoff events: candidate=%t review=%t report=%t", seenCandidate, seenReview, seenReport)
	}
}
