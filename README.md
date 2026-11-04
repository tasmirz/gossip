# 🗣️ Gossip: Multi-Agent Collaboration Fabric & Shared Knowledge Graph RAG

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![Architecture](https://img.shields.io/badge/Architecture-Blackboard%20%2B%20Gossip-orange)]()
[![Knowledge Graph](https://img.shields.io/badge/RAG-Graphify--Inspired%20(Deterministic)-green)]()
[![SOP Engine](https://img.shields.io/badge/SOP-MetaGPT--Style-purple)]()

> **Gossip** is a high-performance, single-binary Go Model Context Protocol (MCP) server and companion AI Agent Skill. It equips multi-agent systems (Antigravity, Claude Code, Cursor, Windsurf, OpenCode, and autonomous agent swarms) with:
> - **Formally Defined Specialist Roles** (`orchestrator`, `network_engineer`, `math_expert`, `systems_architect`, `security_specialist`, `qa_engineer`, `researcher`, `worker`, `red_team`, `critic`).
> - **MetaGPT-Style Standard Operating Procedures (SOPs)** enforcing phase gates and versioned Markdown artifacts (`PRD` $\to$ `SystemDesign` $\to$ `TaskDAG` $\to$ `Code` $\to$ `Review` $\to$ `STRIDE Audit` $\to$ `Release`).
> - **Deterministic Shared Knowledge Graph RAG Space** (Graphify & Aider inspired, zero embedding costs) with PageRank centrality, blast-radius impact analysis, community cluster detection, and token-budgeted repo-maps.
> - **Multi-Agent Debate (MAD) & Consensus Engine** featuring multi-round convergence detection (Du et al., 2024) and confidence-weighted Borda Count ranked-choice voting.
> - **Adversarial Red-Team & STRIDE Threat Modeling** with automated probe generation and executive audit reports (`AUDIT_REPORT.md`).
> - **Shared Source-of-Truth Task DAG & Kanban** persisted under `.local/gossip/` with atomic writes, POSIX `flock` / Win32 `LockFileEx` cross-platform concurrency safety, and live auto-rendered `BOARD.md`.
> - **Standalone Precompiled Binaries** for Linux (amd64, arm64), macOS (Apple Silicon, Intel), and Windows.

---

## 🏛️ System Architecture

```mermaid
flowchart TB
    subgraph Agents["👥 Multi-Agent Mesh"]
        Orch["👑 Orchestrator"]
        NetEng["🌐 Network Engineer"]
        Math["📐 Math Expert"]
        SysArch["⚙️ Systems Architect"]
        SecSpec["🛡️ Security Specialist"]
        QA["🧪 QA Specialist"]
        Worker["🔨 General Worker"]
        RedTeam["⚔️ Red Team Attacker"]
        Critic["⚖️ Consensus Arbiter"]
    end

    subgraph GossipMCP["⚡ Gossip Server (Single Go Binary)"]
        MCPEngine["MCP Stdio Protocol Handler<br/>(47 Specialized Tools)"]
        MsgBroker["📨 Message Broker<br/>(Direct, Role, Broadcast)"]
        DAGManager["📊 Task DAG & State Engine<br/>(Cycle Detection & Auto-Unblock)"]
        SOPEngine["⚙️ MetaGPT-Style SOP Pipeline<br/>(Phase Gates & Artifact Handoffs)"]
        GraphRAG["🧠 Graphify/Aider Knowledge Graph<br/>(PageRank, Blast-Radius, Clusters, Paths)"]
        DelibEngine["⚖️ MAD & Red-Team Deliberation<br/>(Convergence, Borda Voting, STRIDE)"]
    end

    subgraph LocalStorage["💾 Shared Source of Truth (.local/gossip/)"]
        LockFile[".lock (POSIX flock / Win32 LockFileEx)"]
        StateJSON["state.json (Agents & Goals)"]
        TasksJSON["tasks.json (Task DAG & Kanban)"]
        SOPJSON["sop.json (SOP Workflow Stages)"]
        ArtifactsDir["artifacts/ (Versioned Deliverables)"]
        MessagesJSONL["messages.jsonl (Append-Only Ledger)"]
        GraphJSON["graph.json (Relational Knowledge Map)"]
        DelibJSON["deliberations.json (Debates & Probes)"]
        BoardMD["BOARD.md (Live Human Dashboard)"]
        GraphMD["GRAPH_REPORT.md (Hubs & Proofs Summary)"]
        PipelineMD["SOP_PIPELINE.md (Phase Gate Tracker)"]
        AuditMD["AUDIT_REPORT.md (STRIDE Security Assessment)"]
    end

    Agents <-->|JSON-RPC 2.0 (MCP)| MCPEngine
    MCPEngine --> MsgBroker
    MCPEngine --> DAGManager
    MCPEngine --> SOPEngine
    MCPEngine --> GraphRAG
    MCPEngine --> DelibEngine
    MsgBroker <--> LocalStorage
    DAGManager <--> LocalStorage
    SOPEngine <--> LocalStorage
    GraphRAG <--> LocalStorage
    DelibEngine <--> LocalStorage
```

---

## 🎭 JSON-Driven Role Taxonomy (SOLID Design)

Roles in Gossip are **never hardcoded in Go source code**. They are dynamically loaded from `roles.json` (`config/roles.json` or `.local/gossip/roles.json`) and can be extended at runtime by agents or operators without recompiling.

Adhering strictly to **SOLID Principles**:
- **Single Responsibility (SRP)**: Distinct subsystems handle storage locking (`pkg/state`), role schema resolution (`pkg/roles`), task DAG evaluation (`pkg/tasks`), graph analysis (`pkg/graph`), deliberation (`pkg/deliberation`), and workflow pipelines (`pkg/sop`).
- **Open/Closed (OCP)**: New roles, node types, and SOP workflows can be registered dynamically via JSON or MCP tools without altering existing code.
- **Liskov Substitution (LSP)**: Interface contracts (`roles.Provider`, `tasks.Manager`, `state.Store`) can be swapped or extended without breaking higher-level consumers.
- **Interface Segregation (ISP)**: Interfaces are strictly partitioned by capability (Agent Registry vs Task Engine vs RAG vs SOP).
- **Dependency Inversion (DIP)**: The MCP server and CLI depend on abstract service interfaces injected at construction time.

| Role ID | Title | Specialization & Behavioral Directives | Focus Areas |
| :--- | :--- | :--- | :--- |
| `orchestrator` | Lead System Orchestrator | Decomposes goals into dependency-aware task DAGs (`blocked_by`). Drives SOP phase gates (`gossip_sop_advance`). Synthesizes deliverables. | Planning, delegation, milestones, architecture. |
| `worker` | General Software Engineer | Claims ready tasks (`todo` $\to$ `in_progress`), writes clean code, logs discovered symbols into graph, delivers results. | Implementation, refactoring, feature authoring. |
| `network_engineer` | Distributed Systems & Network Engineer | Protocol design, length-prefixed binary framing, socket non-blocking multiplexing, connection state machines, serialization overhead, latency budgets. | Protocols, framing, sockets, mesh, packet efficiency. |
| `math_expert` | Mathematical & Algorithmic Specialist | Algorithmic invariants, time/space asymptotic bounds ($O, \Omega, \Theta$), graph algorithms (cycle detection, BFS), polynomial selection (CRC32 Castagnoli). | Formal proofs, complexity bounds, graph theory, probability. |
| `systems_architect` | Low-Level Systems & OS Architect | Concurrency safety, race-condition elimination, POSIX file locking (`flock`), atomic rename persistence, cache locality, memory allocation churn. | Memory safety, OS primitives, thread synchronization, IPC. |
| `security_specialist` | Security & Vulnerability Auditor | STRIDE threat modeling, input sanitization, directory traversal defense, cryptographically secure randomness, deserialization safety, DoS protection. | OWASP, crypto validation, sandboxing, vulnerability audit. |
| `qa_engineer` | Verification & Test Specialist | Deterministic test harnesses, property-based verification, boundary condition analysis, race detection (`-race`), invariant validation. | Regression suites, edge cases, integration verification. |
| `researcher` | Deep Research & Explorer | Explores external documentation, RFC standards, competitor benchmarks, and extracts structured concepts into the shared knowledge graph. | RFCs, documentation, benchmarks, state-of-the-art surveying. |
| `red_team` | Adversarial Red Team Specialist | Aggressively probes edge cases, injects data races, fuzzes inputs, generates STRIDE attack vectors, and stress-tests architectural assumptions. | Fuzzing, race injection, payload testing, exploit analysis. |
| `critic` | Independent Critic & Consensus Arbiter | Evaluates debates impartially, identifies hidden trade-offs, tallies Borda ranked votes, checks convergence, and commits decisions to the shared knowledge graph. | Consensus evaluation, arbitration, decision records. |

---

## ⚙️ Standard Operating Procedures (MetaGPT-Style)

Multi-agent engineering succeeds when agents follow structured assembly-line workflows instead of unconstrained chatting:

$$\text{Code} = \text{SOP}(\text{Team})$$

Gossip implements three built-in SOP templates with strict phase gates and artifact handoffs:
1. **`feature_delivery`**: Requirements (`prd`) $\to$ Architecture (`system_design`) $\to$ Planning (`task_plan`) $\to$ Implementation (`code_manifest`) $\to$ QA Review (`peer_review`) $\to$ Security Audit (`security_audit`) $\to$ Signoff (`release_notes`).
2. **`bug_investigation`**: Incident Triage $\to$ Blast Radius Analysis $\to$ Fix Patch $\to$ QA Regression $\to$ Resolution Signoff.
3. **`security_hardening`**: STRIDE Threat Modeling $\to$ Red-Team Probing $\to$ Defensive Patching $\to$ Security Clearance Signoff.

---

## 🛠️ Complete MCP Tool Directory (47 Tools)

### 1. Identity & Role Management
- `gossip_register_agent`: Join the active gossip mesh with agent ID, role, capabilities, and active task. Supports multi-instance per role.
- `gossip_list_agents`: List all active agents, their statuses, tasks, and last heartbeats.
- `gossip_heartbeat`: Refresh session timestamp and update current task or status.
- `gossip_get_role_definition`: Fetch complete system prompt, directives, and tool recommendations for any role.
- `gossip_list_roles`: View markdown directory of all roles loaded dynamically from JSON.
- `gossip_register_custom_role`: Dynamically define and register a new custom agent role at runtime in JSON.

### 2. Standard Operating Procedures (SOP) Tools
- `gossip_sop_list_workflows`: List active SOP workflows and available templates.
- `gossip_sop_start`: Initiate an SOP workflow instance (`feature_delivery`, `bug_investigation`, `security_hardening`).
- `gossip_sop_save_artifact`: Save a versioned deliverable artifact (`prd`, `system_design`, `task_plan`, `code_manifest`, `peer_review`, `security_audit`, `release_notes`) to `.local/gossip/artifacts/`.
- `gossip_sop_advance`: Advance stage through phase gates after validating required deliverables and roles.
- `gossip_sop_get_status`: Inspect active stage, blockers, and artifacts for a workflow.

### 3. Knowledge Graph RAG & Codebase Architecture (Graphify & Aider Inspired)
- `gossip_rag_add_node`: Add a concept, file, symbol, decision, constraint, or formal proof to the shared graph.
- `gossip_rag_add_edge`: Connect entities with typed relationships and provenance (`EXTRACTED` vs `INFERRED`).
- `gossip_rag_query`: Search graph using keywords, type filter, and tag filter with relevance scoring.
- `gossip_rag_get_neighborhood`: Inspect 1-hop or 2-hop connected subgraphs around any node.
- `gossip_rag_find_path`: Compute the shortest relationship path between any two concepts using BFS.
- `gossip_rag_export_report`: Generate and return the human-readable `GRAPH_REPORT.md` summary.
- `gossip_rag_pagerank`: Compute PageRank architectural centrality scores across all knowledge nodes.
- `gossip_rag_impact_analysis`: Calculate blast radius and affected dependents (Graphify-style `affected.py`).
- `gossip_rag_clusters`: Detect modular architectural clusters and submodules (Graphify-style `cluster.py`).
- `gossip_rag_repo_map`: Generate a compact, token-budgeted architectural map ranked by PageRank centrality (Aider-style).

### 4. Multi-Agent Deliberations, Debates & Consensus (MAD)
- `gossip_start_debate`: Open a structured multi-agent debate session on an architectural trade-off or controversial proposal.
- `gossip_post_argument`: Post a pro, con, critique, rebuttal, or neutral argument in an active debate round.
- `gossip_advance_debate_round`: Close current round and open next round for critique and rebuttal.
- `gossip_cast_vote`: Cast a formal vote on a debate proposal with confidence level (1-5) and rationale.
- `gossip_debate_ranked_vote`: Cast a ranked-choice ballot ordering multiple alternative candidates (Borda Count tallying).
- `gossip_debate_tally_ranked_votes`: Tally ranked-choice votes using confidence-weighted Borda Count to identify consensus winner.
- `gossip_debate_check_convergence`: Evaluate whether a multi-round debate has reached consensus (Du et al., 2024).
- `gossip_conclude_debate`: Conclude debate, tally votes, determine consensus outcome, and record the adopted decision in graph.

### 5. Peer Reviews & Adversarial Red Teaming
- `gossip_request_review`: Request formal peer review on a task, artifact, or code file.
- `gossip_submit_review`: Submit structured review findings (`minor`, `major`, `critical`) and issue formal verdict.
- `gossip_log_red_team_probe`: Log an adversarial exploit, boundary attack, or data race with reproduction payloads and remediation task.
- `gossip_redteam_generate_stride_probes`: Generate structured STRIDE threat vectors (Spoofing, Tampering, Repudiation, Info Disclosure, DoS, Elevation of Privilege) against any target component.
- `gossip_redteam_generate_audit_report`: Compile and return the executive security assessment report (`AUDIT_REPORT.md`).
- `gossip_list_deliberations`: List active or historical debates, peer reviews, and red team probes.

### 6. Inter-Agent Messaging Tools
- `gossip_send_message`: Send point-to-point message (`agent:<id>`), role-targeted message (`role:<role_id>`), or broadcast (`all`).
- `gossip_broadcast_message`: Broadcast high-priority announcements to all agents.
- `gossip_read_inbox`: Retrieve incoming messages filtered by agent ID, role, unread status, or thread ID.
- `gossip_mark_messages_read`: Mark messages as acknowledged.
- `gossip_list_threads`: Inspect active multi-agent conversation threads.

### 7. Shared Source of Truth: Goals & Tasks
- `gossip_set_goal`: Define project objectives and milestone checkpoints.
- `gossip_get_goals`: Retrieve active goals and milestone completion status.
- `gossip_create_task`: Create a task in the DAG with dependencies (`blocked_by`), priority, and role assignee.
- `gossip_update_task`: Advance task state (`todo` $\to$ `in_progress` $\to$ `completed`), append progress notes, or register result artifacts. *(Completing a blocker automatically unblocks dependent tasks!)*
- `gossip_list_tasks`: Query tasks filtered by status, role, or assignee.
- `gossip_get_task`: Retrieve full task specification, dependencies, and history notes.
- `gossip_render_board`: Regenerate and return the live Markdown Kanban board (`BOARD.md`).

---

## 🚀 Quickstart & Installation

### Option A: Using Precompiled Binary (Instant)

Precompiled statically-linked binaries are included in `bin/`:
```bash
# Run MCP stdio server
./bin/gossip-mcp

# Or use CLI features
./bin/gossip init
./bin/gossip status
./bin/gossip board
./bin/gossip pagerank
./bin/gossip repomap
./bin/gossip audit
```

### Option B: Building from Source
```bash
# Clone the repository
git clone https://github.com/tasmirz/gossip.git
cd gossip

# Build local binary
go build -ldflags="-s -w" -o bin/gossip ./cmd/gossip

# Or build for all platforms (Linux, macOS, Windows)
./scripts/build.sh
```

---

## ⚙️ Configuring MCP Clients

### Claude Desktop (`claude_desktop_config.json`)
```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip/bin/gossip-mcp",
      "args": ["mcp"]
    }
  }
}
```

### Antigravity / Cursor / Windsurf (`~/.gemini/antigravity/mcp/` or project settings)
```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip/bin/gossip-mcp",
      "args": ["mcp", "--dir", "/absolute/path/to/your/project/.local/gossip"]
    }
  }
}
```

---

## 🖥️ Command-Line Interface (CLI)

Gossip functions both as an MCP server and a fast developer CLI:

```bash
# Initialize gossip in the current project root
gossip init

# Inspect mesh status (agents online, task counts, knowledge nodes)
gossip status

# View the live Markdown Kanban board
gossip board

# View the knowledge graph report (core hubs, proofs, decisions)
gossip report

# Compute and display PageRank architectural centrality
gossip pagerank

# Perform blast-radius impact analysis when modifying a component
gossip impact util:core --depth 3

# Detect architectural clusters and submodules
gossip clusters

# Generate PageRank-prioritized compact repo map
gossip repomap

# Generate executive red-team security assessment (AUDIT_REPORT.md)
gossip audit

# List active Standard Operating Procedure (SOP) workflows
gossip sop

# List tasks in terminal table format
gossip tasks

# List active and past debates with vote tallies
gossip debates

# List adversarial red team security probes
gossip probes

# Send a message directly from terminal
gossip send --from dev_terminal --role orchestrator --to role:math_expert --msg "Analyze complexity"

# Print all defined roles and system prompts
gossip roles
```

---

## 🧪 Testing & Verification

Gossip includes unit and integration tests with concurrency race detection:

```bash
# Run full test suite with race detector
go test -v -race ./...
```

All subsystems (state locking, append-only message streaming, task DAG validation, SOP phase gates, PageRank convergence, blast radius analysis, Borda count ranked voting, and MCP tool serialization) pass 100% cleanly.

---

## 📜 License

This project is licensed under the **GNU Affero General Public License v3.0 (AGPLv3)**. See [LICENSE](LICENSE) for the full license text.
