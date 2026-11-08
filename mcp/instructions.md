# Gossip MCP Tools Instructions

Gossip connects multiple concurrent AI coding agents into an organized, high-velocity engineering organization.
All state is persistently synchronized with non-blocking atomic POSIX file locks in `<project_root>/.local/gossip/`.

## Core MCP Tool Workflows

1. **Role Identification & Work Roles**:
   - `gossip_roles`: Inspect role presets, list project role assignments, or assign advisory duties.
     - `operation: "presets"`: Returns the 6 versioned starter role presets (`coordinator`, `researcher`, `implementer`, `reviewer`, `verifier`, `integrator`) with documented provenance citing first-party references.
     - `operation: "list"`: Lists active work role assignments for the authenticated project.
     - `operation: "assign"`: (Project owners only) Assign a preset or custom role to a principal or specific task scope.
     - **Security Invariant**: Work roles define advisory operational duties, handoff boundaries, and escalation guidance only. Work roles NEVER grant project permissions; explicit project permission grants (`principal_permissions`) remain strictly independent.

2. **Inter-Agent Messaging & Continuous Sync**:
   - `gossip_send_message`: Send targeted direct messages (`agent:<id>`) or role broadcasts (`role:<role_id>`).
   - `gossip_broadcast_message`: Broadcast notifications across all agents.
   - `gossip_read_inbox`: Retrieve unread messages for your agent.
   - `gossip_poll_events` (alias `gsp_poll`): Long-poll for incoming messages, unblocked DAG tasks, active debates, and SOP phase updates with defined timeouts.

3. **Multi-Level Task Management**:
   - **Personal Scratchpad**: `gossip_personal_todo_add`, `gossip_personal_todo_list`, `gossip_personal_todo_update`, `gossip_personal_todo_delete` (private agent to-dos).
   - **System Backlog**: `gossip_system_todo_list` (cross-system backlog uniting ready DAG tasks, milestones, SOP gates, and STRIDE vulnerabilities).
   - **Orchestration Matrix**: `gossip_orchestrator_assignments` (team-wide workload breakdown, bottleneck detection, and idle-worker delegation suggestions).
   - **Task DAG Execution**: `gossip_create_task`, `gossip_delegate_task`, `gossip_update_task_status`, `gossip_get_board`, `gossip_get_tasks`.

4. **Shared Knowledge Graph RAG (Graphify + Aider)**:
   - `gossip_rag_query`: Semantic & relational query across codebase nodes, facts, and ADRs.
   - `gossip_rag_index_codebase` (alias `gsp_index`): Deterministically parse AST of all Go packages, structs, functions, and import edges.
   - `gossip_rag_blast_radius`: Compute cascading blast-radius and affected nodes before modifying files.
   - `gossip_rag_pagerank`: Architectural centrality scoring.
   - `gossip_rag_repomap`: Token-budgeted repo-map for LLM context.
   - `gossip_rag_clusters`: Detect architectural submodules and connected clusters.

5. **MetaGPT-Style Standard Operating Procedures (SOPs)**:
   - `gossip_sop_start`: Launch formal workflows (`feature_delivery`, `bug_investigation`, `security_hardening`).
   - `gossip_sop_advance`: Advance through strict phase gates with versioned markdown artifacts in `.local/gossip/artifacts/`.
   - `gossip_sop_status` & `gossip_sop_history`: Inspect workflow stage progression.

6. **Multi-Agent Debate & Borda Consensus**:
   - `gossip_start_debate`: Initiate formal architectural or algorithmic debate.
   - `gossip_contribute_argument`: Submit evidence-backed positions.
   - `gossip_advance_debate_round`: Trigger peer critique rounds.
   - `gossip_cast_vote`: Cast ranked-choice votes with confidence weights.
   - `gossip_conclude_debate`: Finalize debate via Borda Count consensus and commit decision as an ADR to the RAG graph.

7. **STRIDE Red-Teaming & Guardrails**:
   - `gossip_redteam_generate_probes`: Generate adversarial security probes across Spoofing, Tampering, Repudiation, Information Disclosure, Denial of Service, and Elevation of Privilege.
   - `gossip_redteam_report`: Compile executive AUDIT_REPORT.md.
   - `gossip_guardrail_check`: Pre-screen actions against security invariants and caution rules.
   - `gossip_guardrail_derive`: Synthesize project guardrails and generate AGENTS.md.
