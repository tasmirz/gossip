package graph

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Default dependency relations indicating that source depends on or uses target.
var defaultDependentRelations = map[string]bool{
	"calls":         true,
	"indirect_call": true,
	"references":    true,
	"imports":       true,
	"imports_from":  true,
	"dynamic_import": true,
	"re_exports":    true,
	"inherits":      true,
	"extends":       true,
	"implements":    true,
	"uses":          true,
	"mixes_in":      true,
	"depends_on":    true,
	"requires":      true,
	"verifies":      true,
	"solves":        true,
}

// NodeScore captures centrality metrics for a graph node.
type NodeScore struct {
	NodeID    string  `json:"node_id"`
	Label     string  `json:"label"`
	Type      string  `json:"type"`
	PageRank  float64 `json:"pagerank"`
	InDegree  int     `json:"in_degree"`
	OutDegree int     `json:"out_degree"`
}

// AffectedNode models a component impacted by changes to a target component.
type AffectedNode struct {
	NodeID      string  `json:"node_id"`
	Label       string  `json:"label"`
	Type        string  `json:"type"`
	Depth       int     `json:"depth"`
	ViaRelation string  `json:"via_relation"`
	ViaSource   string  `json:"via_source"`
	PageRank    float64 `json:"pagerank,omitempty"`
}

// ImpactAnalysisReport encapsulates the blast-radius assessment when a component changes.
type ImpactAnalysisReport struct {
	SeedNodeID               string         `json:"seed_node_id"`
	SeedLabel                string         `json:"seed_label"`
	DirectHits               []AffectedNode `json:"direct_hits"`
	IndirectHits             []AffectedNode `json:"indirect_hits"`
	TotalAffectedCount       int            `json:"total_affected_count"`
	BlastRadiusScore         float64        `json:"blast_radius_score"` // 0.0 to 100.0 scale
	CriticalBottlenecks      []string       `json:"critical_bottlenecks"`
	RecommendedVerifications []string       `json:"recommended_verifications"`
}

// ClusterResult groups tightly coupled architectural nodes into submodules.
type ClusterResult struct {
	ClusterID   string   `json:"cluster_id"`
	ClusterName string   `json:"cluster_name"`
	Size        int      `json:"size"`
	Nodes       []string `json:"nodes"`
	HubNode     string   `json:"hub_node"`
}

// ComputePageRank calculates the structural centrality of all nodes using iterative power method.
// Damping factor is typically 0.85; maxIter is typically 100; tolerance is 1e-5.
func (m *Manager) ComputePageRank(damping float64, maxIter int, tol float64) ([]NodeScore, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	return calculatePageRank(g, damping, maxIter, tol), nil
}

func calculatePageRank(g *KnowledgeGraph, damping float64, maxIter int, tol float64) []NodeScore {
	n := len(g.Nodes)
	if n == 0 {
		return nil
	}
	if damping <= 0 || damping >= 1.0 {
		damping = 0.85
	}
	if maxIter <= 0 {
		maxIter = 100
	}
	if tol <= 0 {
		tol = 1e-5
	}

	nodeList := make([]string, 0, n)
	for id := range g.Nodes {
		nodeList = append(nodeList, id)
	}
	sort.Strings(nodeList)

	// Adjacency and degree maps
	inEdges := make(map[string][]string)  // target -> list of sources
	outDegree := make(map[string]int)     // source -> out degree
	inDegree := make(map[string]int)

	for _, e := range g.Edges {
		inEdges[e.Target] = append(inEdges[e.Target], e.Source)
		outDegree[e.Source]++
		inDegree[e.Target]++
	}

	// Initial uniform distribution
	initialScore := 1.0 / float64(n)
	pr := make(map[string]float64, n)
	for _, id := range nodeList {
		pr[id] = initialScore
	}

	// Iterative power method
	for iter := 0; iter < maxIter; iter++ {
		nextPR := make(map[string]float64, n)
		danglingSum := 0.0

		// Nodes with no outgoing edges contribute their mass evenly to all nodes
		for _, id := range nodeList {
			if outDegree[id] == 0 {
				danglingSum += pr[id]
			}
		}

		baseScore := (1.0 - damping) / float64(n)
		danglingContribution := (damping * danglingSum) / float64(n)

		maxDiff := 0.0
		for _, id := range nodeList {
			rankSum := 0.0
			for _, src := range inEdges[id] {
				outDeg := outDegree[src]
				if outDeg > 0 {
					rankSum += pr[src] / float64(outDeg)
				}
			}
			newScore := baseScore + danglingContribution + (damping * rankSum)
			diff := math.Abs(newScore - pr[id])
			if diff > maxDiff {
				maxDiff = diff
			}
			nextPR[id] = newScore
		}

		pr = nextPR
		if maxDiff < tol {
			break
		}
	}

	scores := make([]NodeScore, 0, n)
	for _, id := range nodeList {
		node := g.Nodes[id]
		scores = append(scores, NodeScore{
			NodeID:    id,
			Label:     node.Label,
			Type:      node.Type,
			PageRank:  pr[id],
			InDegree:  inDegree[id],
			OutDegree: outDegree[id],
		})
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].PageRank > scores[j].PageRank
	})

	return scores
}

// AffectedNodes computes the blast-radius of modifying a node (inspired by Graphify's affected.py).
// It traverses backward along consumer/dependency edges to find all affected systems up to maxDepth.
func (m *Manager) AffectedNodes(seedID string, maxDepth int) (*ImpactAnalysisReport, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > 5 {
		maxDepth = 5
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

	seedNode, exists := g.Nodes[seedID]
	if !exists {
		return nil, fmt.Errorf("seed node %q not found in knowledge graph", seedID)
	}

	// Calculate PageRank to score criticality of affected components
	prScores := calculatePageRank(g, 0.85, 50, 1e-4)
	prMap := make(map[string]float64)
	for _, s := range prScores {
		prMap[s.NodeID] = s.PageRank
	}

	// Incoming edges: target -> list of edges where edge.Target == target
	incoming := make(map[string][]Edge)
	// Outgoing edges for containment/definition relations
	outgoing := make(map[string][]Edge)

	for _, e := range g.Edges {
		incoming[e.Target] = append(incoming[e.Target], e)
		outgoing[e.Source] = append(outgoing[e.Source], e)
	}

	type queueItem struct {
		nodeID      string
		depth       int
		viaRelation string
		viaSource   string
	}

	visited := make(map[string]bool)
	visited[seedID] = true

	queue := []queueItem{{nodeID: seedID, depth: 0, viaRelation: "root", viaSource: seedID}}

	// Also seed with member nodes (defines, contains, method) if seed is a container/file/class
	for _, outEdge := range outgoing[seedID] {
		rel := strings.ToLower(outEdge.Relation)
		if rel == "defines" || rel == "contains" || rel == "method" {
			if !visited[outEdge.Target] {
				visited[outEdge.Target] = true
				queue = append(queue, queueItem{
					nodeID:      outEdge.Target,
					depth:       0, // treated as part of the seed boundary
					viaRelation: outEdge.Relation,
					viaSource:   seedID,
				})
			}
		}
	}

	var directHits []AffectedNode
	var indirectHits []AffectedNode

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr.depth >= maxDepth {
			continue
		}

		// Traverse incoming edges (who depends on curr.nodeID?)
		for _, inEdge := range incoming[curr.nodeID] {
			rel := strings.ToLower(inEdge.Relation)
			// Only follow dependency/call/import relations
			if !defaultDependentRelations[rel] && !strings.Contains(rel, "depend") && !strings.Contains(rel, "use") {
				continue
			}

			callerID := inEdge.Source
			if !visited[callerID] {
				visited[callerID] = true
				callerNode, hasNode := g.Nodes[callerID]
				label := callerID
				nodeType := "unknown"
				if hasNode {
					label = callerNode.Label
					nodeType = callerNode.Type
				}

				hit := AffectedNode{
					NodeID:      callerID,
					Label:       label,
					Type:        nodeType,
					Depth:       curr.depth + 1,
					ViaRelation: inEdge.Relation,
					ViaSource:   curr.nodeID,
					PageRank:    prMap[callerID],
				}

				if hit.Depth == 1 {
					directHits = append(directHits, hit)
				} else {
					indirectHits = append(indirectHits, hit)
				}

				queue = append(queue, queueItem{
					nodeID:      callerID,
					depth:       curr.depth + 1,
					viaRelation: inEdge.Relation,
					viaSource:   curr.nodeID,
				})
			}
		}
	}

	totalAffected := len(directHits) + len(indirectHits)

	// Calculate Blast Radius Score (0.0 to 100.0)
	// Base score: 5 points per direct hit, 2.5 per indirect hit + PageRank multiplier
	prWeightedSum := 0.0
	for _, h := range directHits {
		prWeightedSum += h.PageRank * 100.0
	}
	for _, h := range indirectHits {
		prWeightedSum += h.PageRank * 50.0
	}

	rawScore := (float64(len(directHits)) * 8.0) + (float64(len(indirectHits)) * 3.0) + prWeightedSum
	if rawScore > 100.0 {
		rawScore = 100.0
	}
	if rawScore < 0.0 {
		rawScore = 0.0
	}

	// Find critical bottlenecks (top PageRank among affected)
	allHits := append([]AffectedNode{}, directHits...)
	allHits = append(allHits, indirectHits...)
	sort.Slice(allHits, func(i, j int) bool {
		return allHits[i].PageRank > allHits[j].PageRank
	})

	var bottlenecks []string
	for i, h := range allHits {
		if i >= 5 {
			break
		}
		if h.PageRank > 0.05 || len(allHits) <= 5 {
			bottlenecks = append(bottlenecks, fmt.Sprintf("%s (%s, PageRank: %.3f)", h.NodeID, h.Type, h.PageRank))
		}
	}

	// Generate actionable verifications
	var verifications []string
	if len(directHits) > 0 {
		verifications = append(verifications, fmt.Sprintf("Execute unit tests and contract validation for %d direct dependent(s)", len(directHits)))
	}
	if len(indirectHits) > 0 {
		verifications = append(verifications, fmt.Sprintf("Run integration and end-to-end regression suites for %d indirect dependent(s)", len(indirectHits)))
	}
	if rawScore > 50.0 {
		verifications = append(verifications, "High blast radius detected (>50.0): mandatory Peer Review and Red-Team probe required prior to merging changes")
	}

	return &ImpactAnalysisReport{
		SeedNodeID:               seedID,
		SeedLabel:                seedNode.Label,
		DirectHits:               directHits,
		IndirectHits:             indirectHits,
		TotalAffectedCount:       totalAffected,
		BlastRadiusScore:         math.Round(rawScore*100) / 100,
		CriticalBottlenecks:      bottlenecks,
		RecommendedVerifications: verifications,
	}, nil
}

// DetectClusters groups nodes into architectural submodules based on graph connectivity (inspired by Graphify cluster.py).
func (m *Manager) DetectClusters() ([]ClusterResult, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return nil, err
	}

	if len(g.Nodes) == 0 {
		return nil, nil
	}

	// Build undirected adjacency graph
	adj := make(map[string][]string)
	for _, e := range g.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
		adj[e.Target] = append(adj[e.Target], e.Source)
	}

	// Calculate degrees to find hub nodes
	degrees := make(map[string]int)
	for _, e := range g.Edges {
		degrees[e.Source]++
		degrees[e.Target]++
	}

	// Connected components clustering
	visited := make(map[string]bool)
	var clusters []ClusterResult
	clusterCounter := 1

	// Sort node IDs for deterministic output
	nodeIDs := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	for _, startID := range nodeIDs {
		if visited[startID] {
			continue
		}

		// BFS to find connected component
		compQueue := []string{startID}
		visited[startID] = true
		var compNodes []string
		hubID := startID
		maxDeg := degrees[startID]

		for len(compQueue) > 0 {
			curr := compQueue[0]
			compQueue = compQueue[1:]
			compNodes = append(compNodes, curr)

			if degrees[curr] > maxDeg {
				maxDeg = degrees[curr]
				hubID = curr
			}

			for _, neighbor := range adj[curr] {
				if !visited[neighbor] {
					visited[neighbor] = true
					compQueue = append(compQueue, neighbor)
				}
			}
		}

		sort.Strings(compNodes)

		// Name cluster after its hub node's label or type
		clusterName := fmt.Sprintf("Cluster %d (%s)", clusterCounter, hubID)
		if hubNode, ok := g.Nodes[hubID]; ok {
			clusterName = fmt.Sprintf("Subsystem: %s", hubNode.Label)
		}

		clusters = append(clusters, ClusterResult{
			ClusterID:   fmt.Sprintf("cluster_%d", clusterCounter),
			ClusterName: clusterName,
			Size:        len(compNodes),
			Nodes:       compNodes,
			HubNode:     hubID,
		})
		clusterCounter++
	}

	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].Size > clusters[j].Size
	})

	return clusters, nil
}

// GenerateRepoMap creates a token-budgeted, PageRank-prioritized architectural map (inspired by Aider's repo-map).
func (m *Manager) GenerateRepoMap(maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 1000
	}

	unlock, err := m.store.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	g, err := m.LoadGraph()
	if err != nil {
		return "", err
	}

	scores := calculatePageRank(g, 0.85, 50, 1e-4)
	if len(scores) == 0 {
		return "# 🗺️ Architectural Repo-Map\n\n_Graph is currently empty._\n", nil
	}

	// Approximate 1 token = 4 characters
	charBudget := maxTokens * 4

	var b strings.Builder
	b.WriteString("# 🗺️ Gossip Architectural Repo-Map (PageRank-Ranked)\n\n")
	b.WriteString(fmt.Sprintf("*Generated with PageRank Centrality across %d components*\n\n", len(scores)))

	// Outgoing and incoming relation lookup
	outRelations := make(map[string][]string)
	inRelations := make(map[string][]string)
	for _, e := range g.Edges {
		outRelations[e.Source] = append(outRelations[e.Source], fmt.Sprintf("%s -> `%s`", e.Relation, e.Target))
		inRelations[e.Target] = append(inRelations[e.Target], fmt.Sprintf("`%s` -> %s", e.Source, e.Relation))
	}

	for _, s := range scores {
		node := g.Nodes[s.NodeID]
		tagsStr := ""
		if len(node.Tags) > 0 {
			tagsStr = fmt.Sprintf(" `[%s]`", strings.Join(node.Tags, ", "))
		}

		var item strings.Builder
		item.WriteString(fmt.Sprintf("### `%s` (%s)%s — Centrality: %.3f\n", s.NodeID, s.Type, tagsStr, s.PageRank))
		item.WriteString(fmt.Sprintf("**Label:** %s\n", s.Label))
		if node.Summary != "" {
			item.WriteString(fmt.Sprintf("**Summary:** %s\n", node.Summary))
		}
		if len(inRelations[s.NodeID]) > 0 {
			deps := inRelations[s.NodeID]
			if len(deps) > 4 {
				deps = append(deps[:4], fmt.Sprintf("... (+%d more)", len(deps)-4))
			}
			item.WriteString(fmt.Sprintf("- **Used By:** %s\n", strings.Join(deps, ", ")))
		}
		if len(outRelations[s.NodeID]) > 0 {
			refs := outRelations[s.NodeID]
			if len(refs) > 4 {
				refs = append(refs[:4], fmt.Sprintf("... (+%d more)", len(refs)-4))
			}
			item.WriteString(fmt.Sprintf("- **Depends On:** %s\n", strings.Join(refs, ", ")))
		}
		item.WriteString("\n")

		itemStr := item.String()
		if b.Len()+len(itemStr) > charBudget {
			b.WriteString(fmt.Sprintf("\n_... Truncated %d additional lower-centrality components to preserve token budget._\n", len(scores)-b.Len()/100))
			break
		}
		b.WriteString(itemStr)
	}

	return b.String(), nil
}
