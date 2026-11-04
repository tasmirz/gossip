# 🗣️ Gossip: Multi-Agent Collaboration Fabric & Shared Knowledge Graph RAG

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![Architecture](https://img.shields.io/badge/Architecture-Blackboard%20%2B%20Gossip-orange)]()
[![Knowledge Graph](https://img.shields.io/badge/RAG-Graphify--Inspired%20(Deterministic)-green)]()

> **Gossip** is a high-performance, single-binary Go Model Context Protocol (MCP) server and companion AI Agent Skill. It equips multi-agent systems (Antigravity, Claude Code, Cursor, Windsurf, OpenCode, and autonomous agent swarms) with:
> - **Formally Defined Specialist Roles** (`orchestrator`, `network_engineer`, `math_expert`, `systems_architect`, `security_specialist`, `qa_engineer`, `researcher`, `worker`).
> - **Asynchronous Inter-Agent Communication** (direct messaging, role-channel broadcasting, discussion threads).
> - **Shared Source-of-Truth Task DAG & Kanban** persisted under `.local/gossip/` with atomic writes, POSIX `flock` concurrency safety, and live auto-rendered `BOARD.md`.
> - **Deterministic Shared Knowledge Graph RAG Space** (Graphify-inspired, no heavy vector DB or embedding API fees) supporting structural code links (`EXTRACTED`), analytical deductions (`INFERRED`), shortest path tracing, and auto-generated `GRAPH_REPORT.md`.
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
    end

    subgraph GossipMCP["⚡ Gossip Server (Single Go Binary)"]
        MCPEngine["MCP Stdio Protocol Handler<br/>(23 Specialized Tools)"]
        MsgBroker["📨 Message Broker<br/>(Direct, Role, Broadcast)"]
        DAGManager["📊 Task DAG & State Engine<br/>(Cycle Detection & Auto-Unblock)"]
        GraphRAG["🧠 Graphify-Style Knowledge Graph<br/>(Nodes, Edges, BFS Paths, Provenance)"]
    end

    subgraph LocalStorage["💾 Shared Source of Truth (.local/gossip/)"]
        LockFile[".lock (POSIX flock / Win32 LockFileEx)"]
        StateJSON["state.json (Agents & Goals)"]
        TasksJSON["tasks.json (Task DAG & Kanban)"]
        MessagesJSONL["messages.jsonl (Append-Only Ledger)"]
        GraphJSON["graph.json (Relational Knowledge Map)"]
        BoardMD["BOARD.md (Live Human Dashboard)"]
        GraphMD["GRAPH_REPORT.md (Hubs & Proofs Summary)"]
    end

    Agents <-->|JSON-RPC 2.0 (MCP)| MCPEngine
    MCPEngine --> MsgBroker
    MCPEngine --> DAGManager
    MCPEngine --> GraphRAG
    MsgBroker <--> LocalStorage
    DAGManager <--> LocalStorage
    GraphRAG <--> LocalStorage
```

---

## 🎭 JSON-Driven Role Taxonomy (SOLID Design)

Roles in Gossip are **never hardcoded in Go source code**. They are dynamically loaded from `roles.json` (`config/roles.json` or `.local/gossip/roles.json`) and can be extended at runtime by agents or operators without recompiling.

Adhering strictly to **SOLID Principles**:
- **Single Responsibility (SRP)**: Distinct subsystems handle storage locking (`pkg/state`), role schema resolution (`pkg/roles`), task DAG evaluation (`pkg/tasks`), and graph traversal (`pkg/graph`).
- **Open/Closed (OCP)**: New roles and node types can be registered dynamically via JSON or the `gossip_register_custom_role` tool without altering existing code.
- **Liskov Substitution (LSP)**: Interface contracts (`roles.Provider`, `tasks.Manager`, `state.Store`) can be swapped or extended without breaking higher-level consumers.
- **Interface Segregation (ISP)**: Interfaces are strictly partitioned by capability (Agent Registry vs Task Engine vs RAG).
- **Dependency Inversion (DIP)**: The MCP server and CLI depend on abstract service interfaces injected at construction time.

| Role ID | Title | Specialization & Behavioral Directives | Focus Areas |
| :--- | :--- | :--- | :--- |
| `orchestrator` | Lead System Orchestrator | Decomposes goals into dependency-aware task DAGs (`blocked_by`). Delegates to specialists, resolves cross-domain blockers, monitors `BOARD.md`, and synthesizes final project deliverables. | Planning, delegation, milestones, architecture. |
| `worker` | General Software Engineer | Claims ready tasks (`todo` $\to$ `in_progress`), writes clean, idiomatic code, logs discovered symbols into the knowledge graph, and posts result deliverables. | Implementation, refactoring, feature authoring. |
| `network_engineer` | Distributed Systems & Network Engineer | Protocol design, length-prefixed binary framing, socket non-blocking multiplexing, connection state machines, serialization overhead, latency budgets, exponential backoff. | Protocols, framing, sockets, mesh, packet efficiency. |
| `math_expert` | Mathematical & Algorithmic Specialist | Algorithmic invariants, time/space asymptotic bounds ($O, \Omega, \Theta$), graph algorithms (cycle detection, partitioning, BFS/DFS), CRC32 polynomial optimality, numerical stability. | Formal proofs, complexity bounds, graph theory, probability. |
| `systems_architect` | Low-Level Systems & OS Architect | Concurrency safety, race-condition elimination, POSIX file locking (`flock`), atomic rename persistence, cache locality, memory allocation churn, zero-copy I/O. | Memory safety, OS primitives, thread synchronization, IPC. |
| `security_specialist` | Security & Vulnerability Auditor | Threat modeling (STRIDE), input sanitization, directory traversal defense, cryptographically sound primitives, deserialization safety, denial-of-service resilience. | OWASP, crypto validation, sandboxing, vulnerability audit. |
| `qa_engineer` | Verification & Test Specialist | Deterministic test harnesses, property-based verification, boundary condition analysis (empty, singular, max capacity), race detection (`-race`), invariant validation. | Regression suites, edge cases, integration verification. |
| `researcher` | Deep Research & Explorer | Explores external documentation, RFC standards, competitor benchmarks, and extracts structured concepts into the shared knowledge graph. | RFCs, documentation, benchmarks, state-of-the-art surveying. |

---

## 🛠️ MCP Tool Directory (32 Granular Tools)

### 1. Identity, Roles & Multi-Instance Management
- `gossip_register_agent`: Join the active gossip mesh with agent ID, role, capabilities, and active task. Supports multiple instances per role (e.g. `worker-1`, `worker-2`, `red-team-1`, `red-team-2`).
- `gossip_list_agents`: List all active agents, their statuses, tasks, and last heartbeats.
- `gossip_heartbeat`: Refresh session timestamp and update current task or status.
- `gossip_get_role_definition`: Fetch complete system prompt, directives, and tool recommendations for any role.
- `gossip_list_roles`: View markdown directory of all roles loaded dynamically from JSON.
- `gossip_register_custom_role`: Dynamically define and register a new custom agent role at runtime in JSON.

### 2. Multi-Agent Deliberations: Debating, Voting, Peer Review & Red Teaming
- `gossip_start_debate`: Open a structured multi-agent debate session on an architectural trade-off or controversial proposal.
- `gossip_post_argument`: Post a pro, con, critique, rebuttal, or neutral argument in an active debate round.
- `gossip_cast_vote`: Cast a formal vote on a debate proposal with confidence level (1-5) and rationale.
- `gossip_conclude_debate`: Tally votes, determine consensus outcome, and automatically record the adopted decision in the knowledge graph.
- `gossip_request_review`: Request formal peer review or red-team critique on a task, artifact, or code file.
- `gossip_submit_review`: Submit structured review findings (`minor`, `major`, `critical`) and issue a formal verdict.
- `gossip_log_red_team_probe`: Log an adversarial exploit, boundary attack, or data race with reproduction payloads and link a remediation task.
- `gossip_list_deliberations`: List active or historical debates, peer reviews, and red team probes.

### 3. Inter-Agent Messaging Tools
- `gossip_send_message`: Send point-to-point message (`agent:<id>`), role-targeted message (`role:<role_id>`), or broadcast (`all`).
- `gossip_broadcast_message`: Broadcast high-priority announcements to all agents.
- `gossip_read_inbox`: Retrieve incoming messages filtered by agent ID, role, unread status, or thread ID.
- `gossip_mark_messages_read`: Mark messages as acknowledged.
- `gossip_list_threads`: Inspect active multi-agent conversation threads.

### 4. Shared Source of Truth: Goals & Tasks
- `gossip_set_goal`: Define project objectives and milestone checkpoints.
- `gossip_get_goals`: Retrieve active goals and milestone completion status.
- `gossip_create_task`: Create a task in the DAG with dependencies (`blocked_by`), priority, and role assignee.
- `gossip_update_task`: Advance task state (`todo` $\to$ `in_progress` $\to$ `completed`), append progress notes, or register result artifacts. *(Completing a blocker automatically unblocks dependent tasks!)*
- `gossip_list_tasks`: Query tasks filtered by status, role, or assignee.
- `gossip_get_task`: Retrieve full task specification, dependencies, and history notes.
- `gossip_render_board`: Regenerate and return the live Markdown Kanban board (`BOARD.md`).

### 5. Graphify-Style Shared RAG Space (Knowledge Graph)
- `gossip_rag_add_node`: Add a concept, file, symbol, decision, constraint, or formal proof.
- `gossip_rag_add_edge`: Connect entities with typed relationships (`depends_on`, `implements`, `verifies`, `calls`, `solves`, `conflicts_with`) and provenance (`EXTRACTED` vs `INFERRED`).
- `gossip_rag_query`: Search graph using keywords, type filter, and tag filter with relevance scoring.
- `gossip_rag_get_neighborhood`: Inspect 1-hop or 2-hop connected subgraphs around any node.
- `gossip_rag_find_path`: Compute the shortest relationship path between any two concepts using BFS.
- `gossip_rag_export_report`: Generate and return the human-readable `GRAPH_REPORT.md` summary.

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

# List tasks in terminal table format
gossip tasks

# List active and past debates with vote tallies
gossip debates

# List adversarial red team security probes
gossip probes

# Send a message directly from terminal
gossip send --from dev_terminal --role orchestrator --to role:math_expert --msg "Please analyze time complexity of partition algorithm"

# Print all defined roles and system prompts
gossip roles
```

---

## 🧠 The Deterministic Knowledge Graph (Graphify Inspired)

Unlike traditional vector stores that require external embedding API calls, high token consumption, and probabilistic fuzzy matching, Gossip utilizes a **fully deterministic local knowledge graph**:

1. **Relational Provenance**:
   - `EXTRACTED`: Explicit structural links discovered directly from code AST, file imports, or schemas.
   - `INFERRED`: Relational links deduced by specialist agents during design and verification.
2. **Topological Insights**:
   - Automatically computes node degree centrality to surface **Core Architectural Hubs**.
   - Path-finding (`gossip_rag_find_path`) uncovers transitive dependencies and hidden coupling.
   - Neighborhood extraction allows agents to inspect 1-hop or 2-hop subgraphs before making breaking refactors.
3. **Artifact Output**:
   - Persisted to `.local/gossip/graph.json`.
   - Human-readable summary exported to `.local/gossip/GRAPH_REPORT.md`.

---

## 🧪 Testing & Verification

Gossip includes unit and integration tests with concurrency race detection:

```bash
# Run full test suite with race detector
go test -v -race ./...
```

All subsystems (state locking, append-only message streaming, task DAG validation, BFS graph pathfinding, and MCP tool serialization) are thoroughly verified.

---

## 📜 License

This project is licensed under the **GNU Affero General Public License v3.0 (AGPLv3)**. See [LICENSE](LICENSE) for the full license text.
