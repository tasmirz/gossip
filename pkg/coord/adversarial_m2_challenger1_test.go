package coord_test

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
)

// TestAdversarialM2_WebBackendInterfaceCompileTimeSafety verifies that:
// 1. WebBackend interface declares CreateProjectWithWorkspace and RemoteAccessEnabled.
// 2. pkg/coord/web.go has ZERO runtime type assertions in createLocalProject and ServeHTTP.
func TestAdversarialM2_WebBackendInterfaceCompileTimeSafety(t *testing.T) {
	// 1. Reflection verification of WebBackend interface methods
	webBackendType := reflect.TypeOf((*coord.WebBackend)(nil)).Elem()

	createMethod, ok := webBackendType.MethodByName("CreateProjectWithWorkspace")
	if !ok {
		t.Fatalf("CRITICAL: WebBackend interface does not contain CreateProjectWithWorkspace")
	}
	if createMethod.Type.NumIn() != 6 || createMethod.Type.NumOut() != 5 {
		t.Fatalf("unexpected signature for CreateProjectWithWorkspace on WebBackend: %v", createMethod.Type)
	}

	remoteAccessMethod, ok := webBackendType.MethodByName("RemoteAccessEnabled")
	if !ok {
		t.Fatalf("CRITICAL: WebBackend interface does not contain RemoteAccessEnabled")
	}
	if remoteAccessMethod.Type.NumIn() != 1 || remoteAccessMethod.Type.NumOut() != 2 {
		t.Fatalf("unexpected signature for RemoteAccessEnabled on WebBackend: %v", remoteAccessMethod.Type)
	}

	// 2. AST inspection of pkg/coord/web.go to confirm no dynamic type assertions exist
	fset := token.NewFileSet()
	webGoPath := filepath.Join("web.go")
	node, err := parser.ParseFile(fset, webGoPath, nil, 0)
	if err != nil {
		t.Fatalf("failed to parse web.go: %v", err)
	}

	var createLocalProjectFunc *ast.FuncDecl
	var serveHTTPFunc *ast.FuncDecl

	ast.Inspect(node, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if fn.Name.Name == "createLocalProject" {
			createLocalProjectFunc = fn
		}
		if fn.Name.Name == "ServeHTTP" {
			serveHTTPFunc = fn
		}
		return true
	})

	if createLocalProjectFunc == nil {
		t.Fatalf("could not find createLocalProject in web.go")
	}
	if serveHTTPFunc == nil {
		t.Fatalf("could not find ServeHTTP in web.go")
	}

	// Verify no TypeAssertExpr exists in createLocalProject
	ast.Inspect(createLocalProjectFunc, func(n ast.Node) bool {
		if ta, ok := n.(*ast.TypeAssertExpr); ok {
			t.Errorf("FAIL: Found unexpected runtime type assertion in createLocalProject: %v", ta)
		}
		return true
	})

	// Verify no TypeAssertExpr on store for RemoteAccessEnabled in ServeHTTP
	ast.Inspect(serveHTTPFunc, func(n ast.Node) bool {
		if ta, ok := n.(*ast.TypeAssertExpr); ok {
			var buf bytes.Buffer
			_ = ast.Fprint(&buf, fset, ta, nil)
			if strings.Contains(buf.String(), "RemoteAccessEnabled") {
				t.Errorf("FAIL: Found unexpected runtime type assertion on RemoteAccessEnabled in ServeHTTP: %s", buf.String())
			}
		}
		return true
	})
}

// TestAdversarialM2_Distinct401vs403AccessLossMatrix validates
// all permutations of 401 unauthenticated vs 403 forbidden access codes.
func TestAdversarialM2_Distinct401vs403AccessLossMatrix(t *testing.T) {
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

	// Scenario 1: Unauthenticated request to project endpoint -> 401
	recMissing := doReq(http.MethodGet, "/api/projects/"+project.ID+"/tasks", "", nil)
	if recMissing.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated request, got %d", recMissing.Code)
	}
	if !strings.Contains(recMissing.Body.String(), "no project credential is connected") {
		t.Fatalf("unexpected body: %s", recMissing.Body.String())
	}

	// Scenario 2: Valid worker attempting owner-only GET /replicas -> 403
	recReplicas := doReq(http.MethodGet, "/api/projects/"+project.ID+"/replicas", "", workerCookie)
	if recReplicas.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for replicas, got %d", recReplicas.Code)
	}
	if !strings.Contains(recReplicas.Body.String(), "not allowed to perform that action") {
		t.Fatalf("expected role denial message, got: %s", recReplicas.Body.String())
	}

	// Scenario 3: Valid worker attempting owner-only POST /settings -> 403
	settingsPayload := `{"settings":{"max_parallel":8,"max_lease_seconds":300,"summary_token_target":2000,"default_review_mode":"report_first"}}`
	recSettings := doReq(http.MethodPost, "/api/projects/"+project.ID+"/settings", settingsPayload, workerCookie)
	if recSettings.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for settings, got %d", recSettings.Code)
	}

	// Scenario 4: Valid worker attempting owner-only POST /roles -> 403
	rolePayload := `{"recipient":"` + worker.PrincipalID + `","name":"admin","body":"root","revision":0}`
	recRole := doReq(http.MethodPost, "/api/projects/"+project.ID+"/roles", rolePayload, workerCookie)
	if recRole.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for role assignment, got %d", recRole.Code)
	}

	// Scenario 5: Valid worker attempting owner-only POST /certificates -> 403
	certPayload := `{"operation":"grant","fingerprint":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`
	recCert := doReq(http.MethodPost, "/api/projects/"+project.ID+"/certificates", certPayload, workerCookie)
	if recCert.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for cert grant, got %d", recCert.Code)
	}

	// Scenario 6: Revoked principal attempting GET /plan -> 401
	ctx := context.Background()
	if err := s.RevokePrincipal(ctx, project.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("failed to revoke principal: %v", err)
	}
	recRevoked := doReq(http.MethodGet, "/api/projects/"+project.ID+"/plan", "", workerCookie)
	if recRevoked.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for revoked principal, got %d", recRevoked.Code)
	}
	if !strings.Contains(recRevoked.Body.String(), "project access is no longer valid") {
		t.Fatalf("unexpected body: %s", recRevoked.Body.String())
	}

	// Scenario 7: Non-loopback attempt to create local project -> 403
	nonLoopbackReq := httptest.NewRequest(http.MethodPost, "http://192.168.1.50:8081/api/projects/create", strings.NewReader(`{"name":"test","local_path":"/tmp"}`))
	nonLoopbackReq.RemoteAddr = "192.168.1.100:12345"
	nonLoopbackReq.Header.Set("Content-Type", "application/json")
	nonLoopbackReq.Header.Set("Origin", "http://192.168.1.50:8081")
	recNonLoopback := httptest.NewRecorder()
	h.ServeHTTP(recNonLoopback, nonLoopbackReq)
	if recNonLoopback.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback project creation, got %d", recNonLoopback.Code)
	}
}

// TestAdversarialM2_AppJSInNodeVMEmpiricalChallenge runs app.js directly in Node.js
// to stress-test in-memory state wiping and unmask the refresh() render() TypeError bug.
func TestAdversarialM2_AppJSInNodeVMEmpiricalChallenge(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node executable not found in PATH")
	}

	appJsPath, err := filepath.Abs(filepath.Join("ui", "app.js"))
	if err != nil {
		t.Fatal(err)
	}

	jsScript := fmt.Sprintf(`
const fs = require('fs');
const vm = require('vm');

const appJsPath = %q;
const code = fs.readFileSync(appJsPath, 'utf8');

function createMockEl() {
  const handler = {
    get(target, prop) {
      if (prop in target) return target[prop];
      if (prop === 'elements') {
        return new Proxy({}, { get: (t, p) => createMockEl() });
      }
      if (prop === 'classList') {
        return { toggle: () => {}, add: () => {}, remove: () => {} };
      }
      if (prop === 'options') return [];
      if (prop === 'value') return '';
      return () => createMockEl();
    }
  };
  return new Proxy({
    insertAdjacentHTML: () => {},
    addEventListener: () => {},
    appendChild: () => {},
    querySelectorAll: () => [],
    querySelector: () => createMockEl(),
    setAttribute: () => {},
    style: {}
  }, handler);
}

const mockEl = createMockEl();
const document = {
  querySelector: () => mockEl,
  querySelectorAll: () => [],
  body: mockEl,
  addEventListener: () => {},
  getElementById: () => mockEl
};
const window = { prompt: () => '', addEventListener: () => {} };
const location = { search: '', pathname: '/' };
const history = { replaceState: () => {} };

let fetchMock = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

const context = {
  document, window, location, history, URL, Blob, URLSearchParams, crypto,
  setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, console, CSS: { escape: s => s },
  fetch: (...args) => fetchMock(...args)
};
vm.createContext(context);
vm.runInContext(code, context);

async function run() {
  // 1. Verify that expireProjectSession wipes all 5 required sensitive state arrays/objects
  vm.runInContext("state.project = { project_id: 'proj-wipe', project_name: 'Wipe Me', role: 'owner', id: 'prin-1' }; state.tasks = [{ id: 't1', title: 'Sensitive Task' }]; state.plan = { body: 'Sensitive Plan', revision: 1 }; state.messages = [{ body: 'Sensitive Chat', id: 'm1' }]; state.repositories = [{ id: 'r1', name: 'Sensitive Repo' }]; state.workspaces = [{ id: 'w1', name: 'Sensitive Workspace' }]; expireProjectSession('proj-wipe');", context);

  const state1 = vm.runInContext('state', context);
  if (state1.project !== null) throw new Error('state.project not null');
  if (state1.plan !== null) throw new Error('state.plan not null');
  if (state1.tasks.length !== 0) throw new Error('state.tasks not empty');
  if (state1.messages.length !== 0) throw new Error('state.messages not empty');
  if (state1.repositories.length !== 0) throw new Error('state.repositories not empty');
  if (state1.workspaces.length !== 0) throw new Error('state.workspaces not empty');
  console.log('CHECK 1 OK: expireProjectSession explicitly wiped tasks, plan, messages, repositories, workspaces.');

  // 2. Verify that HTTP 403 from an owner-only action preserves a valid project session
  vm.runInContext("state.project = { project_id: 'proj-403', project_name: '403 Proj', role: 'worker', id: 'prin-2' }; state.tasks = [{ id: 't2', title: 'Task 2' }]; state.plan = { body: 'Plan 2' }; state.messages = [{ body: 'Msg 2' }]; state.repositories = [{ id: 'r2' }]; state.workspaces = [{ id: 'w2' }];", context);

  fetchMock = async () => ({
    ok: false,
    status: 403,
    text: async () => 'this project role is not allowed to perform that action'
  });

  try {
    await vm.runInContext("request('/api/projects/proj-403/replicas')", context);
  } catch(e) {}

  const state2 = vm.runInContext('state', context);
  if (state2.project === null || state2.tasks.length !== 1 || state2.plan === null) {
    throw new Error('HTTP 403 incorrectly expired a valid project session');
  }
  console.log('CHECK 2 OK: HTTP 403 from a restricted action preserved the valid project session.');

  // 3. EMPIRICAL BUG DETECTION: Test refresh() catch block when HTTP 401/403 occurs
  vm.runInContext("state.project = { project_id: 'proj-refresh', project_name: 'Refresh Proj', role: 'owner', id: 'prin-3' }; state.generation = 42; state.tasks = [{ id: 't3', title: 'Task 3' }];", context);

  fetchMock = async (url) => {
    if (url.includes('plan')) {
      return { ok: false, status: 401, text: async () => 'project access is no longer valid' };
    }
    return { ok: true, json: async () => [] };
  };

  let caughtErr = null;
  try {
    await vm.runInContext("refresh()", context);
  } catch(e) {
    caughtErr = e;
  }

  if (caughtErr) {
    console.log('CONFIRMED BUG IN REFRESH CATCH BLOCK:');
    console.log('Error:', caughtErr.name, caughtErr.message);
    process.exit(99);
  }
}

run().catch(err => {
  console.error(err);
  process.exit(1);
});
`, appJsPath)

	cmd := exec.Command(nodePath, "-e", jsScript)
	out, err := cmd.CombinedOutput()
	outputStr := string(out)
	t.Logf("Node.js test output:\n%s", outputStr)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 99 {
			t.Errorf("FAIL: refresh() catch block in app.js crashed with unhandled TypeError due to calling render() on null state.project:\n%s", outputStr)
			return
		}
		t.Fatalf("Node script failed unexpectedly: %v\n%s", err, outputStr)
	}
}
