package coord_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestWebEventRetentionControlsAreProjectOwnerScoped(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	if _, err := store.PublishPlan(ctx, project.ID, owner.Token, "retained plan", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage(ctx, project.ID, owner.Token, worker.PrincipalID, "", "retained", "message"); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 200)
	if err != nil || len(events) == 0 {
		t.Fatalf("could not prepare project event history: %d %v", len(events), err)
	}
	h := coord.NewWebHandler(store)
	request := func(method, path string, payload any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if payload != nil {
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	login := func(token string) *http.Cookie {
		rec := request(http.MethodPost, "/api/login", map[string]string{"project_id": project.ID, "token": token}, nil)
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 {
			t.Fatalf("could not create browser session: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	ownerCookie, workerCookie := login(owner.Token), login(worker.Token)
	base := "/api/projects/" + project.ID
	status := request(http.MethodGet, base+"/retention", nil, ownerCookie)
	if status.Code != http.StatusOK {
		t.Fatalf("owner could not inspect retention status: %d %s", status.Code, status.Body.String())
	}
	body := map[string]any{"operation": "prune", "through_sequence": events[len(events)-1].Sequence}
	if denied := request(http.MethodPost, base+"/events", body, workerCookie); denied.Code != http.StatusForbidden {
		t.Fatalf("worker could prune project history: %d %s", denied.Code, denied.Body.String())
	}
	pruned := request(http.MethodPost, base+"/events", body, ownerCookie)
	if pruned.Code != http.StatusOK {
		t.Fatalf("owner could not prune project activity: %d %s", pruned.Code, pruned.Body.String())
	}
	var retention coord.EventRetention
	if err := json.Unmarshal(pruned.Body.Bytes(), &retention); err != nil || retention.PrunedThrough != events[len(events)-1].Sequence {
		t.Fatalf("web prune response omitted retention watermark: %+v %v", retention, err)
	}
}
