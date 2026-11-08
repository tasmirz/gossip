package coord_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestWebProjectIsolationAndOrigin(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	p2, owner2, err := s.CreateProject(ctx, "second", "other")
	if err != nil {
		t.Fatal(err)
	}
	p3, _, err := s.CreateProject(ctx, "not connected", "private owner")
	if err != nil {
		t.Fatal(err)
	}
	h := coord.NewWebHandler(s)
	do := func(method, path string, body any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(data))
		req.RemoteAddr = "127.0.0.1:12345"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	login := map[string]string{"project_id": p.ID, "token": owner.Token}
	if rec := do("POST", "/api/login", login, nil, "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login: %d", rec.Code)
	}
	rec := do("POST", "/api/login", login, nil, "http://localhost:8081")
	if rec.Code != 200 || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	if rec = do("GET", "/api/overview", nil, nil, ""); rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("empty browser project overview: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/overview", nil, cookie, "")
	var overview []coord.ProjectOverview
	if err = json.Unmarshal(rec.Body.Bytes(), &overview); rec.Code != http.StatusOK || err != nil || len(overview) != 1 || overview[0].ProjectID != p.ID {
		t.Fatalf("overview exposed a disconnected project or omitted the connected project: status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
	rec = do("GET", "/api/projects/"+p.ID+"/tasks", nil, cookie, "")
	if rec.Code != 200 {
		t.Fatalf("authorized read: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/projects/"+p.ID+"/role-presets", nil, cookie, "")
	var presets []coord.RolePreset
	if err = json.Unmarshal(rec.Body.Bytes(), &presets); rec.Code != http.StatusOK || err != nil || len(presets) < 5 {
		t.Fatalf("authorized role preset catalog: status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
	if rec = do("GET", "/api/projects/"+p2.ID+"/role-presets", nil, cookie, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("role presets bypassed project membership: %d", rec.Code)
	}
	rec = do("GET", "/api/projects/"+p.ID+"/integrations", nil, cookie, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("integration history read: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/projects/"+p2.ID+"/tasks", nil, cookie, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross project access: %d", rec.Code)
	}
	rec = do("POST", "/api/projects/"+p.ID+"/messages", map[string]string{"body": "hello"}, cookie, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing origin: %d", rec.Code)
	}
	rec = do("POST", "/api/login", map[string]string{"project_id": p2.ID, "token": owner2.Token}, cookie, "http://localhost:8081")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec = do("GET", "/api/projects", nil, cookie, "")
	var projects []coord.Principal
	if err = json.Unmarshal(rec.Body.Bytes(), &projects); err != nil || len(projects) != 2 {
		t.Fatalf("projects: %s %v", rec.Body.String(), err)
	}
	rec = do("GET", "/api/overview", nil, cookie, "")
	if err = json.Unmarshal(rec.Body.Bytes(), &overview); rec.Code != http.StatusOK || err != nil || len(overview) != 2 {
		t.Fatalf("overview did not aggregate connected projects: status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
	for _, item := range overview {
		if item.ProjectID == p3.ID {
			t.Fatal("overview disclosed an unconnected project's metadata")
		}
	}
	rec = do("POST", "/api/projects/"+p.ID+"/messages", map[string]string{"body": "hello"}, cookie, "http://localhost:8081")
	if rec.Code != 200 {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	workerLogin := do("POST", "/api/login", map[string]string{"project_id": p.ID, "token": worker.Token}, nil, "http://localhost:8081")
	if workerLogin.Code != http.StatusOK {
		t.Fatalf("worker login: %d %s", workerLogin.Code, workerLogin.Body.String())
	}
	workerCookie := workerLogin.Result().Cookies()[0]
	inviteID, inviteSecret, err := coord.PrepareCertificateInvitation()
	if err != nil {
		t.Fatal(err)
	}
	inviteBody := map[string]any{"operation": "create", "id": inviteID, "invitation_token": inviteSecret, "expires_at": time.Now().Add(time.Hour).UnixMilli()}
	rec = do("POST", "/api/projects/"+p.ID+"/certificate-invitations", inviteBody, workerCookie, "http://localhost:8081")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("worker created certificate invitation: %d", rec.Code)
	}
	rec = do("POST", "/api/projects/"+p.ID+"/certificate-invitations", inviteBody, cookie, "http://localhost:8081")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), inviteSecret) {
		t.Fatalf("owner invitation or secret exposure: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/api/projects/"+p.ID+"/certificate-invitations", nil, workerCookie, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("worker listed certificate invitations: %d", rec.Code)
	}
	rec = do("GET", "/api/projects/"+p.ID+"/certificate-invitations", nil, cookie, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), inviteID) || strings.Contains(rec.Body.String(), inviteSecret) {
		t.Fatalf("owner invitation list: %d %s", rec.Code, rec.Body.String())
	}
	preparedProject, remoteCredential, err := coord.PrepareCredential(p.ID)
	if err != nil || preparedProject != p.ID {
		t.Fatalf("prepare remote project invite: %v", err)
	}
	fingerprint := strings.Repeat("a4", 32)
	rec = do("POST", "/api/projects/"+p.ID+"/agents", map[string]any{"operation": "invite_remote", "name": "Remote member", "role": "worker", "principal_id": remoteCredential.PrincipalID, "credential_token": remoteCredential.Token, "fingerprint": fingerprint}, cookie, "http://localhost:8081")
	if rec.Code != http.StatusOK {
		t.Fatalf("atomic remote project invite: %d %s", rec.Code, rec.Body.String())
	}
	if _, err = s.WhoAmI(context.Background(), p.ID, remoteCredential.Token); err != nil {
		t.Fatalf("remote project credential not enrolled: %v", err)
	}
	if allowed, checkErr := s.CertificateAuthorized(context.Background(), p.ID, fingerprint); checkErr != nil || !allowed {
		t.Fatalf("remote project certificate not granted: %v %v", allowed, checkErr)
	}
	if allowed, checkErr := s.CertificateAuthorized(context.Background(), p2.ID, fingerprint); checkErr != nil || allowed {
		t.Fatalf("remote invite crossed project boundary: %v %v", allowed, checkErr)
	}
	taskBody := map[string]any{"operation": "create", "request_id": "delegated-research-test", "revision": 1, "title": "Delegated research", "description": "Investigate the API", "kind": "research", "contract": map[string]string{"review_type": "full_code"}}
	rec = do("POST", "/api/projects/"+p.ID+"/tasks", taskBody, workerCookie, "http://localhost:8081")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("worker created task without explicit grant: %d", rec.Code)
	}
	rec = do("POST", "/api/projects/"+p.ID+"/permissions", map[string]string{"operation": "grant", "principal_id": worker.PrincipalID, "permission": "task.delegate"}, cookie, "http://localhost:8081")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner grant via dashboard API: %d %s", rec.Code, rec.Body.String())
	}
	missingRequestID := map[string]any{}
	for key, value := range taskBody {
		missingRequestID[key] = value
	}
	delete(missingRequestID, "request_id")
	if missing := do("POST", "/api/projects/"+p.ID+"/tasks", missingRequestID, workerCookie, "http://localhost:8081"); missing.Code != http.StatusBadRequest {
		t.Fatalf("dashboard task creation accepted a missing request ID: %d %s", missing.Code, missing.Body.String())
	}
	rec = do("POST", "/api/projects/"+p.ID+"/tasks", taskBody, workerCookie, "http://localhost:8081")
	if rec.Code != http.StatusOK {
		t.Fatalf("delegated worker task creation via dashboard API: %d %s", rec.Code, rec.Body.String())
	}
	var createdTask coord.Task
	if err = json.Unmarshal(rec.Body.Bytes(), &createdTask); err != nil {
		t.Fatal(err)
	}
	rec = do("POST", "/api/projects/"+p.ID+"/tasks", taskBody, workerCookie, "http://localhost:8081")
	var retriedTask coord.Task
	if err = json.Unmarshal(rec.Body.Bytes(), &retriedTask); rec.Code != http.StatusOK || err != nil || retriedTask.ID != createdTask.ID {
		t.Fatalf("task creation retry duplicated work: first=%s retry=%+v status=%d err=%v", createdTask.ID, retriedTask, rec.Code, err)
	}
	changedTaskBody := map[string]any{}
	for key, value := range taskBody {
		changedTaskBody[key] = value
	}
	changedTaskBody["title"] = "Changed after a lost response"
	if changed := do("POST", "/api/projects/"+p.ID+"/tasks", changedTaskBody, workerCookie, "http://localhost:8081"); changed.Code != http.StatusConflict {
		t.Fatalf("task request ID accepted a different payload: %d %s", changed.Code, changed.Body.String())
	}
	rec = do("POST", "/api/projects/"+p.ID+"/permissions", map[string]string{"operation": "revoke", "principal_id": worker.PrincipalID, "permission": "task.delegate"}, cookie, "http://localhost:8081")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner permission revoke via dashboard API: %d %s", rec.Code, rec.Body.String())
	}
	rec = do("POST", "/api/projects/"+p.ID+"/tasks", taskBody, workerCookie, "http://localhost:8081")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoked worker created task: %d", rec.Code)
	}
	rec = do("POST", "/api/projects/"+p.ID+"/agents", map[string]string{"operation": "revoke", "id": owner.PrincipalID}, workerCookie, "http://localhost:8081")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("worker revoked a principal: %d", rec.Code)
	}
	rec = do("POST", "/api/projects/"+p.ID+"/agents", map[string]string{"operation": "revoke", "id": worker.PrincipalID}, cookie, "http://localhost:8081")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner revoke: %d %s", rec.Code, rec.Body.String())
	}
	if _, err = s.WhoAmI(context.Background(), p.ID, worker.Token); err == nil {
		t.Fatal("revoked dashboard credential remained valid")
	}
	rec = do("GET", "/api/projects/"+p2.ID+"/messages", nil, cookie, "")
	if rec.Body.String() != "[]\n" {
		t.Fatalf("message leaked across project: %s", rec.Body.String())
	}
	req := httptest.NewRequest("GET", "http://attacker.example/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, req)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("nonlocal host: %d", blocked.Code)
	}
	spoofed := httptest.NewRequest("GET", "http://localhost:8081/", nil)
	spoofed.RemoteAddr = "192.0.2.10:12345"
	blocked = httptest.NewRecorder()
	h.ServeHTTP(blocked, spoofed)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("nonlocal peer spoofed a loopback Host: %d", blocked.Code)
	}
	for _, host := range []string{"localhost:123;script-src", "127.0.0.1:99999"} {
		r := httptest.NewRequest("GET", "http://localhost/diagram.html", nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid host %q: %d", host, w.Code)
		}
	}
	diagram := do("GET", "/diagram.html", nil, cookie, "")
	if diagram.Code != http.StatusOK || !strings.Contains(diagram.Header().Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Fatal("diagram sandbox policy missing")
	}
}

func TestRolePresetCatalogRechecksProjectMembership(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	h := coord.NewWebHandler(s)
	login := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/login", strings.NewReader(`{"project_id":"`+project.ID+`","token":"`+worker.Token+`"}`))
	login.RemoteAddr = "127.0.0.1:12345"
	login.Header.Set("Origin", "http://localhost:8081")
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("worker login: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	if err := s.RevokePrincipal(context.Background(), project.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8081/api/projects/"+project.ID+"/role-presets", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(loginResponse.Result().Cookies()[0])
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked project credential loaded role presets: %d %s", response.Code, response.Body.String())
	}
}

func TestLocalProjectSetupCreatesSessionAndReturnsCredential(t *testing.T) {
	s, _, _, _, _ := setup(t)
	h := coord.NewWebHandler(s)
	checkout := initLocalGitCheckout(t, "org/checkout")
	body, _ := json.Marshal(map[string]string{"name": "", "local_path": checkout})
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/projects/create", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local project setup: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Project    coord.Principal  `json:"project"`
		Repository coord.Repository `json:"repository"`
		Workspace  coord.Workspace  `json:"workspace"`
		Credential struct {
			ProjectID   string `json:"project_id"`
			PrincipalID string `json:"principal_id"`
			Token       string `json:"token"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Project.ProjectID == "" || result.Project.ProjectName != "checkout" || result.Credential.ProjectID != result.Project.ProjectID || result.Credential.PrincipalID != result.Project.ID || result.Credential.Token == "" || result.Repository.Name != "checkout" || result.Workspace.LocalPath != checkout || result.Workspace.RepositoryID != result.Repository.ID {
		t.Fatalf("incomplete project setup response: %+v", result)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("project setup did not establish a protected browser session")
	}
	projectsReq := httptest.NewRequest(http.MethodGet, "http://localhost:8081/api/projects", nil)
	projectsReq.RemoteAddr = "127.0.0.1:12345"
	projectsReq.AddCookie(cookies[0])
	projectsRec := httptest.NewRecorder()
	h.ServeHTTP(projectsRec, projectsReq)
	var projects []coord.Principal
	if err := json.Unmarshal(projectsRec.Body.Bytes(), &projects); projectsRec.Code != http.StatusOK || err != nil || len(projects) != 1 || projects[0].ProjectID != result.Project.ProjectID {
		t.Fatalf("new project was not connected in browser session: status=%d body=%s err=%v", projectsRec.Code, projectsRec.Body.String(), err)
	}

	// Dashboard sessions are process-local. A browser can retain a cookie from
	// before a dashboard restart; project setup must replace that stale cookie.
	staleCookie := &http.Cookie{Name: "gossip_session", Value: "stale-session-after-restart"}
	restartedBody, _ := json.Marshal(map[string]string{"name": "after restart", "local_path": initLocalGitCheckout(t, "org/after-restart")})
	restartedReq := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/projects/create", bytes.NewReader(restartedBody))
	restartedReq.RemoteAddr = "127.0.0.1:12345"
	restartedReq.Header.Set("Origin", "http://localhost:8081")
	restartedReq.Header.Set("Content-Type", "application/json")
	restartedReq.AddCookie(staleCookie)
	restartedRec := httptest.NewRecorder()
	h.ServeHTTP(restartedRec, restartedReq)
	newCookies := restartedRec.Result().Cookies()
	if restartedRec.Code != http.StatusOK || len(newCookies) != 1 || newCookies[0].Value == staleCookie.Value {
		t.Fatalf("project setup did not replace stale session cookie: status=%d cookies=%v body=%s", restartedRec.Code, newCookies, restartedRec.Body.String())
	}
	projectsAfterRestartReq := httptest.NewRequest(http.MethodGet, "http://localhost:8081/api/projects", nil)
	projectsAfterRestartReq.RemoteAddr = "127.0.0.1:12345"
	projectsAfterRestartReq.AddCookie(newCookies[0])
	projectsAfterRestartRec := httptest.NewRecorder()
	h.ServeHTTP(projectsAfterRestartRec, projectsAfterRestartReq)
	var restartedProjects []coord.Principal
	if err := json.Unmarshal(projectsAfterRestartRec.Body.Bytes(), &restartedProjects); projectsAfterRestartRec.Code != http.StatusOK || err != nil || len(restartedProjects) != 1 {
		t.Fatalf("new session could not access project after dashboard restart: status=%d body=%s err=%v", projectsAfterRestartRec.Code, projectsAfterRestartRec.Body.String(), err)
	}

	remoteReq := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/projects/create", strings.NewReader(`{"name":"remote","local_path":"/tmp"}`))
	remoteReq.RemoteAddr = "192.0.2.10:12345"
	remoteReq.Header.Set("Origin", "http://localhost:8081")
	remoteReq.Header.Set("Content-Type", "application/json")
	remoteRec := httptest.NewRecorder()
	h.ServeHTTP(remoteRec, remoteReq)
	if remoteRec.Code != http.StatusForbidden {
		t.Fatalf("remote caller created a local project: %d", remoteRec.Code)
	}
}

func initLocalGitCheckout(t *testing.T, remote string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"remote", "add", "origin", "https://github.com/" + remote + ".git"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func TestWebProjectAccessErrorsDistinguishMissingCredentialFromRoleDenial(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	h := coord.NewWebHandler(s)
	request := func(method, path string, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost:8081"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		if body != "" {
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
	path := "/api/projects/" + project.ID + "/repositories"
	missing := request(http.MethodGet, path, "", nil)
	if missing.Code != http.StatusUnauthorized || !strings.Contains(missing.Body.String(), "no project credential is connected") {
		t.Fatalf("missing browser project credential was not explained: %d %s", missing.Code, missing.Body.String())
	}
	login := func(token string) *http.Cookie {
		t.Helper()
		rec := request(http.MethodPost, "/api/login", `{"project_id":"`+project.ID+`","token":"`+token+`"}`, nil)
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
			t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	ownerCookie := login(owner.Token)
	workerCookie := login(worker.Token)
	replicas := request(http.MethodGet, "/api/projects/"+project.ID+"/replicas", "", ownerCookie)
	if replicas.Code != http.StatusOK || strings.TrimSpace(replicas.Body.String()) != "[]" {
		t.Fatalf("owner replica receipt view: %d %s", replicas.Code, replicas.Body.String())
	}
	workerReplicas := request(http.MethodGet, "/api/projects/"+project.ID+"/replicas", "", workerCookie)
	if workerReplicas.Code != http.StatusForbidden || !strings.Contains(workerReplicas.Body.String(), "project role is not allowed") {
		t.Fatalf("worker accessed owner-only replica receipts: %d %s", workerReplicas.Code, workerReplicas.Body.String())
	}
	denied := request(http.MethodPost, path, `{"name":"worker repository"}`, workerCookie)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "project role is not allowed") {
		t.Fatalf("valid worker credential did not produce a role-specific denial: %d %s", denied.Code, denied.Body.String())
	}
	if err := s.RevokePermission(context.Background(), project.ID, owner.Token, worker.PrincipalID, coord.PermissionProjectRead); err != nil {
		t.Fatal(err)
	}
	readDenied := request(http.MethodGet, "/api/projects/"+project.ID+"/tasks", "", workerCookie)
	if readDenied.Code != http.StatusForbidden || !strings.Contains(readDenied.Body.String(), "project role is not allowed") {
		t.Fatalf("valid but read-revoked project session did not produce 403: %d %s", readDenied.Code, readDenied.Body.String())
	}
	if _, err := s.GrantPermission(context.Background(), project.ID, owner.Token, worker.PrincipalID, coord.PermissionProjectRead); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePrincipal(context.Background(), project.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	revoked := request(http.MethodGet, "/api/projects/"+project.ID+"/workspaces", "", workerCookie)
	if revoked.Code != http.StatusUnauthorized || !strings.Contains(revoked.Body.String(), "access is no longer valid") {
		t.Fatalf("revoked credential was not distinguished from role denial: %d %s", revoked.Code, revoked.Body.String())
	}
	assetRequest := httptest.NewRequest(http.MethodGet, "http://localhost:8081/app.js", nil)
	assetRequest.RemoteAddr = "127.0.0.1:12345"
	assetResponse := httptest.NewRecorder()
	h.ServeHTTP(assetResponse, assetRequest)
	app := assetResponse.Body.String()
	if assetResponse.Code != http.StatusOK || !strings.Contains(app, "response.status===401&&projectPath){expireProjectSession") || !strings.Contains(app, "e.status===403&&state.project?.project_id===project.project_id") {
		t.Fatalf("browser does not distinguish invalid credentials from permission denial: status=%d", assetResponse.Code)
	}
}

func TestTaskAPIAndUIExposePlannedReviewContract(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	planningTool := coord.MCPServer(s, project.ID, owner.Token).GetTool("gossip_task")
	var reviewTypeHelp string
	if planningTool != nil {
		if property, ok := planningTool.Tool.InputSchema.Properties["review_type"].(map[string]any); ok {
			reviewTypeHelp, _ = property["description"].(string)
		}
	}
	if !strings.Contains(reviewTypeHelp, "Ask the user during planning if unspecified") || !strings.Contains(reviewTypeHelp, "full_code or diagram_report") {
		t.Fatalf("MCP task planning does not explicitly ask for the review deliverable: %+v", planningTool)
	}
	contract := coord.ReportContract{ReviewType: "diagram_report", ReviewMode: "report_first", Formats: []string{"markdown", "mermaid_class", "mermaid_sequence"}, Coverage: []string{"project access", "revocation"}, RequiredRole: "reviewer", RequiredCapabilities: []string{"git_workspace_inspection"}}
	task, err := s.CreateTask(context.Background(), project.ID, owner.Token, 1, "Review access control", "Trace project membership and revocation", "review", contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := coord.NewWebHandler(s)
	login := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/login", strings.NewReader(`{"project_id":"`+project.ID+`","token":"`+owner.Token+`"}`))
	login.RemoteAddr = "127.0.0.1:12345"
	login.Header.Set("Origin", "http://localhost:8081")
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	tasksRequest := httptest.NewRequest(http.MethodGet, "http://localhost:8081/api/projects/"+project.ID+"/tasks", nil)
	tasksRequest.RemoteAddr = "127.0.0.1:12345"
	tasksRequest.AddCookie(loginResponse.Result().Cookies()[0])
	tasksResponse := httptest.NewRecorder()
	h.ServeHTTP(tasksResponse, tasksRequest)
	var tasks []coord.Task
	if err = json.Unmarshal(tasksResponse.Body.Bytes(), &tasks); tasksResponse.Code != http.StatusOK || err != nil || len(tasks) != 1 || tasks[0].ID != task.ID || tasks[0].Contract.ReviewType != "diagram_report" || tasks[0].Contract.RequiredRole != "reviewer" || len(tasks[0].Contract.RequiredCapabilities) != 1 || tasks[0].Contract.RequiredCapabilities[0] != "git_workspace_inspection" {
		t.Fatalf("task API omitted planned review contract: status=%d body=%s err=%v", tasksResponse.Code, tasksResponse.Body.String(), err)
	}
	assetRequest := httptest.NewRequest(http.MethodGet, "http://localhost:8081/app.js", nil)
	assetRequest.RemoteAddr = "127.0.0.1:12345"
	assetResponse := httptest.NewRecorder()
	h.ServeHTTP(assetResponse, assetRequest)
	app := assetResponse.Body.String()
	if assetResponse.Code != http.StatusOK || !strings.Contains(app, "t.report_contract||t.contract||{}") || !strings.Contains(app, "contract.review_type==='diagram_report'?'Markdown report with UML diagrams':'Full code review'") || !strings.Contains(app, "eligibleAssignees") || !strings.Contains(app, "contract.required_role") || !strings.Contains(app, "hasCapabilities") || !strings.Contains(app, "contract.required_capabilities.every") || strings.Contains(app, "if(!contract.required_role)return true;") || !strings.Contains(app, "Select an online agent reporting all required capabilities") || !strings.Contains(app, "Host reported (unverified)") {
		t.Fatalf("task details do not consume and label the planned review contract: status=%d", assetResponse.Code)
	}
}

func TestRemoteWebCertificateProjectGrants(t *testing.T) {
	s, _, p1, owner1, _ := setup(t)
	if err := s.SetRemoteAccessEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	p2, owner2, err := s.CreateProject(context.Background(), "second", "other")
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{Raw: []byte("enrolled human certificate")}
	other := &x509.Certificate{Raw: []byte("different human certificate")}
	sum := sha256.Sum256(certificate.Raw)
	fingerprint := hex.EncodeToString(sum[:])
	if _, err = s.AddCertificateGrant(context.Background(), p1.ID, owner1.Token, fingerprint); err != nil {
		t.Fatal(err)
	}
	h := coord.NewWebHandlerWithCertificateGrants(s, []string{"ui.example:8444"})
	wrongHost := httptest.NewRequest("GET", "https://localhost:8444/", nil)
	wrongHost.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	wrongHostResponse := httptest.NewRecorder()
	h.ServeHTTP(wrongHostResponse, wrongHost)
	if wrongHostResponse.Code != http.StatusForbidden {
		t.Fatalf("HTTPS request bypassed host allowlist: %d", wrongHostResponse.Code)
	}
	request := func(method, path string, body any, cookie *http.Cookie, cert *x509.Certificate) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "https://ui.example:8444"+path, bytes.NewReader(data))
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://ui.example:8444")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(project string, token string, cert *x509.Certificate) *httptest.ResponseRecorder {
		t.Helper()
		return request("POST", "/api/login", map[string]string{"project_id": project, "token": token}, nil, cert)
	}
	first := login(p1.ID, owner1.Token, certificate)
	if first.Code != http.StatusOK || len(first.Result().Cookies()) == 0 {
		t.Fatalf("authorized certificate could not connect: %d %s", first.Code, first.Body.String())
	}
	if denied := login(p2.ID, owner2.Token, certificate); denied.Code != http.StatusUnauthorized {
		t.Fatalf("certificate accessed ungranted project: %d", denied.Code)
	}
	cookie := first.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("remote session cookie must be Secure, HttpOnly, and SameSite=Strict")
	}
	if denied := request("GET", "/api/projects/"+p1.ID+"/plan", nil, cookie, other); denied.Code != http.StatusForbidden {
		t.Fatalf("session was usable with another certificate: %d", denied.Code)
	}
	if denied := request("GET", "/api/projects/"+p1.ID+"/plan", nil, cookie, certificate); denied.Code != http.StatusOK {
		t.Fatalf("authorized session failed: %d %s", denied.Code, denied.Body.String())
	}
	_, prepared, err := coord.PrepareCredential(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite := request("POST", "/api/projects/"+p1.ID+"/agents", map[string]string{"operation": "invite", "name": "LAN worker", "role": "worker", "principal_id": prepared.PrincipalID, "credential_token": prepared.Token}, cookie, certificate)
	if invite.Code != http.StatusOK {
		t.Fatalf("remote owner could not invite a project member: %d %s", invite.Code, invite.Body.String())
	}
	var invited coord.Credential
	if err := json.Unmarshal(invite.Body.Bytes(), &invited); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhoAmI(context.Background(), p2.ID, invited.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("invited project credential crossed project scope: %v", err)
	}
	if denied := login(p1.ID, invited.Token, other); denied.Code != http.StatusUnauthorized {
		t.Fatalf("project credential bypassed the remote certificate grant: %d", denied.Code)
	}
	otherSum := sha256.Sum256(other.Raw)
	otherFingerprint := hex.EncodeToString(otherSum[:])
	grant := request("POST", "/api/projects/"+p1.ID+"/certificates", map[string]string{"operation": "grant", "fingerprint": otherFingerprint}, cookie, certificate)
	if grant.Code != http.StatusOK {
		t.Fatalf("owner could not grant another certificate: %d %s", grant.Code, grant.Body.String())
	}
	otherLogin := login(p1.ID, invited.Token, other)
	if otherLogin.Code != http.StatusOK || len(otherLogin.Result().Cookies()) == 0 {
		t.Fatalf("separately granted certificate and project credential did not connect: %d %s", otherLogin.Code, otherLogin.Body.String())
	}
	otherCookie := otherLogin.Result().Cookies()[0]
	revoked := request("POST", "/api/projects/"+p1.ID+"/certificates", map[string]string{"operation": "revoke", "fingerprint": otherFingerprint}, cookie, certificate)
	if revoked.Code != http.StatusOK {
		t.Fatalf("owner could not revoke certificate: %d %s", revoked.Code, revoked.Body.String())
	}
	if denied := request("GET", "/api/projects/"+p1.ID+"/plan", nil, otherCookie, other); denied.Code != http.StatusForbidden {
		t.Fatalf("certificate grant revocation was not immediate: %d", denied.Code)
	}
	memberRevoked := request("POST", "/api/projects/"+p1.ID+"/agents", map[string]string{"operation": "revoke", "id": invited.PrincipalID}, cookie, certificate)
	if memberRevoked.Code != http.StatusOK {
		t.Fatalf("project owner could not revoke invited principal: %d %s", memberRevoked.Code, memberRevoked.Body.String())
	}
	if denied := login(p1.ID, invited.Token, certificate); denied.Code != http.StatusUnauthorized {
		t.Fatalf("project credential revocation was not immediate: %d", denied.Code)
	}
	logout := request("POST", "/api/logout", map[string]string{}, cookie, certificate)
	if logout.Code != http.StatusOK || len(logout.Result().Cookies()) == 0 || !logout.Result().Cookies()[0].Secure {
		t.Fatalf("remote logout cookie was not securely cleared: %d %s", logout.Code, logout.Body.String())
	}
}

func TestWebStaleCertificateSessionCanReconnectWithoutUnauthorizedToast(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	if err := s.SetRemoteAccessEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	certA := &x509.Certificate{Raw: []byte("first browser certificate")}
	certB := &x509.Certificate{Raw: []byte("second browser certificate")}
	// Grant the actual fingerprints used by webClientPin.
	for _, cert := range []*x509.Certificate{certA, certB} {
		sum := sha256.Sum256(cert.Raw)
		if _, err := s.AddCertificateGrant(context.Background(), project.ID, owner.Token, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	h := coord.NewWebHandlerWithCertificateGrants(s, []string{"ui.example:8444"})
	request := func(method, path string, body any, cookie *http.Cookie, cert *x509.Certificate) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "https://ui.example:8444"+path, bytes.NewReader(data))
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://ui.example:8444")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	firstLogin := request("POST", "/api/login", map[string]string{"project_id": project.ID, "token": owner.Token}, nil, certA)
	if firstLogin.Code != http.StatusOK {
		t.Fatalf("first certificate login failed: %d %s", firstLogin.Code, firstLogin.Body.String())
	}
	staleCookie := firstLogin.Result().Cookies()[0]
	projects := request("GET", "/api/projects", nil, staleCookie, certB)
	var connected []coord.Principal
	if projects.Code != http.StatusOK || json.Unmarshal(projects.Body.Bytes(), &connected) != nil || len(connected) != 0 {
		t.Fatalf("stale certificate session should return an empty connected-project list: %d %s", projects.Code, projects.Body.String())
	}
	if len(projects.Result().Cookies()) == 0 || projects.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatal("stale certificate session cookie was not cleared")
	}
	reconnected := request("POST", "/api/login", map[string]string{"project_id": project.ID, "token": owner.Token}, staleCookie, certB)
	if reconnected.Code != http.StatusOK || len(reconnected.Result().Cookies()) == 0 || reconnected.Result().Cookies()[0].Value == staleCookie.Value {
		t.Fatalf("authorized certificate could not replace stale browser session: %d %s", reconnected.Code, reconnected.Body.String())
	}
	projects = request("GET", "/api/projects", nil, reconnected.Result().Cookies()[0], certB)
	if projects.Code != http.StatusOK || !strings.Contains(projects.Body.String(), project.ID) {
		t.Fatalf("reconnected browser session could not load its project: %d %s", projects.Code, projects.Body.String())
	}
}

func TestProjectRecognitionOnlySearchesConnectedProjects(t *testing.T) {
	s, _, p1, owner1, _ := setup(t)
	p2, owner2, err := s.CreateProject(context.Background(), "second", "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		project coord.Project
		owner   coord.Credential
		name    string
	}{{p1, owner1, "service one"}, {p2, owner2, "service two"}} {
		repo, e := s.CreateRepository(context.Background(), pair.project.ID, pair.owner.Token, pair.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.AddRepositoryRemoteHint(context.Background(), pair.project.ID, pair.owner.Token, repo.ID, "https://github.com/org/shared.git"); e != nil {
			t.Fatal(e)
		}
	}
	h := coord.NewWebHandler(s)
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:12345"
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(project, token string, cookie *http.Cookie) *http.Cookie {
		t.Helper()
		rec := request("POST", "/api/login", map[string]string{"project_id": project, "token": token}, cookie)
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 {
			t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	cookie := login(p1.ID, owner1.Token, nil)
	find := func(cookie *http.Cookie) []coord.ProjectSuggestion {
		t.Helper()
		rec := request("POST", "/api/recognize", map[string]string{"remote_url": "git@github.com:org/shared.git"}, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("project recognition failed: %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "alice") {
			t.Fatalf("recognition returned remote credentials: %s", rec.Body.String())
		}
		var suggestions []coord.ProjectSuggestion
		if err := json.Unmarshal(rec.Body.Bytes(), &suggestions); err != nil {
			t.Fatal(err)
		}
		return suggestions
	}
	one := find(cookie)
	if len(one) != 1 || one[0].ProjectID != p1.ID {
		t.Fatalf("recognition disclosed an unconnected project: %+v", one)
	}
	cookie = login(p2.ID, owner2.Token, cookie)
	two := find(cookie)
	if len(two) != 2 || two[0].ProjectID == two[1].ProjectID {
		t.Fatalf("recognition did not return the ambiguous connected projects: %+v", two)
	}
}

func TestWebReadsProjectScopedLegacyHistory(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "state.json"), []byte(`{"project_name":"legacy","agents":{},"goals":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	archived, _, err := s.ImportLegacyArchive(context.Background(), project.ID, owner.Token, source, false)
	if err != nil {
		t.Fatal(err)
	}
	h := coord.NewWebHandler(s)
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:12345"
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := request("POST", "/api/login", map[string]string{"project_id": project.ID, "token": worker.Token}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("member login: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	imports := request("GET", "/api/projects/"+project.ID+"/legacy-imports", nil, cookie)
	if imports.Code != http.StatusOK || !strings.Contains(imports.Body.String(), archived.ID) {
		t.Fatalf("member could not read archive list: %d %s", imports.Code, imports.Body.String())
	}
	records := request("GET", "/api/projects/"+project.ID+"/legacy-records?import_id="+archived.ID+"&type=goal", nil, cookie)
	if records.Code != http.StatusOK || records.Body.String() != "[]\n" {
		t.Fatalf("member could not read archive records: %d %s", records.Code, records.Body.String())
	}
}

func TestRemoteWebLiveMutualTLS(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	if err := s.SetRemoteAccessEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key := func() *ecdsa.PrivateKey {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := key()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Gossip test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage, ips []net.IP) tls.Certificate {
		t.Helper()
		k := key()
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: ips}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: cert}
	}
	serverCert := issue(2, x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	clientCert := issue(3, x509.ExtKeyUsageClientAuth, nil)
	ungrantedCert := issue(4, x509.ExtKeyUsageClientAuth, nil)
	pin := sha256.Sum256(clientCert.Leaf.Raw)
	if _, err = s.AddCertificateGrant(context.Background(), project.ID, owner.Token, hex.EncodeToString(pin[:])); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewUnstartedServer(nil)
	host, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	hostPort := net.JoinHostPort(host, strconv.Itoa(portNumber))
	ts.Config.Handler = coord.NewWebHandlerWithCertificateGrants(s, []string{hostPort})
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
	ts.StartTLS()
	defer ts.Close()

	client := func(cert tls.Certificate) *http.Client {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}}}}
	}
	post := func(c *http.Client, path, body string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	ownerClient := client(clientCert)
	login := post(ownerClient, "/api/login", `{"project_id":"`+project.ID+`","token":"`+owner.Token+`"}`, nil)
	defer login.Body.Close()
	if login.StatusCode != http.StatusOK || len(login.Cookies()) == 0 || !login.Cookies()[0].Secure {
		t.Fatalf("mutual-TLS browser login failed or cookie is insecure: %d %s", login.StatusCode, login.Status)
	}
	cookie := login.Cookies()[0]
	readReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/projects/"+project.ID+"/plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	readReq.AddCookie(cookie)
	read, err := ownerClient.Do(readReq)
	if err != nil {
		t.Fatal(err)
	}
	read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("authorized live TLS dashboard request denied: %d", read.StatusCode)
	}
	denied := post(client(ungrantedCert), "/api/login", `{"project_id":"`+project.ID+`","token":"`+owner.Token+`"}`, nil)
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("CA-trusted but project-ungranted client could log in: %d", denied.StatusCode)
	}
}

func TestWebAccessControlsExposeTheirSelectedProjectScope(t *testing.T) {
	s, _, _, _, _ := setup(t)
	h := coord.NewWebHandler(s)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://localhost:8081"+path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	page := request("/")
	if page.Code != http.StatusOK {
		t.Fatalf("dashboard page: %d", page.Code)
	}
	for _, required := range []string{"id=\"access-project-name\"", "id=\"access-project-id\"", "Every invite, member revocation, permission grant, and certificate change", "value=\"project.read\"", "value=\"message.send\"", "value=\"task.claim\"", "value=\"result.submit\"", "value=\"plan.edit\"", "value=\"settings.edit\"", "value=\"review.decide\"", "value=\"membership.manage\""} {
		if !strings.Contains(page.Body.String(), required) {
			t.Errorf("dashboard access scope is missing %q", required)
		}
	}
	app := request("/app.js")
	if app.Code != http.StatusOK || !strings.Contains(app.Body.String(), "state.project=project;state.projectReadDenied=false;state.replicas=[]") || !strings.Contains(app.Body.String(), "renderAccessScope()") || !strings.Contains(app.Body.String(), "$('#access-project-id').textContent=project.project_id") || !strings.Contains(app.Body.String(), "hasProjectPermission('plan.edit')") || !strings.Contains(app.Body.String(), "hasProjectPermission('settings.edit')") || !strings.Contains(app.Body.String(), "hasProjectPermission('membership.manage')") {
		t.Fatalf("dashboard does not bind access controls to the selected project: %d", app.Code)
	}
}

func TestWebResearchWorkflow(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	h := coord.NewWebHandler(s)
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:12345"
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(token string) *http.Cookie {
		t.Helper()
		r := request("POST", "/api/login", map[string]string{"project_id": p.ID, "token": token}, nil)
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		return r.Result().Cookies()[0]
	}
	o, w := login(owner.Token), login(worker.Token)
	base := "/api/projects/" + p.ID + "/"
	r := request("POST", base+"tasks", map[string]any{"operation": "create", "request_id": "investigate-test", "revision": 1, "title": "Investigate", "description": "Find evidence", "kind": "research", "contract": map[string]any{"formats": []string{"mermaid_sequence"}, "review_mode": "report_first", "review_type": "full_code"}}, o)
	if r.Code != 200 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	var task coord.Task
	if err := json.Unmarshal(r.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	r = request("GET", base+"task?id="+url.QueryEscape(task.ID), nil, o)
	if r.Code != 200 {
		t.Fatalf("task detail route: %d %s", r.Code, r.Body.String())
	}
	var taskDetail coord.Task
	if err := json.Unmarshal(r.Body.Bytes(), &taskDetail); err != nil || taskDetail.ID != task.ID {
		t.Fatalf("task detail mismatch: %+v %v", taskDetail, err)
	}
	r = request("POST", base+"tasks", map[string]any{"operation": "claim", "id": task.ID}, w)
	if r.Code != 200 {
		t.Fatalf("claim: %d %s", r.Code, r.Body.String())
	}
	if err := json.Unmarshal(r.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	r = request("GET", base+"attempt?id="+url.QueryEscape(task.ID)+"&number="+strconv.Itoa(task.Attempt), nil, o)
	if r.Code != 200 {
		t.Fatalf("attempt detail route: %d %s", r.Code, r.Body.String())
	}
	var attempt coord.Attempt
	if err := json.Unmarshal(r.Body.Bytes(), &attempt); err != nil || attempt.TaskID != task.ID || attempt.Number != task.Attempt {
		t.Fatalf("attempt detail mismatch: %+v %v", attempt, err)
	}
	r = request("POST", base+"reports", map[string]any{"id": task.ID, "attempt": task.Attempt, "baseline": "commit:abc", "body": "Findings"}, w)
	if r.Code != 200 {
		t.Fatalf("submit: %d %s", r.Code, r.Body.String())
	}
	var report coord.Report
	if err := json.Unmarshal(r.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	r = request("POST", base+"review", map[string]string{"id": report.ID, "digest": report.Digest}, o)
	if r.Code != 200 {
		t.Fatalf("review: %d %s", r.Code, r.Body.String())
	}
	r = request("GET", base+"tasks", nil, o)
	var tasks []coord.Task
	if err := json.Unmarshal(r.Body.Bytes(), &tasks); err != nil || len(tasks) != 1 || tasks[0].Status != "accepted" {
		t.Fatalf("task state: %s %v", r.Body.String(), err)
	}
}

func TestWebOwnerCanInviteProjectPrincipalOnce(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	h := coord.NewWebHandler(s)
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:12345"
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(token string) *http.Cookie {
		t.Helper()
		rec := request("POST", "/api/login", map[string]string{"project_id": project.ID, "token": token}, nil)
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
			t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	path := "/api/projects/" + project.ID + "/agents"
	_, prepared, err := coord.PrepareCredential(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	requestBody := map[string]string{"operation": "invite", "name": "Remote reviewer", "role": "reviewer", "principal_id": prepared.PrincipalID, "credential_token": prepared.Token}
	if denied := request("POST", path, requestBody, login(worker.Token)); denied.Code != http.StatusForbidden {
		t.Fatalf("worker issued a project credential: %d %s", denied.Code, denied.Body.String())
	}
	ownerCookie := login(owner.Token)
	issued := request("POST", path, requestBody, ownerCookie)
	if issued.Code != http.StatusOK {
		t.Fatalf("owner could not invite a member: %d %s", issued.Code, issued.Body.String())
	}
	var credential coord.Credential
	if err := json.Unmarshal(issued.Body.Bytes(), &credential); err != nil {
		t.Fatal(err)
	}
	if credential != prepared {
		t.Fatalf("issued credential differs from the prepared recovery credential: %+v", credential)
	}
	retried := request("POST", path, requestBody, ownerCookie)
	if retried.Code != http.StatusOK || retried.Body.String() != issued.Body.String() {
		t.Fatalf("retry did not return the same enrolled credential: %d %s", retried.Code, retried.Body.String())
	}
	principal, err := s.WhoAmI(context.Background(), project.ID, credential.Token)
	if err != nil || principal.ID != credential.PrincipalID || principal.Role != "reviewer" || principal.Name != "Remote reviewer" {
		t.Fatalf("invited credential is not project-scoped as requested: %+v %v", principal, err)
	}
}

func TestWebRemoteAccessKillSwitchRejectsHTTPS(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	ctx := context.Background()

	// Verify initially disabled
	enabled, err := s.RemoteAccessEnabled(ctx)
	if err != nil || enabled {
		t.Fatalf("expected initial RemoteAccessEnabled=false, got: %v %v", enabled, err)
	}

	now := time.Now()
	genKey := func() *ecdsa.PrivateKey {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := genKey()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Gossip test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage, ips []net.IP) tls.Certificate {
		t.Helper()
		k := genKey()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  ips,
			DNSNames:     []string{"localhost"},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: cert}
	}
	serverCert := issue(2, x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	clientCert := issue(3, x509.ExtKeyUsageClientAuth, nil)

	pin := sha256.Sum256(clientCert.Leaf.Raw)
	if _, err = s.AddCertificateGrant(ctx, project.ID, owner.Token, hex.EncodeToString(pin[:])); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewUnstartedServer(nil)
	host, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	hostPort := net.JoinHostPort(host, strconv.Itoa(portNumber))
	ts.Config.Handler = coord.NewWebHandlerWithCertificateGrants(s, []string{hostPort})
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	ts.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ts.StartTLS()
	defer ts.Close()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS13,
				RootCAs:      roots,
				Certificates: []tls.Certificate{clientCert},
			},
		},
	}

	loginPayload, _ := json.Marshal(map[string]string{"project_id": project.ID, "token": owner.Token})

	// 1. When kill switch is disabled (default): HTTPS request returns 503
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/login", bytes.NewReader(loginPayload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 when remote access disabled, got %d: %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "remote dashboard is disabled by the local kill switch") {
		t.Fatalf("expected kill switch notice in body, got: %s", string(body))
	}

	// 2. Loopback HTTP (r.TLS == nil) is NOT blocked by kill switch
	loopbackReq := httptest.NewRequest(http.MethodGet, "http://localhost:8081/", nil)
	loopbackReq.RemoteAddr = "127.0.0.1:12345"
	loopbackRec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(loopbackRec, loopbackReq)
	if loopbackRec.Code != http.StatusOK {
		t.Fatalf("loopback HTTP request was blocked: got %d", loopbackRec.Code)
	}

	// 3. Enable remote access: HTTPS requests succeed
	if err = s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	req2, err := http.NewRequest(http.MethodPost, ts.URL+"/api/login", bytes.NewReader(loginPayload))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", ts.URL)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 after enabling remote access, got %d", resp2.StatusCode)
	}

	// 4. Disable remote access: HTTPS requests immediately fail with 503 again
	if err = s.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	req3, err := http.NewRequest(http.MethodPost, ts.URL+"/api/login", bytes.NewReader(loginPayload))
	if err != nil {
		t.Fatal(err)
	}
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Origin", ts.URL)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 after re-disabling remote access, got %d", resp3.StatusCode)
	}
	if !strings.Contains(string(body3), "remote dashboard is disabled by the local kill switch") {
		t.Fatalf("expected kill switch notice in body, got: %s", string(body3))
	}
}

func TestWebRemoteAccessStatusIsLoopbackOnlyAndReadOnly(t *testing.T) {
	s, _, _, _, _ := setup(t)
	h := coord.NewWebHandlerWithCertificateGrants(s, []string{"dashboard.example"})
	request := func(method, target, body string, secure bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
			if secure {
				r.Header.Set("Origin", "https://dashboard.example")
			} else {
				r.Header.Set("Origin", "http://localhost:8081")
			}
		}
		if secure {
			certificate := &x509.Certificate{Raw: []byte("trusted dashboard client")}
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	status := request(http.MethodGet, "http://localhost:8081/api/remote-access", "", false)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"enabled":false`) {
		t.Fatalf("loopback status did not report safe default: %d %s", status.Code, status.Body.String())
	}
	localWrite := request(http.MethodPost, "http://localhost:8081/api/remote-access", `{"enabled":true}`, false)
	if localWrite.Code != http.StatusNotFound {
		t.Fatalf("loopback UI exposed an unauthenticated switch mutation: %d %s", localWrite.Code, localWrite.Body.String())
	}
	if enabled, err := s.RemoteAccessEnabled(context.Background()); err != nil || enabled {
		t.Fatalf("local UI write changed switch: enabled=%v err=%v", enabled, err)
	}
	if err := s.SetRemoteAccessEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	remoteRead := request(http.MethodGet, "https://dashboard.example/api/remote-access", "", true)
	if remoteRead.Code != http.StatusForbidden {
		t.Fatalf("remote TLS dashboard read host-local state: %d %s", remoteRead.Code, remoteRead.Body.String())
	}
	remoteWrite := request(http.MethodPost, "https://dashboard.example/api/remote-access", `{"enabled":false}`, true)
	if remoteWrite.Code != http.StatusNotFound {
		t.Fatalf("remote TLS dashboard exposed switch mutation: %d %s", remoteWrite.Code, remoteWrite.Body.String())
	}
	if enabled, err := s.RemoteAccessEnabled(context.Background()); err != nil || !enabled {
		t.Fatalf("web attempt changed switch despite denial: enabled=%v err=%v", enabled, err)
	}
}

func TestWebBackendCreateProjectWithWorkspaceCompliance(t *testing.T) {
	s, _, _, _, _ := setup(t)
	ctx := context.Background()

	// Compile-time interface compliance check
	var backend coord.WebBackend = s

	// Direct interface call
	tmpDir := t.TempDir()
	observation := coord.GitObservation{
		Root:   tmpDir,
		Branch: "feature/test",
		Head:   "1111222233334444555566667777888899990000",
		Dirty:  false,
	}

	project, cred, repo, ws, err := backend.CreateProjectWithWorkspace(
		ctx,
		"interface-check-project",
		"interface-owner",
		"interface-repo",
		"test-node",
		observation,
	)
	if err != nil {
		t.Fatalf("CreateProjectWithWorkspace failed via WebBackend interface: %v", err)
	}
	if project.Name != "interface-check-project" {
		t.Fatalf("unexpected project name: %q", project.Name)
	}
	if cred.PrincipalID == "" || cred.Token == "" {
		t.Fatalf("expected non-empty owner credential: %+v", cred)
	}
	if repo.Name != "interface-repo" {
		t.Fatalf("unexpected repo name: %q", repo.Name)
	}
	if ws.Branch != "feature/test" || ws.Head != observation.Head {
		t.Fatalf("unexpected workspace: %+v", ws)
	}

	// Also verify that WebHandler invokes it cleanly through /api/projects/create
	h := coord.NewWebHandler(backend)
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, gitErr := cmd.CombinedOutput()
		if gitErr != nil {
			t.Fatalf("git %v failed: %v\n%s", args, gitErr, string(out))
		}
	}
	runGit("init")
	runGit("checkout", "-b", "main")
	testFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial commit")

	reqBody, _ := json.Marshal(map[string]string{
		"name":       "web-created-project",
		"local_path": tmpDir,
	})
	createReq := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/projects/create", bytes.NewReader(reqBody))
	createReq.RemoteAddr = "127.0.0.1:12345"
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Origin", "http://localhost:8081")

	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 from createLocalProject, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var createdResult struct {
		Project struct {
			Name string `json:"project_name"`
		} `json:"project"`
		Credential struct {
			Token string `json:"token"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createdResult); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if createdResult.Project.Name != "web-created-project" || createdResult.Credential.Token == "" {
		t.Fatalf("unexpected project created via web endpoint: %+v", createdResult)
	}
}

func TestWebDistinct401Vs403StatusReturnsOnAccessLoss(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	h := coord.NewWebHandler(s)

	doReq := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost:8081"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		if body != "" {
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
		t.Helper()
		payload := `{"project_id":"` + project.ID + `","token":"` + token + `"}`
		rec := doReq(http.MethodPost, "/api/login", payload, nil)
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
			t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}

	workerCookie := login(worker.Token)

	// 1. HTTP 403 Forbidden: Valid authenticated member lacking role permission
	// Worker attempts to access owner-only replica synchronization receipts
	rec403 := doReq(http.MethodGet, "/api/projects/"+project.ID+"/replicas", "", workerCookie)
	if rec403.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for role unauthorized action, got %d: %s", rec403.Code, rec403.Body.String())
	}
	if !strings.Contains(rec403.Body.String(), "project role is not allowed to perform that action") {
		t.Fatalf("expected role denial message, got: %s", rec403.Body.String())
	}

	// Worker attempts to modify project settings (requires settings.edit permission)
	settingsPayload := `{"settings":{"max_parallel":4,"max_lease_seconds":300,"summary_token_target":1000,"default_review_mode":"report_first"}}`
	settings403 := doReq(http.MethodPost, "/api/projects/"+project.ID+"/settings", settingsPayload, workerCookie)
	if settings403.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for settings mutation without permission, got %d: %s", settings403.Code, settings403.Body.String())
	}

	// 2. HTTP 401 Unauthorized: Access loss via credential/principal revocation
	// Owner revokes the worker principal
	ctx := context.Background()
	if err := s.RevokePrincipal(ctx, project.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("failed to revoke principal: %v", err)
	}

	// Worker tries to access plan using the now-revoked credential session
	rec401Revoked := doReq(http.MethodGet, "/api/projects/"+project.ID+"/plan", "", workerCookie)
	if rec401Revoked.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized after credential revocation, got %d: %s", rec401Revoked.Code, rec401Revoked.Body.String())
	}
	if !strings.Contains(rec401Revoked.Body.String(), "project access is no longer valid; reconnect a credential for this project") {
		t.Fatalf("expected access no longer valid message, got: %s", rec401Revoked.Body.String())
	}

	// 3. HTTP 401 Unauthorized: Unauthenticated request without session cookie
	rec401Missing := doReq(http.MethodGet, "/api/projects/"+project.ID+"/plan", "", nil)
	if rec401Missing.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized for missing session, got %d: %s", rec401Missing.Code, rec401Missing.Body.String())
	}
	if !strings.Contains(rec401Missing.Body.String(), "no project credential is connected") {
		t.Fatalf("expected missing credential message, got: %s", rec401Missing.Body.String())
	}
}
