package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/events"
	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/guardrails"
	"github.com/tasmirz/gossip/pkg/mcp"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/moe"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
	"github.com/tasmirz/gossip/pkg/web"
)

const (
	Version   = "1.0.0"
	BannerArt = `
  ____                 _       
 / ___| ___  ___  ___ (_)_ __  
| |  _ / _ \/ __|/ __|| | '_ \ 
| |_| | (_) \__ \\__ \| | |_) |
 \____|\___/|___/|___/|_| .__/ 
                        |_|    
Multi-Agent Collaboration Mesh & Shared RAG Space (AGPLv3)
`
)

func main() {
	if len(os.Args) < 2 {
		// Default to MCP stdio mode if no args, perfect for MCP clients
		runMCPServer("")
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "mcp", "serve":
		dir := parseDirFlag(os.Args[2:])
		runMCPServer(dir)

	case "init":
		dir := parseDirFlag(os.Args[2:])
		runInit(dir)

	case "status":
		dir := parseDirFlag(os.Args[2:])
		runStatus(dir)

	case "board":
		dir := parseDirFlag(os.Args[2:])
		runBoard(dir)

	case "report":
		dir := parseDirFlag(os.Args[2:])
		runReport(dir)

	case "tasks":
		dir := parseDirFlag(os.Args[2:])
		runTasks(dir)

	case "debates":
		dir := parseDirFlag(os.Args[2:])
		runDebates(dir)

	case "probes":
		dir := parseDirFlag(os.Args[2:])
		runProbes(dir)

	case "pagerank":
		dir := parseDirFlag(os.Args[2:])
		runPageRank(dir)

	case "impact":
		runImpact(os.Args[2:])

	case "clusters":
		dir := parseDirFlag(os.Args[2:])
		runClusters(dir)

	case "repomap":
		dir := parseDirFlag(os.Args[2:])
		runRepoMap(dir)

	case "index", "gsp:index":
		runIndex(os.Args[2:])

	case "audit":
		dir := parseDirFlag(os.Args[2:])
		runAudit(dir)

	case "sop":
		dir := parseDirFlag(os.Args[2:])
		runSOP(dir)

	case "derive", "gsp:derive":
		runDerive(os.Args[2:])

	case "check", "gsp:check":
		runCheck(os.Args[2:])

	case "guardrails", "gsp:guardrails":
		dir := parseDirFlag(os.Args[2:])
		runGuardrails(dir)

	case "agents", "gsp:agents":
		dir := parseDirFlag(os.Args[2:])
		runAgents(dir)

	case "poll", "gsp:poll":
		runPoll(os.Args[2:])

	case "watch", "gsp:watch":
		runWatch(os.Args[2:])

	case "todo", "gsp:my_todos":
		runPersonalTodo(os.Args[2:])

	case "system-todos", "gsp:system_todos":
		dir := parseDirFlag(os.Args[2:])
		runSystemTodos(dir)

	case "assignments", "gsp:assignments":
		dir := parseDirFlag(os.Args[2:])
		runAssignments(dir)

	case "resume", "reidentify", "gsp:resume":
		runResume(os.Args[2:])

	case "ui", "web":
		runWebUI(os.Args[2:])

	case "moe", "gsp:moe":
		runMoE(os.Args[2:])

	case "export-mcp", "mcp-export":
		runExportMCP(os.Args[2:])

	case "roles":
		fmt.Println(roles.FormatRoleSummary())

	case "send":
		runSend(os.Args[2:])

	case "version", "-v", "--version":
		fmt.Printf("Gossip v%s (linux/amd64, AGPLv3)\n", Version)

	case "help", "-h", "--help":
		printHelp()

	default:
		// If command starts with "-", maybe flags for mcp
		if strings.HasPrefix(cmd, "-") {
			dir := parseDirFlag(os.Args[1:])
			runMCPServer(dir)
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nRun 'gossip help' for usage.\n", cmd)
		os.Exit(1)
	}
}

func parseDirFlag(args []string) string {
	fs := flag.NewFlagSet("cmd", flag.ContinueOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory (default: .local/gossip)")
	_ = fs.Parse(args)
	return *dir
}

func runMCPServer(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize storage at %s: %v\n", customDir, err)
		os.Exit(1)
	}

	roleSrv, _ := roles.NewService(store.Dir(), "")
	srv := mcp.NewServer(store, roleSrv)
	if err := srv.ServeStdio(); err != nil {
		fmt.Fprintf(os.Stderr, "Gossip MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func runInit(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating store: %v\n", err)
		os.Exit(1)
	}

	// Initialize default state
	st, err := store.LoadProjectState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading state: %v\n", err)
		os.Exit(1)
	}
	if err := store.SaveProjectState(st); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
		os.Exit(1)
	}

	// Initialize roles JSON file
	roleSrv, _ := roles.NewService(store.Dir(), "")
	_ = roleSrv.SaveToFile(filepath.Join(store.Dir(), "roles.json"))

	// Initialize tasks manager & board
	tm := tasks.NewManager(store)
	tc, err := tm.LoadContainer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading tasks: %v\n", err)
		os.Exit(1)
	}
	if err := tm.SaveContainer(tc); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	// Initialize knowledge graph
	gm := graph.NewManager(store)
	g, err := gm.LoadGraph()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading graph: %v\n", err)
		os.Exit(1)
	}

	// Add initial seed node
	wd, _ := os.Getwd()
	projName := filepath.Base(wd)
	_, _ = gm.AddNode(graph.Node{
		ID:        fmt.Sprintf("project:%s", projName),
		Type:      graph.TypeArchitecture,
		Label:     projName,
		Summary:   fmt.Sprintf("Root project entity for %s", projName),
		CreatedBy: "gossip-init",
	})
	_ = gm.SaveGraph(g)

	fmt.Printf("%s\n", BannerArt)
	fmt.Printf("✓ Initialized Gossip mesh in: %s\n", store.Dir())
	fmt.Printf("  ├── state.json\n")
	fmt.Printf("  ├── tasks.json\n")
	fmt.Printf("  ├── messages.jsonl\n")
	fmt.Printf("  ├── graph.json\n")
	fmt.Printf("  ├── roles.json (JSON-driven role specifications)\n")
	fmt.Printf("  ├── BOARD.md\n")
	fmt.Printf("  └── GRAPH_REPORT.md\n\n")
	fmt.Printf("Ready for multi-agent collaboration!\n")
}

func runStatus(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	agents, err := store.ListAgents()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading agents: %v\n", err)
		os.Exit(1)
	}

	goals, _ := store.GetGoals()
	tm := tasks.NewManager(store)
	taskList, _ := tm.ListTasks("", "", "")
	gm := graph.NewManager(store)
	g, _ := gm.LoadGraph()

	fmt.Printf("%s\n", BannerArt)
	fmt.Printf("Location: %s\n", store.Dir())
	fmt.Printf("Active Goals: %d | Total Tasks: %d | Knowledge Nodes: %d | Edges: %d\n\n",
		len(goals), len(taskList), len(g.Nodes), len(g.Edges))

	fmt.Printf("=== Registered Agents (%d) ===\n", len(agents))
	if len(agents) == 0 {
		fmt.Println("No active agents registered.")
	} else {
		for _, a := range agents {
			fmt.Printf("• [%s] %s (Role: %s) Status: %s | Task: %s\n",
				a.ID, a.Name, a.Role, a.Status, a.CurrentTaskID)
		}
	}
	fmt.Println()

	// Task breakdown
	statusCounts := make(map[string]int)
	for _, t := range taskList {
		statusCounts[t.Status]++
	}
	fmt.Println("=== Task DAG Breakdown ===")
	for _, st := range []string{"todo", "in_progress", "blocked", "in_review", "completed", "failed"} {
		fmt.Printf("  %-12s: %d\n", st, statusCounts[st])
	}
	fmt.Println()
}

func runBoard(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tm := tasks.NewManager(store)
	board, err := tm.RenderBoard()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error rendering board: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(board)
}

func runReport(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := graph.NewManager(store)
	rep, err := gm.GenerateReport()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating report: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(rep)
}

func runTasks(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tm := tasks.NewManager(store)
	list, err := tm.ListTasks("", "", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(list) == 0 {
		fmt.Println("No tasks found in project.")
		return
	}

	fmt.Printf("%-10s %-12s %-10s %-15s %s\n", "ID", "STATUS", "PRIORITY", "ROLE", "TITLE")
	fmt.Println(strings.Repeat("-", 75))
	for _, t := range list {
		fmt.Printf("%-10s %-12s %-10s %-15s %s\n", t.ID, t.Status, t.Priority, t.Role, t.Title)
	}
}

func runDebates(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	dm := deliberation.NewManager(store)
	list, err := dm.ListDebates("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading debates: %v\n", err)
		os.Exit(1)
	}

	if len(list) == 0 {
		fmt.Println("No active or past debates found.")
		return
	}

	fmt.Printf("%-10s %-14s %-20s %s\n", "ID", "STATUS", "PROPOSER", "TITLE")
	fmt.Println(strings.Repeat("-", 75))
	for _, d := range list {
		fmt.Printf("%-10s %-14s %-20s %s\n", d.ID, strings.ToUpper(d.Status), d.Proposer, d.Title)
	}
}

func runProbes(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	dm := deliberation.NewManager(store)
	list, err := dm.ListProbes("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading probes: %v\n", err)
		os.Exit(1)
	}

	if len(list) == 0 {
		fmt.Println("No red team security probes logged.")
		return
	}

	fmt.Printf("%-10s %-14s %-16s %-22s %s\n", "ID", "STATUS", "ATTACKER", "THREAT", "TARGET")
	fmt.Println(strings.Repeat("-", 85))
	for _, p := range list {
		fmt.Printf("%-10s %-14s %-16s %-22s %s\n", p.ID, strings.ToUpper(p.Status), p.AttackerID, p.ThreatCategory, p.Target)
	}
}

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	from := fs.String("from", "cli-user", "Sender ID")
	role := fs.String("role", "orchestrator", "Sender Role")
	to := fs.String("to", "all", "Recipient: 'all', 'role:<role>', 'agent:<id>'")
	msg := fs.String("msg", "", "Message content")
	subject := fs.String("subject", "CLI Message", "Message subject")
	dir := fs.String("dir", "", "Storage directory")
	_ = fs.Parse(args)

	if *msg == "" {
		fmt.Fprintln(os.Stderr, "Error: --msg is required")
		os.Exit(1)
	}

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	mm := messages.NewManager(store)
	m, err := mm.SendMessage(*from, *role, *to, "", *subject, *msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Message sent [%s] to %s: %s\n", m.ID, m.To, m.Content)
}

func runPageRank(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := graph.NewManager(store)
	scores, err := gm.ComputePageRank(0.85, 100, 1e-5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error calculating PageRank: %v\n", err)
		os.Exit(1)
	}

	if len(scores) == 0 {
		fmt.Println("No nodes found in knowledge graph.")
		return
	}

	fmt.Printf("%-24s %-12s %-12s %-8s %-8s %s\n", "NODE ID", "TYPE", "PAGERANK", "IN-DEG", "OUT-DEG", "LABEL")
	fmt.Println(strings.Repeat("-", 80))
	for _, s := range scores {
		fmt.Printf("%-24s %-12s %-12.4f %-8d %-8d %s\n", s.NodeID, s.Type, s.PageRank, s.InDegree, s.OutDegree, s.Label)
	}
}

func runImpact(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "Usage: gossip impact <node_id> [--depth <int>] [--dir <path>]")
		os.Exit(1)
	}
	seedID := args[0]
	fs := flag.NewFlagSet("impact", flag.ExitOnError)
	depth := fs.Int("depth", 2, "Max traversal depth")
	dir := fs.String("dir", "", "Storage directory")
	_ = fs.Parse(args[1:])

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := graph.NewManager(store)
	report, err := gm.AffectedNodes(seedID, *depth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Blast-radius error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🎯 Blast-Radius Impact Analysis for [%s] (%s)\n", report.SeedNodeID, report.SeedLabel)
	fmt.Printf("Total Affected Components: %d | Blast Radius Score: %.2f / 100.0\n\n", report.TotalAffectedCount, report.BlastRadiusScore)

	if len(report.DirectHits) > 0 {
		fmt.Printf("=== Direct Dependents (Depth 1) [%d] ===\n", len(report.DirectHits))
		for _, h := range report.DirectHits {
			fmt.Printf("  • %-20s (%s) via %s from %s\n", h.NodeID, h.Type, h.ViaRelation, h.ViaSource)
		}
		fmt.Println()
	}

	if len(report.IndirectHits) > 0 {
		fmt.Printf("=== Transitive Dependents (Depth 2+) [%d] ===\n", len(report.IndirectHits))
		for _, h := range report.IndirectHits {
			fmt.Printf("  • %-20s (Depth %d, %s) via %s from %s\n", h.NodeID, h.Depth, h.Type, h.ViaRelation, h.ViaSource)
		}
		fmt.Println()
	}

	if len(report.RecommendedVerifications) > 0 {
		fmt.Println("=== Recommended Verifications ===")
		for _, rec := range report.RecommendedVerifications {
			fmt.Printf("  ✓ %s\n", rec)
		}
	}
}

func runClusters(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := graph.NewManager(store)
	clusters, err := gm.DetectClusters()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cluster error: %v\n", err)
		os.Exit(1)
	}

	if len(clusters) == 0 {
		fmt.Println("No clusters identified.")
		return
	}

	for _, c := range clusters {
		fmt.Printf("📦 %s (Hub: %s, Size: %d)\n", c.ClusterName, c.HubNode, c.Size)
		for _, n := range c.Nodes {
			fmt.Printf("   - %s\n", n)
		}
		fmt.Println()
	}
}

func runRepoMap(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := graph.NewManager(store)
	repoMap, err := gm.GenerateRepoMap(1000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Repo-map error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(repoMap)
}

func runAudit(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	dm := deliberation.NewManager(store)
	report, err := dm.GenerateAuditReport()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Audit report error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(report)
}

func runSOP(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	sm := sop.NewManager(store)
	wfs, err := sm.ListWorkflows()
	if err != nil {
		fmt.Fprintf(os.Stderr, "SOP error: %v\n", err)
		os.Exit(1)
	}

	if len(wfs) == 0 {
		fmt.Println("No active SOP workflows found. Available templates:")
		for name, stages := range sop.GetWorkflowTemplates() {
			fmt.Printf("  • Template: %-20s (%d phase gates)\n", name, len(stages))
		}
		return
	}

	fmt.Printf("%-24s %-16s %-10s %s\n", "WORKFLOW ID", "TEMPLATE", "STATUS", "TITLE")
	fmt.Println(strings.Repeat("-", 75))
	for _, wf := range wfs {
		fmt.Printf("%-24s %-16s %-10s %s\n", wf.ID, wf.Template, strings.ToUpper(wf.Status), wf.Title)
	}
}

func runDerive(args []string) {
	fs := flag.NewFlagSet("derive", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory")
	name := fs.String("name", "", "Project name")
	stack := fs.String("stack", "Go", "Comma-separated tech stack list")
	goal := fs.String("goal", "High quality, resilient system implementation", "Project goal")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	projectName := *name
	if projectName == "" {
		wd, _ := os.Getwd()
		projectName = filepath.Base(wd)
	}

	var stackList []string
	for _, s := range strings.Split(*stack, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			stackList = append(stackList, s)
		}
	}

	gm := guardrails.NewManager(store)
	pg, err := gm.DeriveProjectRoles(projectName, stackList, *goal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Derive error: %v\n", err)
		os.Exit(1)
	}

	// Register with role provider
	roleSrv, _ := roles.NewService(store.Dir(), "")
	for _, r := range pg.Roles {
		_ = roleSrv.RegisterRole(roles.RoleDefinition{
			ID:           r.RoleID,
			Title:        r.Title,
			Category:     "derived",
			Description:  r.Description,
			SystemPrompt: r.SystemPrompt,
			Directives:   append(r.CautionDirectives, r.ForbiddenActions...),
		})
	}
	_ = roleSrv.SaveToFile(filepath.Join(store.Dir(), "roles.json"))

	fmt.Printf("✓ Derived %d roles & guardrails for project '%s'\n", len(pg.Roles), pg.ProjectName)
	fmt.Printf("  • Stored configuration: %s\n", filepath.Join(store.Dir(), "guardrails.json"))
	fmt.Printf("  • Generated directives: %s\n", filepath.Join(store.Dir(), "AGENTS.md"))
	fmt.Println("\n=== Derived Roles ===")
	for _, r := range pg.Roles {
		fmt.Printf("  • %-22s: %s (Permitted: %d, Forbidden: %d)\n", r.RoleID, r.Title, len(r.PermittedActions), len(r.ForbiddenActions))
	}
	fmt.Printf("\n=== Project Invariants (%d) ===\n", len(pg.Invariants))
	for i, inv := range pg.Invariants {
		fmt.Printf("  %d. %s\n", i+1, inv)
	}
}

func runCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory")
	role := fs.String("role", "worker", "Agent role attempting action")
	action := fs.String("action", "write_file", "Action to perform (e.g. write_file, edit_config)")
	path := fs.String("path", "", "Target file or resource path")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := guardrails.NewManager(store)
	res, err := gm.ValidateProposedAction(*role, *action, *path, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Check error: %v\n", err)
		os.Exit(1)
	}

	if res.Allowed {
		fmt.Printf("🟢 [ALLOWED] Action '%s' by role '%s' on '%s' is permitted.\n", *action, *role, *path)
	} else {
		fmt.Printf("🔴 [BLOCKED] Action '%s' by role '%s' on '%s' is BLOCKED.\n", *action, *role, *path)
		fmt.Printf("   Reason: %s\n", res.Reason)
		fmt.Printf("   Enforcement: %s\n", res.Enforcement)
	}

	if len(res.ViolatedDirectives) > 0 {
		fmt.Println("\n⚠️ Directives Flagged:")
		for _, c := range res.ViolatedDirectives {
			fmt.Printf("   • %s\n", c)
		}
	}
}

func runGuardrails(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := guardrails.NewManager(store)
	pg, err := gm.GetGuardrails()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== Guardrails: %s ===\n", pg.ProjectName)
	fmt.Printf("Tech Stack: %s\n", strings.Join(pg.TechStack, ", "))
	fmt.Printf("\nProject Invariants (%d):\n", len(pg.Invariants))
	for i, inv := range pg.Invariants {
		fmt.Printf("  %d. %s\n", i+1, inv)
	}

	fmt.Printf("\nCaution Rules (%d):\n", len(pg.Cautions))
	for _, c := range pg.Cautions {
		fmt.Printf("  • [%s] %s (Trigger: %s | %s)\n", strings.ToUpper(c.Level), c.Directive, c.TriggerPattern, c.Enforcement)
	}
}

func runAgents(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := guardrails.NewManager(store)
	md, err := gm.ExportAgentsMD()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exporting AGENTS.md: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(md)
}

func runPoll(args []string) {
	fs := flag.NewFlagSet("poll", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory")
	agent := fs.String("agent", "cli-agent", "Calling agent ID")
	role := fs.String("role", "worker", "Calling agent role")
	since := fs.String("since", "5m", "Since timestamp or relative duration (e.g. 5m, 1h, RFC3339)")
	timeout := fs.Int("timeout", 0, "Long-polling timeout in seconds (0 returns immediately)")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	sinceTime := time.Now().Add(-5 * time.Minute)
	if *since != "" {
		clean := strings.TrimSuffix(*since, " ago")
		if d, err := time.ParseDuration(clean); err == nil {
			sinceTime = time.Now().Add(-d)
		} else if t, err := time.Parse(time.RFC3339, *since); err == nil {
			sinceTime = t
		}
	}

	msgMgr := messages.NewManager(store)
	taskMgr := tasks.NewManager(store)
	delibMgr := deliberation.NewManager(store)
	sopMgr := sop.NewManager(store)
	evtMgr := events.NewManager(store, msgMgr, taskMgr, delibMgr, sopMgr)

	resp, err := evtMgr.Poll(context.Background(), events.PollRequest{
		AgentID:       *agent,
		Role:          *role,
		SinceTime:     sinceTime,
		Timeout:       time.Duration(*timeout) * time.Second,
		AutoHeartbeat: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Poll error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("⏱️ Server Time: %s | Poll Duration: %d ms | Updates Found: %v\n",
		resp.ServerTime.Format(time.RFC3339), resp.PollDurationMs, resp.HasUpdates)
	fmt.Printf("📬 Unread Messages: %d | 📋 Ready Tasks: %d | 👥 Active Agents: %d\n",
		resp.UnreadMessagesCount, resp.ReadyTasksCount, resp.ActiveAgentsCount)

	if len(resp.Events) > 0 {
		fmt.Printf("\n=== Events (%d) ===\n", len(resp.Events))
		for _, e := range resp.Events {
			fmt.Printf("  • [%s] %s (%s)\n    %s\n",
				e.Timestamp.Format("15:04:05"), strings.ToUpper(e.Type), e.ID, e.Summary)
		}
	} else {
		fmt.Println("\nNo new events in polled window.")
	}
}

func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory")
	agent := fs.String("agent", "cli-watcher", "Watcher agent ID")
	role := fs.String("role", "orchestrator", "Watcher role")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	msgMgr := messages.NewManager(store)
	taskMgr := tasks.NewManager(store)
	delibMgr := deliberation.NewManager(store)
	sopMgr := sop.NewManager(store)
	evtMgr := events.NewManager(store, msgMgr, taskMgr, delibMgr, sopMgr)

	fmt.Printf("%s\n", BannerArt)
	fmt.Printf("👀 Watching Gossip event stream in %s (Ctrl+C to stop)...\n", store.Dir())
	fmt.Println(strings.Repeat("-", 65))

	cursor := time.Now().Add(-1 * time.Minute)
	for {
		resp, err := evtMgr.Poll(context.Background(), events.PollRequest{
			AgentID:       *agent,
			Role:          *role,
			SinceTime:     cursor,
			Timeout:       10 * time.Second,
			AutoHeartbeat: true,
		})
		if err == nil {
			cursor = resp.ServerTime
			for _, e := range resp.Events {
				fmt.Printf("[%s] %-16s %s\n", e.Timestamp.Format("15:04:05"), strings.ToUpper(e.Type), e.Summary)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func runIndex(args []string) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory")
	root := fs.String("root", ".", "Root codebase directory to index")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	gm := graph.NewManager(store)
	absRoot, _ := filepath.Abs(*root)
	summary, err := gm.IndexCodebase(absRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Indexing error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s\n", BannerArt)
	fmt.Printf("✓ Codebase indexed into shared RAG knowledge graph (%s) in %d ms\n", store.Dir(), summary.DurationMs)
	fmt.Printf("  • Files Scanned    : %d\n", summary.FilesScanned)
	fmt.Printf("  • Packages Found   : %d\n", summary.PackagesFound)
	fmt.Printf("  • Symbols Extracted: %d\n", summary.SymbolsExtracted)
	fmt.Printf("  • Total Nodes Added: %d\n", summary.NodesAdded)
	fmt.Printf("  • Total Edges Added: %d\n", summary.EdgesAdded)
	fmt.Printf("  • Report Generated : %s\n", filepath.Join(store.Dir(), "GRAPH_REPORT.md"))
}

func runPersonalTodo(args []string) {
	if len(args) > 0 && args[0] == "add" {
		fs := flag.NewFlagSet("todo add", flag.ExitOnError)
		dir := fs.String("dir", "", "Path to gossip storage directory")
		agent := fs.String("agent", "cli-agent", "Agent ID")
		title := fs.String("title", "", "To-do item title")
		priority := fs.String("priority", "normal", "Priority: low, normal, high")
		taskID := fs.String("task", "", "Context DAG task ID")
		notes := fs.String("notes", "", "Working notes")
		_ = fs.Parse(args[1:])

		if *title == "" {
			fmt.Fprintln(os.Stderr, "Error: --title is required")
			os.Exit(1)
		}

		store, err := state.NewStore(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		tm := tasks.NewManager(store)
		item, err := tm.AddPersonalTodo(*agent, *title, *priority, *taskID, *notes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("✓ Added personal to-do for agent '%s': [%s] %s (%s)\n",
			*agent, item.ID, item.Title, item.Priority)
		return
	}

	fs := flag.NewFlagSet("todo", flag.ExitOnError)
	dir := fs.String("dir", "", "Path to gossip storage directory")
	agent := fs.String("agent", "cli-agent", "Agent ID")
	status := fs.String("status", "", "Filter by status: pending, in_progress, done")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tm := tasks.NewManager(store)
	items, err := tm.ListPersonalTodos(*agent, *status)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== Personal To-Do List: %s (%d items) ===\n", *agent, len(items))
	if len(items) == 0 {
		fmt.Println("No personal to-do items found.")
		return
	}

	for i, it := range items {
		statusMark := "[ ]"
		if it.Status == "done" {
			statusMark = "[✓]"
		} else if it.Status == "in_progress" {
			statusMark = "[→]"
		}
		contextStr := ""
		if it.ContextTaskID != "" {
			contextStr = fmt.Sprintf(" (DAG: %s)", it.ContextTaskID)
		}
		fmt.Printf("  %d. %s %s [%s]%s\n", i+1, statusMark, it.Title, strings.ToUpper(it.Priority), contextStr)
		if it.Notes != "" {
			fmt.Printf("     Notes: %s\n", it.Notes)
		}
	}
}

func runSystemTodos(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tm := tasks.NewManager(store)
	sysList, err := tm.GetSystemTodoList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(sysList.RenderMarkdown())
}

func runAssignments(customDir string) {
	store, err := state.NewStore(customDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	tm := tasks.NewManager(store)
	matrix, err := tm.GetOrchestratorAssignments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(matrix.RenderMarkdown())
}

func runResume(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	agentID := fs.String("agent", "", "Agent ID to re-identify (required)")
	role := fs.String("role", "", "Role override (optional)")
	sessionID := fs.String("session", "", "External session or conversation ID (optional)")
	dir := fs.String("dir", "", "Custom gossip directory")
	format := fs.String("format", "markdown", "Output format (markdown, json)")
	_ = fs.Parse(args)

	if *agentID == "" {
		fmt.Fprintf(os.Stderr, "Error: --agent is required\nUsage: gossip resume --agent <id> [--role <role>] [--session <session_id>]\n")
		os.Exit(1)
	}

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	existing, found, err := store.GetAgent(*agentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking agent: %v\n", err)
		os.Exit(1)
	}

	var timeAway time.Duration
	sessionCount := 1
	effectiveRole := *role
	if found {
		timeAway = time.Since(existing.LastHeartbeat)
		sessionCount = existing.SessionCount + 1
		if effectiveRole == "" {
			effectiveRole = existing.Role
		}
	} else if effectiveRole == "" {
		effectiveRole = "worker"
	}

	sess := state.AgentSession{
		ID:           *agentID,
		Role:         effectiveRole,
		Status:       "active",
		SessionID:    *sessionID,
		SessionCount: sessionCount,
	}
	if found {
		sess.Name = existing.Name
		sess.Capabilities = existing.Capabilities
		sess.CurrentTaskID = existing.CurrentTaskID
	}

	if err := store.RegisterAgent(sess); err != nil {
		fmt.Fprintf(os.Stderr, "Error reidentifying agent: %v\n", err)
		os.Exit(1)
	}

	msgMgr := messages.NewManager(store)
	taskMgr := tasks.NewManager(store)
	delibMgr := deliberation.NewManager(store)
	sopMgr := sop.NewManager(store)

	unreadMsgs, _ := msgMgr.ReadInbox(*agentID, effectiveRole, true, "", 20)
	allTasks, _ := taskMgr.ListTasks("", "", *agentID)
	var pendingTasks []tasks.Task
	for _, t := range allTasks {
		if t.Status != "done" {
			pendingTasks = append(pendingTasks, t)
		}
	}

	pTodos, _ := taskMgr.ListPersonalTodos(*agentID, "pending")
	ipTodos, _ := taskMgr.ListPersonalTodos(*agentID, "in_progress")
	activeTodos := append(pTodos, ipTodos...)

	debates, _ := delibMgr.ListDebates("in_progress")
	workflows, _ := sopMgr.ListWorkflows()
	var activeWf *sop.SOPWorkflow
	for i := range workflows {
		if workflows[i].Status == "active" {
			activeWf = &workflows[i]
			break
		}
	}

	timeAwayStr := "first session"
	if found {
		timeAwayStr = timeAway.Round(time.Second).String()
	}

	if *format == "json" {
		briefing := map[string]interface{}{
			"agent_id":        *agentID,
			"role":            effectiveRole,
			"session_count":   sessionCount,
			"time_away":       timeAwayStr,
			"unread_messages": len(unreadMsgs),
			"assigned_tasks":  len(pendingTasks),
			"personal_todos":  len(activeTodos),
			"active_debates":  len(debates),
			"status":          "active",
		}
		if activeWf != nil {
			briefing["active_sop"] = map[string]interface{}{
				"workflow_id":   activeWf.ID,
				"template_name": activeWf.Template,
				"current_stage": activeWf.Stages[activeWf.CurrentStageIndex].Name,
			}
		}
		data, _ := json.MarshalIndent(briefing, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("🔄 Agent Re-Identification Briefing: %s\n", *agentID)
	fmt.Printf("  Role:            %s\n", effectiveRole)
	fmt.Printf("  Session:         #%d (Time away: %s)\n", sessionCount, timeAwayStr)
	fmt.Printf("  Assigned Tasks:  %d in progress/pending\n", len(pendingTasks))
	for _, t := range pendingTasks {
		fmt.Printf("    - [%s] (%s) %s\n", t.ID, t.Status, t.Title)
	}
	fmt.Printf("  Unread Messages: %d in inbox\n", len(unreadMsgs))
	for _, m := range unreadMsgs {
		fmt.Printf("    - From %s (%s): %s\n", m.From, m.Role, m.Subject)
	}
	fmt.Printf("  Personal To-Dos: %d active scratchpad items\n", len(activeTodos))
	for _, td := range activeTodos {
		fmt.Printf("    - [%s] (%s) %s\n", td.ID, td.Priority, td.Title)
	}
	if len(debates) > 0 {
		fmt.Printf("  Active Debates:  %d awaiting consensus\n", len(debates))
		for _, d := range debates {
			fmt.Printf("    - [%s] (Round %d): %s\n", d.ID, len(d.Rounds), d.Title)
		}
	}
	if activeWf != nil {
		fmt.Printf("  Active SOP:      %s (Stage: %s)\n", activeWf.Template, activeWf.Stages[activeWf.CurrentStageIndex].Name)
	}
}

func runWebUI(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	port := fs.Int("port", 8080, "Web dashboard port (default 8080)")
	dir := fs.String("dir", "", "Custom gossip directory")
	_ = fs.Parse(args)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing state store: %v\n", err)
		os.Exit(1)
	}

	roleProv, _ := roles.NewService(store.Dir(), "")
	srv := web.NewServer(*port, store, roleProv)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running web server: %v\n", err)
		os.Exit(1)
	}
}

func runMoE(args []string) {
	if len(args) == 0 || args[0] == "list" {
		dir := parseDirFlag(args)
		store, err := state.NewStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		mgr := moe.NewManager(store, nil)
		panels, err := mgr.ListPanels("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(panels) == 0 {
			fmt.Println("No active Mixture of Experts (MoE) councils. Convene one with 'gossip moe convene'.")
			return
		}
		for _, p := range panels {
			fmt.Print(p.RenderMarkdown())
			fmt.Println("------------------------------------------------------------")
		}
		return
	}

	sub := args[0]
	switch sub {
	case "convene":
		fs := flag.NewFlagSet("moe convene", flag.ExitOnError)
		title := fs.String("title", "", "Panel topic / title (required)")
		query := fs.String("query", "", "Problem statement or trade-off query (required)")
		domain := fs.String("domain", "", "Domain keywords (e.g. 'concurrency, networking, crypto')")
		dir := fs.String("dir", "", "Custom gossip storage directory")
		parallel := fs.Int("parallel", 4, "Max parallel subagent workers (default: 4)")
		_ = fs.Parse(args[1:])

		if *title == "" || *query == "" {
			fmt.Fprintf(os.Stderr, "Error: --title and --query are required\n")
			os.Exit(1)
		}

		store, err := state.NewStore(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		mgr := moe.NewManager(store, nil)
		panel, err := mgr.ConvenePanel(*title, *query, *domain, "cli-user", nil, *parallel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error convening panel: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(panel.RenderMarkdown())

	case "view":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: gossip moe view <panel_id>\n")
			os.Exit(1)
		}
		panelID := args[1]
		dir := parseDirFlag(args[2:])
		store, err := state.NewStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		mgr := moe.NewManager(store, nil)
		panel, err := mgr.GetPanel(panelID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(panel.RenderMarkdown())

	default:
		fmt.Fprintf(os.Stderr, "Unknown moe subcommand '%s'. Available: list, convene, view\n", sub)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Printf("%s\n", BannerArt)
	fmt.Print(`Usage:
  gossip [command] [options]

Commands:
  mcp             Run as an MCP stdio server (default when invoked with no args)
  init            Initialize .local/gossip in current working directory
  status          Show registered agents, task counts, and knowledge graph metrics
  board           Print the live Markdown Kanban board (BOARD.md)
  report          Print the Graphify-style knowledge graph report (GRAPH_REPORT.md)
  tasks           List all tasks in table format
  debates         List multi-agent debates and voting sessions
  probes          List adversarial red-team security probes
  pagerank        Compute and display PageRank architectural centrality scores
  impact <id>     Perform blast-radius impact analysis when modifying a node/file
  clusters        Detect architectural submodules and connected clusters
  repomap         Generate PageRank-prioritized token-budgeted repo map
  index           Index codebase AST (files, symbols, ADRs) into RAG graph (alias: gsp:index)
  audit           Display executive red-team security audit report (AUDIT_REPORT.md)
  sop             List active Standard Operating Procedure workflows
  derive          Synthesize LLM-derived specialist roles & guardrails (alias: gsp:derive)
  check           Validate proposed agent action against active guardrails (alias: gsp:check)
  guardrails      Display project invariants and caution rules (alias: gsp:guardrails)
  agents          Print active AGENTS.md directives and role boundaries (alias: gsp:agents)
  poll            Poll or long-poll for incoming messages & tasks with timeout (alias: gsp:poll)
  watch           Live tail of events and mesh activities (alias: gsp:watch)
  todo            List or add personal agent scratchpad to-dos (alias: gsp:my_todos)
  system-todos    View unified cross-system backlog (alias: gsp:system_todos)
  assignments     View Orchestrator task allocation & workload matrix (alias: gsp:assignments)
  resume          Re-identify and resume agent session with instant catch-up briefing (alias: gsp:resume)
  moe             Convene and inspect Mixture of Experts discussion panels (alias: gsp:moe)
  ui              Launch local monochrome black & white web control panel (alias: web)
  export-mcp      Export MCP tool schemas (JSON) and instructions.md (alias: mcp-export)
  roles           Print complete directory of agent roles and directives
  send            Send a message to an agent, role, or broadcast to all
  version         Show Gossip version
  help            Show this help message

Options:
  --dir <path>    Specify custom gossip storage directory (default: .local/gossip)

Examples:
  gossip init
  gossip index
  gossip assignments
  gossip system-todos
  gossip todo --agent worker-1
  gossip todo add --agent worker-1 --title "Verify memory alignment" --task task-1
  gossip mcp
  gossip repomap
  gossip pagerank
  gossip report
  gossip poll --agent worker-1 --role worker --since 5m --timeout 20
  gossip watch
  gossip derive --name "My Service" --stack "Go, gRPC, PostgreSQL"
  gossip check --role worker --action write_file --path .env
  gossip agents
  gossip impact util:core
  gossip audit
  gossip send --from agent_1 --role network_engineer --to role:math_expert --msg "Check polynomial"
  gossip board
`)
}
