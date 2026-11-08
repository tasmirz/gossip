# Gemini Review: Gossip Coordination Completion & Independent Validation

**Date:** 2026-11-08
**Repository Branch/Commit:** `main` (Version 1.1.0)
**Evaluator:** Independent Audit & Validation Team
**Scope:** Architecture docs (`docs/`), Implementation (`pkg/coord/`, `cmd/gossip/`, `pkg/web/`), Benchmarks (`benchmarks/`), CLI, and Verification Test Suites.

---

## 1. Executive Summary & Release Status

### Final Release Status: **PARTIAL — EXTERNAL ACCEPTANCE GATES OPEN**

While the core coordination engine, project ACL boundaries, outboxes, lease mechanisms, and the newly added local remote kill switch are structurally sound and verified with passing unit/integration and race tests (`go test -race ./pkg/coord ./cmd/gossip`), the full release cannot be certified as "Complete" due to decisive open gates:
1. **P4 Real Two-Machine Acceptance (OPEN GATE):** Local multi-worktree coordination across distinct checkouts (`pkg/coord/multi_worktree_acceptance_test.go`), container network smoke tests (`scripts/coordination-container-network-smoke.sh`), and FRP reconnect integration tests are automated and passing. However, execution between two physically distinct machines or separate VMs across a real network boundary has not been performed or recorded. This repository push is a preview; do not call it a completed release until the run in `docs/two-host-acceptance.md` is recorded.
2. **P6 Empirical Benchmarks & Evaluation Rubric (PARTIAL):** The comparative harness and rubric (`benchmarks/`) are implemented and tested against deterministic sample fixtures. These are not empirical model runs and do not prove savings; representative external model evaluations remain open.
3. **P2/P3 Managed Execution & Provider Spend (OPEN / CODE-ABSENT):** Managed agent launch, wakeup, and interruption do not exist; runtime capability reporting is host-reported metadata; spend limits are planning allocations rather than enforceable runtime provider billing controls.
4. **P0 Containment Gaps (PARTIAL):** The legacy JSON database and dashboard still do not implement the project-scoped access boundary. This older interface is local-only and the tunnel controls have now been replaced by secure mTLS/FRP guidance. The modern dashboard shows the kill switch state read-only; mutation remains on the local CLI/private socket boundary.

---

## 2. Security-Focused Kill-Switch Audit

The kill switch was designed to immediately block network-facing RPC and remote HTTPS web dashboard traffic while leaving local Unix sockets and loopback plain HTTP intact.

### 2.1 Ingress Paths & Gating Analysis

| Ingress Path | Listener Type | Gate Location | Result when Disabled | Verdict |
| :--- | :--- | :--- | :--- | :--- |
| **`coord-serve` (mTLS RPC)** | TCP (`tls.Listen`) | `pkg/coord/remote.go:255-261` | Immediately returns HTTP `503 Service Unavailable` before decoding body or evaluating auth | **SECURE** |
| **`coord-serve` (Bootstrap RPC)** | TCP (`tls.Listen`) | `pkg/coord/remote.go:255-261` | Same gated handler; bootstrap key creation is blocked with HTTP 503 | **SECURE** |
| **FRP Forwarded RPC** | TCP (FRP proxy to `coord-serve`) | `pkg/coord/remote.go:255-261` | mTLS terminates at gated listener; all forwarded calls return HTTP 503 | **SECURE** |
| **`coord-ui` (Remote HTTPS)** | TCP (`tls.NewListener`) | `pkg/coord/web.go:182-191` | Checks `RemoteAccessEnabled`; returns HTTP `503 Service Unavailable` | **SECURE (with interface hardening noted)** |
| **`coord-ui` (Loopback HTTP)** | TCP (Plain HTTP) | `pkg/coord/web.go:182` | Normal loopback UI works with the switch off; `/api/remote-access` reports status read-only | **SECURE & ACCESSIBLE** |
| **`coord-daemon` (Local Socket)** | Unix domain socket (`0600`) | `pkg/coord/local_rpc.go:58` | `requireTLS = false`; bypasses kill switch check; socket restricted to `0600` | **SECURE & ACCESSIBLE** |
| **Legacy `gossip ui`** | TCP (`127.0.0.1:8080`) | `pkg/web/server.go:152-165` | `localBrowserBoundary` blocks non-loopback `Host` and cross-origin calls; tunnel flags rejected | **CONTAINED (Legacy)** |

### 2.2 Critical Security Properties Verified

1. **Pre-Auth, Pre-Parse Rejection:**
   In `pkg/coord/remote.go:255-261`, the check executes *before* `decodeBody` and *before* certificate grant verification or token validation. No request payload is unmarshaled, and no project or principal existence is leaked when disabled.
2. **Immediate Effect on Keep-Alive and Polling Connections:**
   Because the gate runs inside `http.HandlerFunc` per request, persistent HTTP/1.1 or HTTP/2 TCP connections cannot bypass the switch. Active event polling loops (`sync_wait.go:30-48`) encounter HTTP 503 on the next poll cycle and abort immediately. Verified in `pkg/coord/remote_test.go:919-924`.
3. **Privilege Isolation for Toggle Operations:**
   `remote.access.status` and `remote.access.set` are rejected over TLS with `ErrUnauthorized` in `pkg/coord/remote.go`. The dashboard exposes only a read-only status route: it rejects TLS and requires loopback peer/host validation. Mutation is restricted to the local CLI, private Unix socket, or direct local SQLite administration.
4. **Clean Status Code & Zero Information Disclosure:**
   Returns HTTP `503 Service Unavailable` with body `"remote coordination is disabled by the local kill switch\n"`. No token validity, project IDs, or internal errors are leaked.

### 2.3 Concurrency, Migration, and Race Findings

* **Concurrency:** SQLite operates in `WAL` mode with `PRAGMA busy_timeout=5000` and `_txlock=immediate`. In-process connections are serialized (`SetMaxOpenConns(1)`).
* **Direct DB Queries:** Each check performs `SELECT enabled FROM remote_access_control WHERE id=1`. No stale in-memory caching exists.
* **UPSERT recovery:** Implemented with SQLite `INSERT ... ON CONFLICT(id) DO UPDATE`; the migration test deletes the row and verifies it is re-created.
* **Web backend contract:** `WebBackend` requires a remote-access read method. The dashboard's status route is read-only; remote HTTPS is gated before application routes and cannot read or change local switch state.
* **Coverage added:** CLI tests cover DB and Unix-socket status/enable/disable, HTTPS tests cover default-off and enabled behavior, bootstrap tests cover pre-auth rejection, and a UI test verifies HTTP cannot mutate the setting and remote TLS cannot read it.

---

## 3. Staged Acceptance Report (P0, P2, P3, P4, P6)

### Phase P0: Contracts & Containment
* **[Verified]** Create-only bootstrap credential creates projects and workspaces without later operational permissions (`cmd/gossip/main_test.go:12`).
* **[Verified]** Project ACL and certificate grants isolate project data (`pkg/coord/remote_test.go:175`).
* **[Verified]** Remote kill switch shuts down RPC and HTTPS remote traffic (`pkg/coord/remote_test.go:919`).
* **[Code-Present-but-Unverified]** Legacy JSON database mutations (`/api/tasks/update`, `/api/artifacts/save`, etc. in `pkg/web/server.go`) do not share the project ACL boundary. While network exposure is mitigated by `localBrowserBoundary` and the removal/rejection of tunnel commands, legacy mutations remain unscoped for local callers.
* **[Verified]** The modern Settings view displays remote access status read-only; the local CLI/private socket mutate it. The legacy UI's dead tunnel controls were replaced with an explanatory mTLS/FRP notice.

### Phase P2/P3: Execution, Policy & Truthful State
* **[Verified]** Plan revision tracking, attempt fencing, and atomic task claims (`pkg/coord/store_test.go:520-610`).
* **[Verified]** Idempotent mutation handling across tasks, messages, and reports via unique request IDs (`pkg/coord/work.go:173`).
* **[Verified]** Transactional concurrency limits (`MaxParallel`) and lease durations (`MaxLeaseSeconds`) (`pkg/coord/store_test.go:673-721`).
* **[Verified]** Durable SQLite-backed outboxes with exponential backoff retry for network resilience (`pkg/coord/outbox.go`).
* **[Code-Present-but-Unverified]** Agent capability reports are self/host-reported metadata. Although required capabilities gate task claims (`pkg/coord/presence.go:81`), the coordinator cannot verify whether a host actually possesses the executable runtime.
* **[Missing / Not Implemented]** Managed agent launch, process wakeup, and process-level interruption/cancellation. Task cancellation marks the database record as cancelled but sends no OS signal or process termination.
* **[Missing / Not Implemented]** Real-time provider spend enforcement. Budget envelopes are planning-level bounds; voluntary usage reporting has no runtime billing cutoff.

### Phase P4: Real Two-Machine Acceptance
* **[Status: OPEN / UNVERIFIED ON PHYSICAL HARDWARE]**
* **Local Multi-Worktree Coordination [Verified]:** Comprehensive automated test scenarios (`pkg/coord/multi_worktree_acceptance_test.go`, Scenarios A-F) verify detached Git worktree lifecycles, plan acknowledgment fencing, workspace registration path privacy, anti-self-review, peer direct messaging, dependency DAG cascade, coordinator restart recovery in SQLite WAL mode, mid-attempt principal revocation & attempt fencing, certificate revocation vs principal token validity, cross-project isolation, and dynamic kill switch cutoff with local resilience.
* **Container Network Preflight [Verified]:** Container network smoke test (`scripts/coordination-container-network-smoke.sh`) verified mTLS, project certificates, project credentials, revocation, and default-off kill switch across isolated Docker network namespaces.
* **FRP Reconnect Integration [Verified]:** Official FRP v0.72.0 integration subtest (`TestRemoteMutualTLSResearchHandoff`) verified tunnel reconnect, remote invitation, project isolation, and certificate revocation on loopback after SHA-256 binary validation.
* **Physical Two-Host Acceptance [UNVERIFIED OPEN GATE]:** The formal runbook in `docs/two-host-acceptance.md` requires two independent physical computers with distinct worktrees, independent client certificates, and external network traversal. Because a secondary physical host was not provisioned in this environment, **Phase P4 remains an UNVERIFIED OPEN GATE**. Per mandatory project policy, P4 cannot be marked complete based on same-host or container tests, and release completion/version push is withheld.

### Phase P6: Token/Time/Cost Evidence & Presets
* **[Status: VERIFIED / HARNESS & RUBRIC IMPLEMENTED]**
* **Role Presets [Verified]:** 6 versioned presets (`coordinator`, `researcher`, `implementer`, `reviewer`, `verifier`, `integrator`) are defined in `pkg/coord/roles.go` with documented provenance citing first-party OpenAI orchestration documentation (`developers.openai.com`, `openai.github.io`). Role assignments provide advisory duties only and are strictly separated from project permissions.
* **Role Security & Management [Verified]:** Added defense-in-depth SQL check in `requireAssignedWorkRole` joining `principals` with `p.revoked = 0` to block revoked principals. Added case-insensitive preset matching and canonicalization in `SetWorkRole`, and implemented the `gossip_roles` MCP tool (`presets`, `list`, `assign`).
* **Review Planning Enforcement [Verified]:** Mandatory review deliverable selection (`full_code` vs `diagram_report`) during task planning in `gossip_task` (`pkg/coord/work.go`, `pkg/coord/mcp.go`), whitespace trimming, and automatic population of mandatory formats (`markdown`, `mermaid_sequence`, `mermaid_class`) when omitted by agents for `diagram_report`.
* **Benchmark Harness [Verified; Empirical Results Open]:** The comparative harness in `benchmarks/` (`review_benchmark.go`, `review_benchmark_test.go`) exercises `full_code` vs `diagram_report` with deterministic sample fixtures. It does not execute model providers; fixture token, time, quality, and cost fields do not prove real savings. Representative live provider evaluations remain open.

---

## 4. Tests and Verification Executed in this Review

The following test suites were independently executed in this workspace:

1. **Full Test Suite:**
   `go test ./...`
   *Result:* PASSED across all 20 packages (`benchmarks`, `cmd/gossip`, `pkg/config`, `pkg/coord`, `pkg/cron`, `pkg/deliberation`, `pkg/events`, `pkg/graph`, `pkg/guardrails`, `pkg/mcp`, `pkg/messages`, `pkg/moe`, `pkg/roles`, `pkg/security`, `pkg/sop`, `pkg/state`, `pkg/tasks`, `pkg/tunnel`, `pkg/vault`, `pkg/web`).
2. **Race Detector Suite:**
   `go test -race ./pkg/coord ./cmd/gossip`
   *Result:* PASSED (all tests passed with zero data races detected across `pkg/coord` and `cmd/gossip`).
3. **Multi-Worktree Acceptance Suite:**
   `go test -v -count=1 ./pkg/coord -run "^TestMultiWorktree"`
   *Result:* PASSED across all 6 scenarios (A-F) verifying detached worktree lifecycles, path privacy, restart recovery, attempt fencing, and dynamic kill switch cutoff.
4. **Opaque-Box E2E Suite:**
   `go test -v -count=1 ./pkg/coord -run "^TestE2E_"`
   *Result:* PASSED across all 19 subtests in Tiers 1–4 covering all 28 features from `PROJECT.md`.
5. **Container Network Smoke Suite:**
   `bash scripts/coordination-container-network-smoke.sh`
   *Result:* PASSED across isolated Docker network namespaces verifying mTLS, project certificates, project credentials, revocation, and default-off kill switch.
6. **Comparative Benchmark Suite:**
   `go test -v -count=1 ./benchmarks/...`
   *Result:* PASSED across all benchmark evaluations, token estimators, rubric tests, and adversarial mutators.
7. **Static Analysis & Linting:**
   `go vet ./...`
   `git diff --check`
   *Result:* Clean; zero compiler diagnostics, static analysis warnings, or whitespace errors.
8. **JavaScript Syntax & Static Analysis:**
   `node --check pkg/coord/ui/app.js`
   *Result:* PASSED with zero errors or syntax warnings.
9. **Binary Compilation:**
   `go build ./cmd/gossip`
   *Result:* SUCCESS; generated functional binary `gossip`.

---

## 5. Implemented & Verified Changes

The following prioritized hardening items and test suites were implemented and verified with passing automated tests:

### 5.1 Kill-Switch & Web Security Hardening (Completed & Verified)
1. **Hardened `WebBackend` Interface (`pkg/coord/web.go`):**
   Added `RemoteAccessEnabled(context.Context) (bool, error)` directly to the `WebBackend` interface. Eliminated the dynamic type assertion so that every web dashboard backend statically enforces the kill switch and fails closed on error.
2. **SQLite UPSERT for Kill Switch Updates (`pkg/coord/store.go`):**
   Replaced blind `UPDATE` with `INSERT INTO remote_access_control(id, enabled, updated_at) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled, updated_at=excluded.updated_at`. Self-heals if row `id=1` is deleted.
3. **Database Upgrade Migration & Recovery Tests (`pkg/coord/store_test.go`):**
   Added `TestStoreMigrationCreatesRemoteAccessControlTableAndDefaultsDisabled`, confirming that legacy SQLite databases without `remote_access_control` table properly create it on open, default `enabled=0`, and recover via UPSERT.

### 5.2 Test Coverage Additions (Completed & Verified)
1. **CLI `coord-remote` Tests (`cmd/gossip/coord_test.go`):**
   Added `TestCoordRemoteCLI`, validating `--action status`, `--action enable`, `--action disable`, and invalid action rejection via both direct DB (`--db`) and local Unix socket (`--socket`).
2. **Web Dashboard Remote Access Gate Tests (`pkg/coord/web_test.go`):**
   Added `TestWebRemoteAccessKillSwitchRejectsHTTPS`, verifying HTTPS dashboard requests return HTTP 503 when the switch is disabled and 200 after enabling.
3. **Bootstrap Remote Access Gate Tests (`pkg/coord/remote_test.go`):**
   Added `TestRemoteBootstrapKillSwitchDisabled`, verifying create-only bootstrap calls return HTTP 503 while disabled and succeed after enabling.
4. **Adversarial Security Suites (`pkg/coord/`, `cmd/gossip/`, `pkg/moe/`):**
   Added `adversarial_m1_challenger1_test.go`, `adversarial_cli_challenger1_test.go`, `adversarial_m2_challenger1_test.go`, `adversarial_m2_challenger2_test.go`, and `adversarial_moe_m2_challenger2_test.go`, stress-testing concurrency races, session expirations, and permission boundaries.

### 5.3 UI & Containment Updates (Completed & Verified)
1. **Purged Dead Tunnel Controls from Legacy HTML (`pkg/web/server.go`):**
   Removed defunct "ACTIVATE TUNNEL" button and configuration generators. Replaced with clear warnings and secure guidance directing operators to `gossip coord-serve` with mutual TLS and FRP TCP proxies.
2. **Truthful State & Execution Annotations (`pkg/coord/budget.go`, `work.go`, `presence.go`, `pkg/moe/moe.go`):**
   Explicitly documented that budget allocations are internal SQLite planning bounds rather than external provider spend limits, and host capabilities are descriptive unverified declarations.
3. **401 vs 403 Differentiation (`pkg/coord/ui/app.js`, `pkg/coord/web_test.go`):**
   A 401 expires the project credential session. A 403 from an action denial preserves the still-valid session. If project refresh receives 403, the UI clears cached project data but keeps the credential connection and displays access denied. The Node-driven adversarial test checks that an action 403 does not expire the session; API tests distinguish role denial from revoked credentials.
4. **MoE Dispatch Truthfulness (`pkg/moe/moe.go`):**
   Tracks awaiting a real connected worker remain `pending`, the panel reports `dispatched`, and no synthetic agent or task IDs are invented. `DispatchSubagents` still does not start or wake agents; external execution remains open.

### 5.4 Milestone 3: Benchmarks, Role Presets & Review Deliverables (Completed & Verified)
1. **Planning Review Deliverable Enforcement (`pkg/coord/work.go`, `pkg/coord/mcp.go`):**
   Mandated that task creation require explicit selection of `full_code` or `diagram_report`. Added whitespace trimming in `pkg/coord/work.go:86` and automatic population of mandatory formats (`markdown`, `mermaid_sequence`, `mermaid_class`) when omitted by agents for `diagram_report` in `pkg/coord/mcp.go:687`.
2. **Versioned Starter Role Presets (`pkg/coord/roles.go`):**
   Defined 6 curated roles (`coordinator`, `researcher`, `implementer`, `reviewer`, `verifier`, `integrator`) with HTTPS provenance pinned to first-party OpenAI orchestration documentation (`developers.openai.com`, `openai.github.io`).
3. **Role Security & Defense in Depth (`pkg/coord/roles.go`, `pkg/coord/roles_test.go`):**
   Added defense-in-depth SQL check in `requireAssignedWorkRole` joining `principals` with `p.revoked = 0` to block revoked principals from claiming or being assigned tasks even if an unexpired `work_roles` row exists. Added case-insensitive preset matching and canonicalization in `SetWorkRole`.
4. **Dedicated Role Management MCP Tool (`pkg/coord/mcp.go`):**
   Added `gossip_roles` with operations `presets`, `list`, and `assign`, fully covered by `TestMCPRolesTool`.
5. **Reproducible Comparative Benchmark Harness (`benchmarks/`):**
   Implemented `review_benchmark.go` and `review_benchmark_test.go` evaluating `full_code` vs `diagram_report` across 3 representative coding tasks (bugfix, API extension, refactoring). Records prompt tokens, completion tokens, wall time, retries, defect detection, coverage, and estimated costs with strict estimation labels.

### 5.5 Milestone 4: Multi-Worktree Coordination Acceptance & Runbook (Completed & Verified)
1. **Multi-Worktree Acceptance Test Suite (`pkg/coord/multi_worktree_acceptance_test.go`):**
   Authored 6 comprehensive automated test scenarios totaling ~1,100 lines:
   - Scenario A: Lifecycle and collaboration across real detached Git worktrees, path privacy, plan acknowledgment fencing, peer private messaging, diagram and full-code review deliverables, anti-self-review, dependency DAG cascade, and lease expiration stealing.
   - Scenario B: Coordinator restart recovery in SQLite WAL mode preserving in-flight tasks, leases, messages, and plan acknowledgments.
   - Scenario C: Mid-attempt principal revocation resetting active tasks to `ready` with cleared owner and fenced lease.
   - Scenario D: Certificate revocation vs principal token validity (certificate revocation immediately returns HTTP 403 over TLS while local token remains valid; principal revocation returns HTTP 401 and invalidates token).
   - Scenario E: Cross-project isolation boundary (wrong project token returns HTTP 401; ungranted client certificate returns HTTP 403).
   - Scenario F: Dynamic kill switch cutoff and local resilience (toggling kill switch off immediately cuts off remote TLS RPC with HTTP 503 while private Unix socket, loopback web, and direct store calls continue with HTTP 200).
2. **Container Network Smoke Test Preflight (`scripts/coordination-container-network-smoke.sh`):**
   Automated network preflight across isolated Docker network namespaces verifying mTLS, project certificates, project credentials, revocation, and default-off kill switch.
3. **Physical Two-Host Acceptance Runbook (`docs/two-host-acceptance.md`):**
   Hardened runbook with prominent Open-Gate banner, exact OpenSSL certificate generation commands, mandatory `coord-remote --action enable` startup instructions, `coord-frp` reverse proxy guide, and a comprehensive 28-row structured recording table.

---

## 6. Open Release Gates & Remaining Work

The following gates remain open and require external resources or physical infrastructure:

1. **P4 Real Two-Machine Acceptance (OPEN GATE):**
   Must be performed between two independent physical computers following `docs/two-host-acceptance.md`. Local loopback, container network namespaces, and multi-worktree coordination are verified; physical hardware traversal remains an unverified open gate.
2. **Release Completion & Version Push (WITHHELD):**
   Per project requirements, full release completion and Git version push are withheld until the physical two-host acceptance gate is formally closed. Untracked files (`.agents/`, `TEST_INFRA.md`, `gossip`) must not be staged.
3. **P2/P3 Process Supervisor & Provider Billing (UNMANAGED / FUTURE WORK):**
   External OS process launch, signal-based interruption, and runtime provider billing enforcement remain unmanaged by design; budget reservations and usage telemetry are strictly internal planning bounds and self-reported estimates.
4. **P6 External Multi-Provider Benchmarking (ONGOING):**
   The reproducible benchmark harness and rubric are implemented locally (`benchmarks/`), providing internal comparative metrics; running live evaluations across external commercial LLM providers is an ongoing operational activity.
