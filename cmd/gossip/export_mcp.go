package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tasmirz/gossip/pkg/mcp"
	"github.com/tasmirz/gossip/pkg/state"
)

type toolExportSchema struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
	InputSchema interface{} `json:"inputSchema,omitempty"`
}

type mcpServerManifest struct {
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	Description  string                `json:"description"`
	Capabilities map[string]interface{} `json:"capabilities"`
	Tools        []toolExportSummary   `json:"tools"`
	Prompts      []promptExportSummary `json:"prompts"`
}

type toolExportSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type promptExportSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func runExportMCP(args []string) {
	targetDir := "mcp"
	for i := 0; i < len(args); i++ {
		if (args[i] == "--out" || args[i] == "--dir") && i+1 < len(args) {
			targetDir = args[i+1]
			i++
		}
	}

	tempStoreDir, err := os.MkdirTemp("", "gossip-export-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp dir for store: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempStoreDir)

	store, err := state.NewStore(tempStoreDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing state store: %v\n", err)
		os.Exit(1)
	}

	srv := mcp.NewServer(store, nil)
	mcpSrv := srv.MCPServer()

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output dir %s: %v\n", targetDir, err)
		os.Exit(1)
	}

	rawTools := mcpSrv.ListTools()
	toolNames := make([]string, 0, len(rawTools))
	for name := range rawTools {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	var manifestTools []toolExportSummary
	for _, name := range toolNames {
		st := rawTools[name]
		t := st.Tool

		schema := toolExportSchema{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			InputSchema: t.InputSchema,
		}

		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling tool %s: %v\n", name, err)
			continue
		}

		filePath := filepath.Join(targetDir, fmt.Sprintf("%s.json", name))
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing schema file %s: %v\n", filePath, err)
		}

		manifestTools = append(manifestTools, toolExportSummary{
			Name:        t.Name,
			Description: t.Description,
		})
	}

	rawPrompts := mcpSrv.ListPrompts()
	promptNames := make([]string, 0, len(rawPrompts))
	for name := range rawPrompts {
		promptNames = append(promptNames, name)
	}
	sort.Strings(promptNames)

	var manifestPrompts []promptExportSummary
	for _, name := range promptNames {
		sp := rawPrompts[name]
		manifestPrompts = append(manifestPrompts, promptExportSummary{
			Name:        sp.Prompt.Name,
			Description: sp.Prompt.Description,
		})
	}

	manifest := mcpServerManifest{
		Name:        "Gossip Agent Mesh",
		Version:     Version,
		Description: "Multi-agent coordination fabric, task DAG delegation, MetaGPT-style Standard Operating Procedures (SOPs), inter-agent messaging, Graphify blast-radius RAG, Aider-style PageRank repo-mapping, Multi-Agent Debate (MAD) with Borda consensus, and STRIDE red-teaming.",
		Capabilities: map[string]interface{}{
			"tools":   map[string]bool{"listChanged": true},
			"prompts": map[string]bool{"listChanged": true},
		},
		Tools:   manifestTools,
		Prompts: manifestPrompts,
	}

	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(targetDir, "server.json"), manifestData, 0644)

	instructions := "# Gossip MCP Tools Instructions\n\n" +
		"Gossip connects multiple concurrent AI coding agents into an organized, high-velocity engineering organization.\n" +
		"All state is persistently synchronized with non-blocking atomic POSIX file locks in `<project_root>/.local/gossip/`.\n\n" +
		"## Core MCP Tool Workflows\n\n" +
		"1. **Role Identification & Presence**:\n" +
		"   - `gossip_register_agent`: Register your identity and assumed role (e.g. `orchestrator`, `worker`, `systems_architect`, `security_specialist`, `math_expert`, `network_engineer`, `qa_engineer`, `researcher`, `red_team`, `critic`).\n" +
		"   - `gossip_heartbeat`: Record periodic liveness.\n" +
		"   - `gossip_list_roles` & `gossip_get_role_definition`: Query active role contracts.\n" +
		"   - `gossip_register_custom_role`: Define new project-specific roles dynamically.\n\n" +
		"2. **Inter-Agent Messaging & Continuous Sync**:\n" +
		"   - `gossip_send_message`: Send targeted direct messages (`agent:<id>`) or role broadcasts (`role:<role_id>`).\n" +
		"   - `gossip_broadcast_message`: Broadcast notifications across all agents.\n" +
		"   - `gossip_read_inbox`: Retrieve unread messages for your agent.\n" +
		"   - `gossip_poll_events` (alias `gsp_poll`): Long-poll for incoming messages, unblocked DAG tasks, active debates, and SOP phase updates with defined timeouts.\n\n" +
		"3. **Multi-Level Task Management**:\n" +
		"   - **Personal Scratchpad**: `gossip_personal_todo_add`, `gossip_personal_todo_list`, `gossip_personal_todo_update`, `gossip_personal_todo_delete` (private agent to-dos).\n" +
		"   - **System Backlog**: `gossip_system_todo_list` (cross-system backlog uniting ready DAG tasks, milestones, SOP gates, and STRIDE vulnerabilities).\n" +
		"   - **Orchestration Matrix**: `gossip_orchestrator_assignments` (team-wide workload breakdown, bottleneck detection, and idle-worker delegation suggestions).\n" +
		"   - **Task DAG Execution**: `gossip_create_task`, `gossip_delegate_task`, `gossip_update_task_status`, `gossip_get_board`, `gossip_get_tasks`.\n\n" +
		"4. **Shared Knowledge Graph RAG (Graphify + Aider)**:\n" +
		"   - `gossip_rag_query`: Semantic & relational query across codebase nodes, facts, and ADRs.\n" +
		"   - `gossip_rag_index_codebase` (alias `gsp_index`): Deterministically parse AST of all Go packages, structs, functions, and import edges.\n" +
		"   - `gossip_rag_blast_radius`: Compute cascading blast-radius and affected nodes before modifying files.\n" +
		"   - `gossip_rag_pagerank`: Architectural centrality scoring.\n" +
		"   - `gossip_rag_repomap`: Token-budgeted repo-map for LLM context.\n" +
		"   - `gossip_rag_clusters`: Detect architectural submodules and connected clusters.\n\n" +
		"5. **MetaGPT-Style Standard Operating Procedures (SOPs)**:\n" +
		"   - `gossip_sop_start`: Launch formal workflows (`feature_delivery`, `bug_investigation`, `security_hardening`).\n" +
		"   - `gossip_sop_advance`: Advance through strict phase gates with versioned markdown artifacts in `.local/gossip/artifacts/`.\n" +
		"   - `gossip_sop_status` & `gossip_sop_history`: Inspect workflow stage progression.\n\n" +
		"6. **Multi-Agent Debate & Borda Consensus**:\n" +
		"   - `gossip_start_debate`: Initiate formal architectural or algorithmic debate.\n" +
		"   - `gossip_contribute_argument`: Submit evidence-backed positions.\n" +
		"   - `gossip_advance_debate_round`: Trigger peer critique rounds.\n" +
		"   - `gossip_cast_vote`: Cast ranked-choice votes with confidence weights.\n" +
		"   - `gossip_conclude_debate`: Finalize debate via Borda Count consensus and commit decision as an ADR to the RAG graph.\n\n" +
		"7. **STRIDE Red-Teaming & Guardrails**:\n" +
		"   - `gossip_redteam_generate_probes`: Generate adversarial security probes across Spoofing, Tampering, Repudiation, Information Disclosure, Denial of Service, and Elevation of Privilege.\n" +
		"   - `gossip_redteam_report`: Compile executive AUDIT_REPORT.md.\n" +
		"   - `gossip_guardrail_check`: Pre-screen actions against security invariants and caution rules.\n" +
		"   - `gossip_guardrail_derive`: Synthesize project guardrails and generate AGENTS.md.\n"

	_ = os.WriteFile(filepath.Join(targetDir, "instructions.md"), []byte(instructions), 0644)

	fmt.Printf("Successfully exported %d MCP tool schemas, server manifest, and instructions to '%s'\n", len(manifestTools), targetDir)
}
