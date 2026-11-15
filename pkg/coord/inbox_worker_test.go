package coord

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestInboxWorkerPersistsAndIsolatesQueue(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project, owner, err := s.CreateProject(ctx, "Project", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddPrincipal(ctx, project.ID, owner.Token, "Worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := DeviceConnection{Profile: "worker", HomeID: s.InstanceID(), ProjectID: project.ID, PrincipalID: worker.PrincipalID, Name: "Project"}
	if err := r.Put(ctx, c, worker.Token); err != nil {
		t.Fatal(err)
	}
	var first string
	for i := 0; i < 103; i++ {
		m, err := s.SendMessageWithID(ctx, project.ID, owner.Token, worker.PrincipalID, "", "New work", "Please check this", fmt.Sprintf("test-message-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = m.ID
		}
	}
	if _, err := s.SendMessage(ctx, project.ID, worker.Token, owner.PrincipalID, "", "Outgoing", "Do not queue"); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); r.RunInboxWorker(workerCtx, s) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := r.InboxStatus(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if status.QueueCount == 103 && status.LastSuccessfulPoll != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup worker did not drain both pages: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	acknowledgments, err := s.MessageAcknowledgments(ctx, project.ID, worker.Token, first)
	if err != nil || len(acknowledgments) != 0 {
		t.Fatalf("polling changed acknowledgments: %v %v", acknowledgments, err)
	}
	if _, err := r.ServiceInbox(ctx, c, first); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	c, _, err = restarted.Resolve(ctx, c.Profile, c.ProjectID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.db.ExecContext(ctx, `UPDATE service_inbox SET events=0,messages=0 WHERE item=''`); err != nil {
		t.Fatal(err)
	}
	if err := restarted.pollInbox(ctx, s, c); err != nil {
		t.Fatal(err)
	}
	status, err := restarted.InboxStatus(ctx, c)
	if err != nil || status.QueueCount != 102 {
		t.Fatalf("cursor replay requeued a dismissed item or lost work: %+v %v", status, err)
	}
	foreign := c
	foreign.Profile = "other"
	status, err = restarted.InboxStatus(ctx, foreign)
	if err != nil || status.QueueCount != 0 {
		t.Fatalf("queue crossed profile boundary: %+v %v", status, err)
	}
}
