package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/mcp"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
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

	case "audit":
		dir := parseDirFlag(os.Args[2:])
		runAudit(dir)

	case "sop":
		dir := parseDirFlag(os.Args[2:])
		runSOP(dir)

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
  audit           Display executive red-team security audit report (AUDIT_REPORT.md)
  sop             List active Standard Operating Procedure workflows
  roles           Print complete directory of agent roles and directives
  send            Send a message to an agent, role, or broadcast to all
  version         Show Gossip version
  help            Show this help message

Options:
  --dir <path>    Specify custom gossip storage directory (default: .local/gossip)

Examples:
  gossip init
  gossip mcp
  gossip impact util:core
  gossip pagerank
  gossip repomap
  gossip audit
  gossip send --from agent_1 --role network_engineer --to role:math_expert --msg "Check polynomial"
  gossip board
`)
}
