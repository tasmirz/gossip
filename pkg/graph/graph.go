package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

// Provenance constants
const (
	ProvenanceExtracted = "EXTRACTED" // Discovered structurally from source code/files
	ProvenanceInferred  = "INFERRED"  // Deduced by specialist agent reasoning
)

// Node types
const (
	TypeConcept      = "concept"
	TypeFile         = "file"
	TypeSymbol       = "symbol"
	TypeDecision     = "decision"
	TypeConstraint   = "constraint"
	TypeProof        = "proof"
	TypeArchitecture = "architecture"
)

// Node represents an entity in the shared knowledge graph.
type Node struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"` // concept, file, symbol, decision, constraint, proof
	Label      string            `json:"label"`
	Summary    string            `json:"summary"`
	Tags       []string          `json:"tags,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	SourcePath string            `json:"source_path,omitempty"`
	CreatedBy  string            `json:"created_by"`
	CreatedAt  time.Time         `json:"created_at"`
}

// Edge represents a directed relationship between two entities.
type Edge struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Target     string    `json:"target"`
	Relation   string    `json:"relation"`   // depends_on, implements, verifies, calls, conflicts_with, solves
	Provenance string    `json:"provenance"` // EXTRACTED or INFERRED
	Summary    string    `json:"summary,omitempty"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// KnowledgeGraph holds the complete in-memory graph.
type KnowledgeGraph struct {
	Version   string          `json:"version"`
	Nodes     map[string]Node `json:"nodes"`
	Edges     map[string]Edge `json:"edges"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// NeighborhoodResult represents a subgraph around a target node.
type NeighborhoodResult struct {
	CenterNode Node   `json:"center_node"`
	Depth      int    `json:"depth"`
	Nodes      []Node `json:"nodes"`
	Edges      []Edge `json:"edges"`
}

// QueryResult represents a search hit with relevance scoring.
type QueryResult struct {
	Node  Node    `json:"node"`
	Score float64 `json:"score"`
}

// Manager coordinates knowledge graph persistence, querying, and reporting.
type Manager struct {
	store *state.Store
}

// NewManager creates a graph manager backed by state.Store.
func NewManager(store *state.Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) graphFilePath() string {
	return filepath.Join(m.store.Dir(), "graph.json")
}

func (m *Manager) reportFilePath() string {
	return filepath.Join(m.store.Dir(), "GRAPH_REPORT.md")
}

// LoadGraph loads graph.json or returns an empty graph.
func (m *Manager) LoadGraph() (*KnowledgeGraph, error) {
	data, err := os.ReadFile(m.graphFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &KnowledgeGraph{
				Version:   "1.0.0",
				Nodes:     make(map[string]Node),
				Edges:     make(map[string]Edge),
				UpdatedAt: time.Now(),
			}, nil
		}
		return nil, err
	}

	var g KnowledgeGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("corrupted graph.json: %w", err)
	}
	if g.Nodes == nil {
		g.Nodes = make(map[string]Node)
	}
	if g.Edges == nil {
		g.Edges = make(map[string]Edge)
	}
	return &g, nil
}

// SaveGraph saves the graph and regenerates GRAPH_REPORT.md.
func (m *Manager) SaveGraph(g *KnowledgeGraph) error {
	g.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}

	if err := m.store.AtomicWriteFile("graph.json", data); err != nil {
		return err
	}

	// Regenerate GRAPH_REPORT.md
	report := m.generateReport(g)
	_ = m.store.AtomicWriteFile("GRAPH_REPORT.md", []byte(report))

	return nil
}

// AddNode inserts or updates an entity in the graph.
func (m *Manager) AddNode(node Node) (*Node, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	if node.ID == "" {
		node.ID = fmt.Sprintf("%s:%s", node.Type, strings.ToLower(strings.ReplaceAll(node.Label, " ", "_")))
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = time.Now()
	}

	g.Nodes[node.ID] = node

	if err := m.SaveGraph(g); err != nil {
		return nil, err
	}
	return &node, nil
}

// AddEdge inserts or updates a directed relationship between two nodes.
func (m *Manager) AddEdge(edge Edge) (*Edge, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	// Verify both endpoints exist (or auto-create placeholder concepts)
	if _, exists := g.Nodes[edge.Source]; !exists {
		g.Nodes[edge.Source] = Node{
			ID:        edge.Source,
			Type:      TypeConcept,
			Label:     edge.Source,
			Summary:   "Inferred node reference",
			CreatedBy: edge.CreatedBy,
			CreatedAt: time.Now(),
		}
	}
	if _, exists := g.Nodes[edge.Target]; !exists {
		g.Nodes[edge.Target] = Node{
			ID:        edge.Target,
			Type:      TypeConcept,
			Label:     edge.Target,
			Summary:   "Inferred node reference",
			CreatedBy: edge.CreatedBy,
			CreatedAt: time.Now(),
		}
	}

	if edge.ID == "" {
		edge.ID = fmt.Sprintf("%s--%s--%s", edge.Source, edge.Relation, edge.Target)
	}
	if edge.Provenance == "" {
		edge.Provenance = ProvenanceInferred
	}
	if edge.CreatedAt.IsZero() {
		edge.CreatedAt = time.Now()
	}

	g.Edges[edge.ID] = edge

	if err := m.SaveGraph(g); err != nil {
		return nil, err
	}
	return &edge, nil
}

// GetNode retrieves a node by ID.
func (m *Manager) GetNode(nodeID string) (*Node, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	node, exists := g.Nodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found in knowledge graph", nodeID)
	}
	return &node, nil
}

// Query performs token matching and relevance ranking across the graph.
func (m *Manager) Query(queryStr, typeFilter, tagFilter string, limit int) ([]QueryResult, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	tokens := strings.Fields(strings.ToLower(queryStr))
	var results []QueryResult

	for _, node := range g.Nodes {
		if typeFilter != "" && !strings.EqualFold(node.Type, typeFilter) {
			continue
		}

		if tagFilter != "" {
			matchedTag := false
			for _, t := range node.Tags {
				if strings.EqualFold(t, tagFilter) {
					matchedTag = true
					break
				}
			}
			if !matchedTag {
				continue
			}
		}

		// Calculate score
		score := 0.0
		nodeText := strings.ToLower(fmt.Sprintf("%s %s %s %s", node.ID, node.Label, node.Summary, strings.Join(node.Tags, " ")))

		if len(tokens) == 0 {
			score = 1.0
		} else {
			for _, tok := range tokens {
				if strings.Contains(strings.ToLower(node.Label), tok) {
					score += 10.0
				}
				if strings.Contains(strings.ToLower(node.ID), tok) {
					score += 5.0
				}
				if strings.Contains(nodeText, tok) {
					score += 2.0
				}
			}
		}

		if score > 0 {
			results = append(results, QueryResult{Node: node, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// GetNeighborhood extracts a subgraph centered on nodeID within the given radius (depth).
func (m *Manager) GetNeighborhood(nodeID string, depth int) (*NeighborhoodResult, error) {
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3 // prevent huge memory explosion
	}

	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	center, exists := g.Nodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	// Adjacency map (undirected for neighborhood discovery)
	adj := make(map[string][]string)
	for _, edge := range g.Edges {
		adj[edge.Source] = append(adj[edge.Source], edge.Target)
		adj[edge.Target] = append(adj[edge.Target], edge.Source)
	}

	visited := make(map[string]bool)
	queue := []string{nodeID}
	visited[nodeID] = true

	currentDepth := 0
	for len(queue) > 0 && currentDepth < depth {
		levelSize := len(queue)
		for i := 0; i < levelSize; i++ {
			curr := queue[0]
			queue = queue[1:]
			for _, neighbor := range adj[curr] {
				if !visited[neighbor] {
					visited[neighbor] = true
					queue = append(queue, neighbor)
				}
			}
		}
		currentDepth++
	}

	var nodes []Node
	for id := range visited {
		if n, ok := g.Nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}

	var edges []Edge
	for _, edge := range g.Edges {
		if visited[edge.Source] && visited[edge.Target] {
			edges = append(edges, edge)
		}
	}

	return &NeighborhoodResult{
		CenterNode: center,
		Depth:      depth,
		Nodes:      nodes,
		Edges:      edges,
	}, nil
}

// FindPath computes the shortest relationship path between two nodes using BFS.
func (m *Manager) FindPath(sourceID, targetID string) ([]string, []Edge, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, nil, err
	}

	if _, ok := g.Nodes[sourceID]; !ok {
		return nil, nil, fmt.Errorf("source node %s not found", sourceID)
	}
	if _, ok := g.Nodes[targetID]; !ok {
		return nil, nil, fmt.Errorf("target node %s not found", targetID)
	}

	if sourceID == targetID {
		return []string{sourceID}, nil, nil
	}

	// BFS queue
	type Step struct {
		Current  string
		Path     []string
		EdgePath []Edge
	}

	visited := make(map[string]bool)
	queue := []Step{{Current: sourceID, Path: []string{sourceID}, EdgePath: []Edge{}}}
	visited[sourceID] = true

	// Build outgoing and incoming edge index
	edgesFrom := make(map[string][]Edge)
	for _, e := range g.Edges {
		edgesFrom[e.Source] = append(edgesFrom[e.Source], e)
		// Bidirectional search
		reverseEdge := e
		reverseEdge.Source = e.Target
		reverseEdge.Target = e.Source
		reverseEdge.Relation = fmt.Sprintf("inverse_%s", e.Relation)
		edgesFrom[e.Target] = append(edgesFrom[e.Target], reverseEdge)
	}

	for len(queue) > 0 {
		step := queue[0]
		queue = queue[1:]

		if step.Current == targetID {
			return step.Path, step.EdgePath, nil
		}

		for _, edge := range edgesFrom[step.Current] {
			next := edge.Target
			if !visited[next] {
				visited[next] = true
				newPath := append([]string{}, step.Path...)
				newPath = append(newPath, next)
				newEdges := append([]Edge{}, step.EdgePath...)
				newEdges = append(newEdges, edge)
				queue = append(queue, Step{
					Current:  next,
					Path:     newPath,
					EdgePath: newEdges,
				})
			}
		}
	}

	return nil, nil, fmt.Errorf("no path found between %s and %s", sourceID, targetID)
}

// GenerateReport creates a Graphify-style GRAPH_REPORT.md.
func (m *Manager) GenerateReport() (string, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return "", err
	}

	report := m.generateReport(g)
	return report, nil
}

func (m *Manager) generateReport(g *KnowledgeGraph) string {
	var b strings.Builder
	b.WriteString("# 🧠 Gossip Shared Knowledge Graph Report\n\n")
	b.WriteString(fmt.Sprintf("*Generated at: %s | Nodes: %d | Edges: %d*\n\n",
		time.Now().Format(time.RFC1123), len(g.Nodes), len(g.Edges)))

	// Provenance breakdown
	extractedCount := 0
	inferredCount := 0
	for _, e := range g.Edges {
		if e.Provenance == ProvenanceExtracted {
			extractedCount++
		} else {
			inferredCount++
		}
	}
	b.WriteString("### 📊 Relational Provenance Breakdown\n")
	b.WriteString(fmt.Sprintf("- **EXTRACTED (Deterministic Code/File Links):** %d\n", extractedCount))
	b.WriteString(fmt.Sprintf("- **INFERRED (Specialist Reasoning & Invariants):** %d\n\n", inferredCount))

	// Node Degree Analysis (Key Hubs)
	type Hub struct {
		ID     string
		Label  string
		Type   string
		Degree int
	}
	degrees := make(map[string]int)
	for _, e := range g.Edges {
		degrees[e.Source]++
		degrees[e.Target]++
	}

	var hubs []Hub
	for id, deg := range degrees {
		if node, ok := g.Nodes[id]; ok {
			hubs = append(hubs, Hub{ID: id, Label: node.Label, Type: node.Type, Degree: deg})
		}
	}
	sort.Slice(hubs, func(i, j int) bool {
		return hubs[i].Degree > hubs[j].Degree
	})

	b.WriteString("### 🏛️ Core Architectural Hubs (Highest Degree)\n\n")
	if len(hubs) == 0 {
		b.WriteString("_No connected nodes yet._\n\n")
	} else {
		b.WriteString("| Node ID | Type | Label | Connected Edges |\n")
		b.WriteString("| :--- | :--- | :--- | :--- |\n")
		topHubs := hubs
		if len(topHubs) > 10 {
			topHubs = topHubs[:10]
		}
		for _, h := range topHubs {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %d |\n", h.ID, h.Type, h.Label, h.Degree))
		}
		b.WriteString("\n")
	}

	// Architectural Decisions & Formal Proofs
	b.WriteString("### 📜 Architectural Decisions & Formal Proofs\n\n")
	foundSpecial := false
	for _, n := range g.Nodes {
		if n.Type == TypeDecision || n.Type == TypeProof || n.Type == TypeConstraint {
			foundSpecial = true
			b.WriteString(fmt.Sprintf("#### `[%s]` %s (%s)\n", strings.ToUpper(n.Type), n.Label, n.ID))
			b.WriteString(fmt.Sprintf("%s\n\n", n.Summary))
		}
	}
	if !foundSpecial {
		b.WriteString("_No formal decisions or proofs logged yet._\n\n")
	}

	// Recent Nodes
	b.WriteString("### 📌 Node Directory\n\n")
	for _, n := range g.Nodes {
		tags := ""
		if len(n.Tags) > 0 {
			tags = fmt.Sprintf(" `[%s]`", strings.Join(n.Tags, ", "))
		}
		b.WriteString(fmt.Sprintf("- **`%s`** (%s)%s: %s\n", n.ID, n.Type, tags, n.Summary))
	}

	return b.String()
}
