# Gemini handoff: Gossip coordination completion and independent validation

## Mission

Continue the Gossip redesign to a verified, usable release. This is an existing Go repository and an existing implementation, not a greenfield design exercise. Start by reading only the two required architecture/source-map documents below, then check the current implementation and the status audit before proposing edits:

1. [`mcp-architecture-security-efficiency.md`](mcp-architecture-security-efficiency.md)
2. [`implementation-mechanics.md`](implementation-mechanics.md)
3. [`gossip-redesign-plan.md`](gossip-redesign-plan.md) for current phase evidence and open gates
4. [`coordination-preview.md`](coordination-preview.md) for actual user-facing setup and limitations
5. [`two-host-acceptance.md`](two-host-acceptance.md) for the multi-machine acceptance procedure

Do not assume prose in an older plan is current implementation truth. Inspect source and tests. Report evidence with file paths, symbols, and test names. Distinguish verified behavior, code-present-but-unverified behavior, and missing behavior.

## User intent and product requirements

The product is a friendly, project-aware coordination hub for multiple coding agents. A common user has two agents and wants to save time, tokens, and money by letting a stronger orchestrator assign bounded work, receive concise reports, ask follow-up questions, and decide when deeper source review is justified. Agents must also be able to work in parallel against one shared plan and communicate when their assumptions diverge.

Required experience:

- The ordinary starting point is a local Git checkout/folder. Inspect its Git metadata, infer repository/remotes/branch/head/dirty state, and create the project and first workspace together. Do not make users invent a project name or provide a Git URL first.
- MCP project creation is allowed only through the separate create-only bootstrap credential. The bootstrap key creates a project and initial workspace; it cannot perform later project operations. Save the resulting owner credential to a private file, mode 0600 on Unix.
- Project access controls answer **which projects** a principal or client certificate can access. Access is scoped per project; every project read/write/event/export must respect membership and explicit permissions. A role label is not an access grant.
- One authoritative home coordinator owns each project. LAN and FRP are authenticated transports to that home. Multi-master writes and authority handoff are outside this release.
- Local and remote machines may use different worktrees or clones, including different updates of the same branch. Track workspace identity, commit baseline, dirty state, plan revision, task attempt, report provenance, and exact review evidence. Do not rely on branch name as version identity.
- In planning, ask which review mode is desired if unspecified: `full_code` or a compact Markdown report containing UML class and sequence diagrams. Research/report first should allow the orchestrator to decide when source code should be examined.
- Roles and duties are adjustable per project/task and must remain separate from authorization permissions. Starter presets need curated sources and clear provenance; do not claim “best” until tested against real tasks/hosts.
- The web UI is a primary control surface. It must make agents, roles, goals, tasks, chat/conversations, activity, projects, workspaces, settings, review evidence, and access understandable and filterable by project. Navigation must work while disconnected; auth and project access failures must be surfaced accurately.
- Minimize repeated large context transfers: use bounded event deltas, paged evidence, concise reports, digest-pinned source snapshots, and explicit staged review.
- LAN/remote access must have strong identity and per-project authorization. The hard local remote kill switch is disabled on first run and must immediately block remote RPC and HTTPS UI, including traffic forwarded through FRP. Only a local DB or private Unix socket may enable it. Loopback UI remains usable while disabled.

## Recent release work: local remote kill switch

The current working change implements this behavior:

- SQLite table `remote_access_control`, initialized with `enabled=0` for new and upgraded databases.
- `gossip coord-remote --action status|enable|disable --db ...` for local database administration, or `--socket ...` when `coord-daemon` holds the database open.
- Request-by-request gate at the shared network RPC handler, before operation parsing/authorization; this includes create-only remote bootstrap and any RPC forwarded by FRP.
- Request-by-request gate for HTTPS dashboard requests. Loopback plain HTTP remains available.
- `remote.access.status` and `remote.access.set` are available only through the private Unix socket handler. TLS RPC explicitly rejects these operations.
- Regression coverage for disabled-by-default state, private-socket control, inability of a remote client object to change it, and immediate block of an already running remote listener.
- README and coordination setup docs explain activation and use.

Please independently review the kill switch for bypasses, concurrency/race issues, upgrade defaults, status-code behavior, UI distinction, and test coverage. Check specifically that every network-facing listener uses the shared gated handler and that no old cleartext tunnel path bypasses it. The legacy tunnel commands are intentionally rejected; existing manually started external FRP processes cannot be killed by Gossip itself.

## Repository and release context

- Repo: `https://github.com/tasmirz/gossip.git`
- Working branch: `main`
- Version constant was advanced to `1.1.0` for this release.
- The working tree contains the project-scoped coordination redesign built over earlier commits, not only the kill-switch patch. Review the complete diff and staged release contents before changing them.
- Do not force-push, reset, or discard user work. Do not commit secrets or local credentials.
- Local verification already completed for the current working state: `go test ./...`, `go vet ./...`, `git diff --check`, and `go build ./cmd/gossip` all passed. Focused tests also cover package and CLI behavior. Run them again after any modifications. Race testing is expected for changes to shared service/store behavior.
- Current `docs/gossip-redesign-plan.md` correctly states that the entire redesign is not complete. A prior `go test -race ./...` was recorded in the plan before this kill-switch patch; rerun after your changes.

## Existing implementation to orient yourself

Primary implementation is in `pkg/coord/` and `cmd/gossip/coord*.go`; the web UI assets are embedded from `pkg/coord/ui/`. MCP client adapters and tools are in the same coordination package. Search source/tests before editing rather than duplicating components.

High-value existing behaviors reported in the status audit include project-principal credentials, project action permissions, certificate grants, create-only bootstrap, Git-folder project setup, project-filtered dashboard views, local Unix socket administration, task/plan/review flows, two review modes, worktree baselines, paged history, durable outboxes/caches, and single-home RPC coordination. Confirm each claim in implementation and tests.

## Remaining work: complete gates, do not relabel as complete

The current phase audit is authoritative for what remains. Prioritize by evidence and user value:

### P0: close remaining containment gaps

- The legacy JSON database/dashboard still does not share the new project ACL and central policy boundary for all legacy mutations. Keep it clearly labeled local-only/legacy; verify it cannot be accidentally exposed through tunnel commands, installer defaults, reverse proxy guidance, or generated configs. If fully securing it is in scope, propose a concrete migration plan first.
- Audit documentation and UI wording so the remote kill switch is accurately described. Make sure users see that remote access is off until locally enabled.
- Validate schema migration on an existing DB and concurrent daemon/CLI access, including immediate setting visibility and private file/socket permissions.

### P2/P3: execution, policy, and truthful state

- Managed agent launch/wakeup and cancellation are not implemented. Current task/MoE dispatch can create assignments/tracks but does not start coding agents.
- Runtime/capability reports are self/host-reported and must not be presented as proof of executable capabilities.
- Settings for concurrency/budget/lease behavior need end-to-end enforcement evidence. Provider-reported usage is not independently verified spend; avoid presenting reservation as actual billing enforcement.
- Run focused audit of state labels and retry semantics: queued, dispatched, running, submitted, reviewed, accepted, failed, and disconnected must reflect observed reality.

### P4: real two-machine acceptance

- Current recorded remote tests and FRP integration ran on one physical host. Execute [`two-host-acceptance.md`](two-host-acceptance.md) using two separate physical machines or virtual machines with independently provisioned credentials.
- Validate a real two-agent flow across distinct worktrees, including plan acknowledgment, chat, research, report-first and full-code review, disconnect/reconnect, home process restart, certificate revoke, membership revoke, and isolation from an unrelated project.
- Exercise kill switch off by default, locally enable, remote access through LAN, remote access through FRP TCP, disable while clients are connected, and confirm subsequent requests fail immediately while local UI and private-socket administration still work.
- Record OS/network setup, build/version, commands, outcomes, and failures in an acceptance log. Do not mark P4 complete based only on mocks or same-host tests.

### P6: evidence for token/time/cost claims and role presets

- There is no representative cost/latency/token benchmark proving savings. Design and run small reproducible tasks comparing report-first/diagram review with full-source review. Record models/settings, prompts, input/output tokens, elapsed time, retries, quality rubric, and limitations. If provider billing cannot be verified, label token data as reported estimates.
- Starter roles are Gossip-authored candidates. Evaluate them across representative coding-agent hosts and tasks before claiming they are best. Verify online references are first-party/current and pin/source them; never fetch untrusted skill instructions as privileged policy.
- Web UI should make the report-first workflow and settings easy without hiding permission or baseline details.

### UI and integration polish

- Verify every navigation action and project filter in browser, both connected and disconnected; verify 401 invalid credential vs 403 permission denial and no stale sensitive data after access loss.
- The reported browser issues included CSP inline style warnings, navigation buttons not switching views, an “unauthorized” toast when not connected, and inability to connect a local folder. Fixes were made earlier in this work; reproduce current behavior and add regression evidence if possible. The expected first-run action is **Create local project / Connect a code folder**, with repository info inferred from the selected checkout.
- Test supported browsers at common viewport sizes and keyboard navigation. Keep CSP strict; do not solve injected-style warnings by broadly adding `unsafe-inline` to the primary application CSP.
- Make project filter selection explicit and ensure access administration always names the exact project being changed.

## Required workflow for your work

1. Read the two design/source documents and the current status table. Inspect `git status`, current commit/version, and whole diff. Preserve all current changes.
2. Return a short findings report first: confirmed complete, gaps, severity, relevant files/tests, and an ordered plan. Clearly identify work that requires two physical machines or provider credentials.
3. Implement bounded, evidence-backed fixes autonomously. Avoid broad rewrites that risk the already functioning workflow. Add/update tests for changed behavior.
4. Run `gofmt`, `git diff --check`, `go test ./...`, `go vet ./...`, `go test -race ./...`, and `go build ./cmd/gossip`. For UI changes, run browser verification. For multi-machine claims, only use the two-host runbook and record evidence.
5. Update `docs/gossip-redesign-plan.md`, `docs/coordination-preview.md`, `README.md`, and relevant MCP/skill docs so claims match evidence. Keep open gates open when not actually exercised.
6. Report changed files, commands/results, known remaining work, and a concise release recommendation. Do not push extra changes without coordinating with the user; the requested version push is handled in the original task.

## Gemini review already delivered

Gemini's audit and test pass are recorded in [`gemini-review.md`](gemini-review.md). It correctly leaves these gates open:

- actual two-host acceptance on two distinct physical or virtual machines;
- managed agent start/wake/interruption and enforceable provider runtime/spend controls;
- representative task evaluations for role presets and measured token/time/cost comparisons;
- complete project ACLs in the legacy JSON database/dashboard, if those old surfaces remain collaborative entrypoints.

Gemini's specific kill-switch hardening items were reviewed and completed in the current worktree: the web backend requires a remote-access status method, SQLite UPSERT recovery, migration and CLI coverage, remote dashboard/bootstrap tests, removal of legacy dead tunnel controls, and a read-only dashboard status indicator. Mutations remain restricted to the local CLI/private socket; the web UI deliberately has no unauthenticated toggle. Its 401/403 UI change was corrected during independent validation: 401 expires the project credential session; action-level 403 preserves the valid session; refresh-time 403 clears cached project data but keeps the session connected. The full race suite passed after these changes.

Rendered-browser verification for the new dashboard panel is still open. The browser's saved permission denied `http://127.0.0.1:18081`; do not bypass that restriction. Automated loopback HTTP tests cover the panel API and confirm TLS callers cannot toggle it.

Do not mark the redesign complete because the Go test suite passes. The two-host acceptance, managed runtime, and empirical role/cost gates still require real evidence.
