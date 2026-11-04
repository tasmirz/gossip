---
name: gossip
description: Multi-agent coordination fabric, task DAG delegation, MetaGPT-style Standard Operating Procedures (SOPs), inter-agent messaging, Graphify blast-radius RAG, Aider-style PageRank repo-mapping, Multi-Agent Debate (MAD) with Borda consensus, and STRIDE red-teaming. Always trigger this skill whenever coordinating multiple AI agents or subagents, delegating tasks between orchestrators and workers, consulting specialist roles (such as network_engineer, math_expert, systems_architect, security_specialist, qa_engineer, researcher), following structured SOP phase gates, reading or writing shared project state under .local/gossip, or analyzing codebase blast-radii.
---

# Gossip: Multi-Agent Collaboration Fabric & Shared Knowledge RAG

Gossip connects multiple concurrent AI coding agents into an organized, high-velocity engineering organization. Agents share a single source of truth in `<project_root>/.local/gossip/`, communicate asynchronously over direct or role channels, advance tasks along dependency-aware DAGs, execute MetaGPT-style Standard Operating Procedures (SOPs), and explore codebase architectures with deterministic knowledge graph RAG (PageRank centrality, community clusters, and blast-radius analysis inspired by Graphify and Aider).

---

## 1. Role Taxonomy & Behavioral Directives

When participating in a Gossip mesh, each agent assumes a specialized role configured via JSON:

| Role ID | Title | Primary Directives |
| :--- | :--- | :--- |
| `orchestrator` | Lead System Orchestrator | Decomposes strategic goals into task DAGs (`blocked_by` chains). Drives SOP phase gates (`gossip_sop_advance`). Monitors `BOARD.md`. Synthesizes final deliverables. |
| `worker` | General Software Engineer | Claims `todo` tasks, sets status to `in_progress`, writes modular clean code, logs discovered symbols into graph, reports completion with artifacts. |
| `network_engineer` | Network & Distributed Systems Engineer | Protocol design, socket multiplexing, length-prefixed binary framing, serialization overhead, latency budgets, exponential backoff, connection state machines. |
| `math_expert` | Mathematical & Algorithmic Specialist | Algorithmic invariants, time/space complexity proofs ($O$, $\Omega$, $\Theta$), graph algorithms (BFS, DAG cycles), polynomial selection (CRC32 Castagnoli), numerical stability. |
| `systems_architect` | Systems & OS Architect | Concurrency safety, race-condition freedom, memory allocation churn, POSIX file locking (`flock`), atomic rename persistence, crash consistency. |
| `security_specialist` | Security & Vulnerability Auditor | STRIDE threat modeling, input sanitization, directory traversal defense, cryptographically secure randomness, deserialization safety, DoS protection. |
| `qa_engineer` | Verification & Test Specialist | Authors deterministic unit and integration test harnesses, edge cases (empty, boundary, overflow), invariant assertions, race condition detection (`-race`). |
| `researcher` | Codebase & External Researcher | Explores documentation, RFC standards, existing code patterns, and populates the knowledge graph with verified facts (`EXTRACTED`) and insights (`INFERRED`). |
| `red_team` | Adversarial Red Team Specialist | Aggressively probes edge cases, injects data races, fuzzes inputs, generates STRIDE attack vectors, and stress-tests architectural assumptions. |
| `critic` | Independent Critic & Consensus Arbiter | Evaluates debates impartially, identifies hidden trade-offs, tallies Borda ranked votes, checks convergence, and commits decisions to the shared knowledge graph. |

> **Dynamic Role Creation & Multiple Instances**: The orchestrator can define custom project roles dynamically using `gossip_register_custom_role` (persisted to `roles.json`). Multiple concurrent instances of any role can run simultaneously (e.g., `worker-1`, `worker-2`, `red-team-alpha`, `red-team-beta`).

---

## 2. MetaGPT-Style Standard Operating Procedures (SOPs)

Multi-agent engineering succeeds when agents follow structured assembly-line workflows instead of chaotic, free-form chat:

$$\text{Code} = \text{SOP}(\text{Team})$$

Gossip provides three built-in SOP templates, each enforced by formal phase gates and structured artifact handoffs:

### A. Feature Delivery Workflow (`feature_delivery`)
1. **Requirements Gate** (`orchestrator`): Deliver `prd` artifact.
2. **Architecture Gate** (`systems_architect`): Deliver `system_design` artifact with API schemas and data models.
3. **Planning Gate** (`orchestrator`): Deliver `task_plan` artifact and populate Gossip task DAG.
4. **Implementation Gate** (`worker`): Deliver `code_manifest` artifact with tested code.
5. **QA Review Gate** (`qa_engineer`): Deliver `peer_review` artifact with test results.
6. **Security Audit Gate** (`security_specialist`): Deliver `security_audit` artifact with STRIDE mitigations.
7. **Release Signoff Gate** (`orchestrator`): Deliver `release_notes` artifact and finalize.

### B. Bug Investigation Workflow (`bug_investigation`)
1. **Incident Triage** $\to$ 2. **Root Cause & Blast Radius** (via `gossip_rag_impact_analysis`) $\to$ 3. **Fix Patch** $\to$ 4. **QA Regression Test** $\to$ 5. **Resolution Signoff**.

### C. Security Hardening Workflow (`security_hardening`)
1. **STRIDE Threat Modeling** $\to$ 2. **Red-Team Probing** (via `gossip_redteam_generate_stride_probes`) $\to$ 3. **Defensive Patching** $\to$ 4. **Security Clearance Signoff**.

#### Using SOPs:
```json
// 1. Start a feature workflow
gossip_sop_start({
  "template": "feature_delivery",
  "title": "WebSocket Streaming Transport",
  "description": "High-throughput binary framing socket transport",
  "initiator": "agent:lead_orchestrator"
})

// 2. Save required deliverable artifact
gossip_sop_save_artifact({
  "workflow_id": "sop-feature_delivery-1793794674",
  "stage_id": "requirements",
  "artifact_type": "prd",
  "title": "WebSocket Transport PRD",
  "content": "## Goal\nImplement full-duplex socket transport with Castagnoli CRC32 framing...",
  "created_by": "agent:pm"
})

// 3. Advance to next phase gate (validates required artifact presence)
gossip_sop_advance({
  "workflow_id": "sop-feature_delivery-1793794674",
  "agent_id": "agent:lead_orchestrator",
  "role": "orchestrator"
})
```

---

## 3. Knowledge Graph RAG & Codebase Architecture Tools

Inspired by **Graphify** and **Aider repo-map**, Gossip's knowledge graph provides deterministic code exploration without heavy vector databases:

### A. PageRank Architectural Centrality
Identify the most load-bearing hubs and utilities in the codebase:
```json
gossip_rag_pagerank({
  "damping": 0.85,
  "max_iterations": 100,
  "tolerance": 0.00001
})
```

### B. Blast-Radius & Affected Components Analysis (Graphify-style)
Before changing a file, function, or architectural invariant, calculate who will break:
```json
gossip_rag_impact_analysis({
  "seed_node_id": "util:core",
  "depth": 3
})
```
*Returns direct dependents (depth 1), transitive dependents (depth 2+), blast radius risk score (0.0–100.0), critical bottlenecks, and required verification test targets.*

### C. Architectural Cluster & Subsystem Detection
Partition the knowledge graph into functional submodules:
```json
gossip_rag_clusters({})
```

### D. Token-Budgeted Compact Repo-Map (Aider-style)
Generate a compact summary of central symbols and dependencies that fits inside a strict LLM token budget (e.g. 1000 tokens):
```json
gossip_rag_repo_map({
  "max_tokens": 1000
})
```

---

## 4. Multi-Agent Debate (MAD), Ranked Voting & Red Teaming

Gossip incorporates formal dialectic and adversarial testing protocols:

### A. Multi-Agent Debate with Convergence Detection
1. **Open Debate**: `gossip_start_debate({"title": "Protocol Selection", "description": "gRPC vs WebSocket vs SSE", "proposer": "net-1", "role": "network_engineer"})`
2. **Post Arguments**: `gossip_post_argument({"debate_id": "deb-1", "agent_id": "net-1", "role": "network_engineer", "stance": "pro", "content": "WebSocket minimizes roundtrip handshakes."})`
3. **Advance Rounds**: `gossip_advance_debate_round({"debate_id": "deb-1"})`
4. **Check Convergence (Du et al., 2024)**:
   ```json
   gossip_debate_check_convergence({"debate_id": "deb-1"})
   ```
   *Returns agreement ratio, stance stability score, and recommendations (`proceed_to_vote`, `continue_debate`, or `deadlock_detected`).*

### B. Ranked-Choice Voting (Borda Count)
When deciding among multiple candidates, agents cast ordered preferences with confidence ratings (1-5):
```json
// Cast ranked vote
gossip_debate_ranked_vote({
  "debate_id": "deb-1",
  "agent_id": "arch-1",
  "role": "systems_architect",
  "ranked_choices": ["Option B (WebSocket)", "Option A (gRPC)", "Option C (SSE)"],
  "confidence": 5,
  "reasoning": "Full duplex socket framing with lowest memory overhead"
})

// Tally consensus
gossip_debate_tally_ranked_votes({"debate_id": "deb-1"})
```

### C. Adversarial Red-Team STRIDE Attack Probes
1. **Automated STRIDE Probe Generation**:
   Generate 6 structured attack scenarios (Spoofing, Tampering, Repudiation, Info Disclosure, DoS, Elevation of Privilege) against any component:
   ```json
   gossip_redteam_generate_stride_probes({
     "target": "pkg/state/state.go",
     "context": "Advisory file locking with flock",
     "auto_log": true
   })
   ```
2. **Generate Executive Security Report**:
   Compile all probes into `.local/gossip/AUDIT_REPORT.md`:
   ```json
   gossip_redteam_generate_audit_report({})
   ```

---

## 5. Storage Architecture (`.local/gossip`)

All state is persisted under `.local/gossip/` inside the current project root:
- `.lock`: Advisory lockfile preventing file corruption across concurrent agent processes.
- `state.json`: Agent registry, last heartbeats, active project goals, and milestones.
- `tasks.json`: Task DAG, blocker lists, assignees, progress logs, and deliverables.
- `sop.json`: Standard Operating Procedure workflows, active stage gates, and deliverables.
- `artifacts/`: Directory of versioned Markdown artifacts (`<workflow>_<stage>_<type>.md`).
- `deliberations.json`: Debates, multi-option votes, peer reviews, and red-team security probes.
- `messages.jsonl`: Append-only audit ledger of every message sent between agents.
- `graph.json`: Knowledge graph nodes and edges with relational provenance (`EXTRACTED` vs `INFERRED`).
- `roles.json`: JSON-driven role definitions, directives, prompts, and tools (extensible dynamically).
- `BOARD.md`: Automatically rendered Markdown Kanban board showing goals, debates, probes, and task lanes.
- `GRAPH_REPORT.md`: Automatically rendered knowledge analysis summarizing core hubs and proofs.
- `SOP_PIPELINE.md`: Live phase-gate pipeline tracker for active SOP workflows.
- `AUDIT_REPORT.md`: Executive STRIDE security audit summary with remediation statuses.

---

## 6. Complete MCP Tools Reference (47 Tools)

### Agent & Role Management
- `gossip_register_agent`: Join the active agent mesh (`agent_id`, `role`, `name`, `capabilities`).
- `gossip_reidentify_agent` (aliases: `gsp_reidentify`, `gossip_resume_session`): Re-identify or resume an agent session across restarts or new context sessions, returning an instant catch-up briefing of in-progress tasks, unread inbox messages, active SOP stages, and personal to-dos.
- `gossip_list_agents`: View online/idle agents and their current tasks.
- `gossip_heartbeat`: Send heartbeat and update active status (`agent_id`, `current_task_id`, `status`).
- `gossip_get_role_definition`: Read system prompt and directives for a role (`role_id`).
- `gossip_list_roles`: List all specialist roles loaded from JSON.
- `gossip_register_custom_role`: Dynamically register a new custom role in JSON (`id`, `title`, `description`, `system_prompt`, `directives`).

### Inter-Agent Messaging
- `gossip_send_message`: Send point-to-point, role (`role:<role>`), or broadcast message (`from`, `role`, `to`, `subject`, `content`).
- `gossip_broadcast_message`: Broadcast announcement to all agents (`from`, `role`, `content`, `subject`).
- `gossip_read_inbox`: Check incoming communications (`agent_id`, `role`, `only_unread`, `limit`).
- `gossip_mark_messages_read`: Acknowledge receipt of messages (`agent_id`, `message_ids`).
- `gossip_list_threads`: Inspect active discussion threads (`limit`).

### Strategic Goals & Task DAG
- `gossip_set_goal`: Set high-level strategic target (`title`, `description`, `milestone_titles`).
- `gossip_get_goals`: View active goals and milestone progress.
- `gossip_create_task`: Create task with dependency constraints (`title`, `description`, `role`, `blocked_by`).
- `gossip_update_task`: Advance task lifecycle (`task_id`, `status`, `assigned_to`, `notes`, `artifact`).
- `gossip_list_tasks`: Query task board (`status`, `role`, `assigned_to`).
- `gossip_get_task`: View task details and history (`task_id`).
- `gossip_render_board`: Fetch live Markdown Kanban board (`BOARD.md`).

### Multi-Level To-Do & Orchestrator Delegation Lists
- `gossip_personal_todo_add`: Add a personal checklist item or scratchpad task for an agent (`agent_id`, `title`, `priority`, `context_task_id`, `notes`).
- `gossip_personal_todo_list`: List an agent's personal to-do items and working scratchpad (`agent_id`, `status`).
- `gossip_personal_todo_update`: Update status (`pending`, `in_progress`, `done`) or notes on a personal task.
- `gossip_personal_todo_delete`: Remove a personal checklist item (`agent_id`, `item_id`).
- `gossip_system_todo_list`: Unified cross-system backlog aggregating ready DAG tasks, incomplete milestones, pending SOP phase gates, and open STRIDE security vulnerabilities.
- `gossip_orchestrator_assignments`: Centralized workload and assignment matrix showing active tasks per agent, bottlenecks, and idle-worker delegation recommendations.

### MetaGPT-Style Standard Operating Procedures (SOPs)
- `gossip_sop_list_workflows`: List active workflows and available templates (`feature_delivery`, `bug_investigation`, `security_hardening`).
- `gossip_sop_start`: Initiate an SOP workflow (`template`, `title`, `description`, `initiator`).
- `gossip_sop_save_artifact`: Save structured artifact deliverable (`workflow_id`, `stage_id`, `artifact_type`, `title`, `content`, `created_by`).
- `gossip_sop_advance`: Advance stage through phase gates after validating required deliverables and roles.
- `gossip_sop_get_status`: Inspect active stage, blockers, and artifacts for a workflow.

### Knowledge Graph RAG & Architectural Analysis
- `gossip_rag_add_node`: Record concept/symbol/proof in shared RAG (`id`, `type`, `label`, `summary`, `tags`).
- `gossip_rag_add_edge`: Link two nodes with `EXTRACTED` or `INFERRED` provenance (`source`, `target`, `relation`, `provenance`).
- `gossip_rag_query`: Search shared knowledge graph with token ranking (`query`, `type`, `tag`, `limit`).
- `gossip_rag_get_neighborhood`: Inspect 1-hop or 2-hop connected subgraph (`node_id`, `depth`).
- `gossip_rag_find_path`: Shortest relationship path between two concepts (`source`, `target`).
- `gossip_rag_export_report`: Fetch human-readable `GRAPH_REPORT.md`.
- `gossip_rag_pagerank`: Compute PageRank centrality scores across all knowledge nodes.
- `gossip_rag_impact_analysis`: Calculate blast radius and affected dependents (Graphify-style).
- `gossip_rag_clusters`: Detect modular architectural clusters and submodules.
- `gossip_rag_repo_map`: Generate PageRank-prioritized compact repo map (Aider-style).

### Deliberations, Debates & Consensus
- `gossip_start_debate`: Launch a multi-agent debate session (`title`, `description`, `proposer`, `role`).
- `gossip_post_argument`: Contribute argument/rebuttal to debate round (`debate_id`, `agent_id`, `role`, `stance`, `content`).
- `gossip_advance_debate_round`: Advance active debate to the next round.
- `gossip_cast_vote`: Cast yes/no/abstain vote with confidence rating (`debate_id`, `agent_id`, `role`, `choice`, `confidence`).
- `gossip_debate_ranked_vote`: Cast ranked-choice ballot across multiple candidates (`debate_id`, `agent_id`, `role`, `ranked_choices`, `confidence`).
- `gossip_debate_tally_ranked_votes`: Run Borda Count tally to determine consensus winner.
- `gossip_debate_check_convergence`: Check if debate has converged across rounds (Du et al., 2024).
- `gossip_conclude_debate`: Conclude debate, tally votes, and register consensus decision in graph.

### Peer Reviews & Red Teaming
- `gossip_request_review`: Request formal peer review on deliverable (`target_type`, `target_id`, `author_id`, `reviewer_id`).
- `gossip_submit_review`: Issue formal critique verdict & findings (`review_id`, `reviewer_id`, `status`, `verdict`).
- `gossip_log_red_team_probe`: Log red-team exploit or race condition probe (`target`, `threat_category`, `attack_vector`, `poc`).
- `gossip_redteam_generate_stride_probes`: Generate automated STRIDE attack scenarios for a target component.
- `gossip_redteam_generate_audit_report`: Generate executive security audit report (`AUDIT_REPORT.md`).
- `gossip_list_deliberations`: Query active debates, reviews, or probes (`type`, `status`).

### Continuous Event Polling & State Synchronization
- `gossip_poll_events` (alias `gsp_poll`, `gossip_sync`): Continuously poll or long-poll for incoming messages, unblocked ready tasks, debate rounds, SOP phase gates, and peer heartbeats with configurable timeout (`since_time`, `timeout_seconds`, `event_types`, `auto_heartbeat`).
  - Returns `server_time`, `has_updates`, `events`, `unread_messages_count`, `ready_tasks_count`, and `active_agents_count`.
- `gossip_log_event`: Append an explicit custom milestone or event to the shared event ledger (`events.jsonl`).

### Mixture of Experts (MoE) Councils & Subagent Parallelism
- `gossip_moe_convene_panel` (alias `gsp_moe_convene`): Convene an MoE council with automated router gating and specialist expert summoning (`title`, `query`, `domain`, `roles`, `max_parallel`).
- `gossip_moe_post_argument` (alias `gsp_moe_argument`): Submit domain-specific technical arguments and trade-off points (`panel_id`, `agent_id`, `role`, `stance`, `perspective`, `key_points`).
- `gossip_moe_synthesize` (alias `gsp_moe_synthesize`): Synthesize expert council viewpoints into unified consensus and modular parallel work packages.
- `gossip_moe_dispatch_subagents` (alias `gsp_parallel`, `gsp_moe_dispatch`): Dispatch concurrent subagent workers for decoupled parallel tracks.
- `gossip_moe_list_panels` (alias `gsp_moe_list`): Inspect all active and historical MoE discussion panels.

### Guardrails, Role Derivation & Project Invariants
- `gossip_derive_project_roles` (alias `gsp_derive_roles`): Synthesize LLM-derived specialist roles, cautions, and invariants for the project; generates `guardrails.json`, `AGENTS.md`, and `RULES.md`.
- `gossip_guardrails_validate_action` (alias `gsp_guardrail_check`): Pre-action safety check checking caller role, action name, and target file against active invariants.
- `gossip_set_caution_rule`: Register a custom caution rule (`id`, `category`, `level`, `trigger_pattern`, `directive`, `enforcement`).
- `gossip_get_guardrails`: Inspect project invariants, tech stack, and active caution rules.
- `gossip_export_agents_md`: Export human-readable Markdown agent directives (`AGENTS.md`).

---

## 7. Native MCP Prompts as Slash Commands (`/gsp:...`)

Gossip registers 15 official Model Context Protocol Prompts that modern MCP clients expose directly in chat as `/gsp:...`:

1. `/gsp:moe`: Convene a Mixture of Experts discussion council to resolve a complex dilemma.
2. `/gsp:parallel`: Dispatch parallel subagent execution tracks for decoupled tasks.
3. `/gsp:resume`: Re-identify an agent across restarts or in a new session with an instant catch-up briefing.
2. `/gsp:poll`: Set up continuous event polling and long-polling sync loop.
3. `/gsp:assignments`: Inspect Orchestrator workload distribution, assigned tasks, and delegation suggestions.
4. `/gsp:my_todos`: Inspect and update personal agent scratchpad checklist.
5. `/gsp:system_todos`: Inspect unified project-wide action backlog.
6. `/gsp:derive_roles`: Synthesize specialist roles and guardrails tailored to the active repository.
7. `/gsp:blast_radius`: Analyze reverse dependency impact before modifying files or symbols.
8. `/gsp:sop_advance`: Advance SOP phase gate with verification checklist.
9. `/gsp:debate`: Initiate an adversarial multi-agent debate with convergence tracking.
10. `/gsp:stride_audit`: Execute automated STRIDE threat modeling against interfaces.
11. `/gsp:review`: Request or submit a formal peer review on deliverables.
12. `/gsp:guardrail_check`: Pre-flight action inspection against caution rules.
13. `/gsp:repomap`: Fetch token-budgeted PageRank architectural repo map.

---

## 8. Research Citations & Attributions

Gossip builds on foundational open-source multi-agent and AI security research. Formal citations are documented in `ATTRIBUTIONS.md`:
- **MetaGPT (`geekan/MetaGPT`)**: Assembly-line Standard Operating Procedures ($\text{Code} = \text{SOP}(\text{Team})$).
- **Graphify (`Graphify-Labs/graphify`)**: Deterministic knowledge graph RAG, reverse dependency blast radius (`affected.py`), and community clustering (`cluster.py`).
- **Aider (`Aider-AI/aider`)**: PageRank centrality repo-mapping for compact token-budgeted codebase context injection.
- **Multi-Agent Debate (Du et al., 2024 / Mohit Parkash)**: Multi-round dialectic deliberation with stance stability convergence tracking and Borda Count ranked-choice voting.
- **Microsoft AutoGen & CrewAI**: Dynamic agent registration, task DAGs, and autonomous peer review protocols.
- **PyRIT (`Azure/PyRIT`) & MultiAgent-RedTeam-Toolkit**: Adversarial red-team probing and STRIDE security audit generation.
- **NVIDIA NeMo Guardrails**: Pre-action guardrail inspection, caution rule enforcement, and `AGENTS.md` synthesis.
