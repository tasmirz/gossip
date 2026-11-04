package graph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IndexSummary contains metrics from an automated codebase indexing run.
type IndexSummary struct {
	RootPath         string `json:"root_path"`
	FilesScanned     int    `json:"files_scanned"`
	PackagesFound    int    `json:"packages_found"`
	SymbolsExtracted int    `json:"symbols_extracted"`
	NodesAdded       int    `json:"nodes_added"`
	EdgesAdded       int    `json:"edges_added"`
	DurationMs       int64  `json:"duration_ms"`
}

// IndexCodebase traverses rootPath, parses Go code/AST, extracts structural packages,
// files, symbols, imports, and architectural decisions, and indexes them into the RAG graph.
func (m *Manager) IndexCodebase(rootPath string) (*IndexSummary, error) {
	start := time.Now()
	if rootPath == "" {
		var err error
		rootPath, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}

	g, err := m.LoadGraph()
	if err != nil {
		g = &KnowledgeGraph{
			Version:   "1.0.0",
			Nodes:     make(map[string]Node),
			Edges:     make(map[string]Edge),
			UpdatedAt: time.Now(),
		}
	}

	summary := &IndexSummary{
		RootPath: rootPath,
	}

	packagesSeen := make(map[string]bool)
	fset := token.NewFileSet()

	// Walk directory and parse Go files
	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden, vendor, and build artifact directories
		if info.IsDir() {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "bin" || base == "node_modules" || base == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(info.Name(), ".go") || strings.HasSuffix(info.Name(), "_test.go") {
			return nil
		}

		relPath, _ := filepath.Rel(rootPath, path)
		summary.FilesScanned++

		nodeFile, pkgName, symbols, imports, parseErr := parseGoSourceFile(fset, path, relPath)
		if parseErr != nil {
			return nil
		}

		// 1. Add File Node
		if _, exists := g.Nodes[nodeFile.ID]; !exists {
			summary.NodesAdded++
		}
		g.Nodes[nodeFile.ID] = nodeFile

		// 2. Add Package Node
		pkgID := fmt.Sprintf("pkg:%s", pkgName)
		if !packagesSeen[pkgID] {
			packagesSeen[pkgID] = true
			summary.PackagesFound++
			if _, exists := g.Nodes[pkgID]; !exists {
				summary.NodesAdded++
			}
			g.Nodes[pkgID] = Node{
				ID:        pkgID,
				Type:      TypeArchitecture,
				Label:     pkgName,
				Summary:   fmt.Sprintf("Go package '%s' in directory '%s'", pkgName, filepath.Dir(relPath)),
				Tags:      []string{"package", "go"},
				CreatedBy: "codebase-indexer",
				CreatedAt: time.Now(),
			}
		}

		// Edge: File is part_of Package
		edgeFilePkg := Edge{
			ID:         fmt.Sprintf("edge:%s-part_of-%s", nodeFile.ID, pkgID),
			Source:     nodeFile.ID,
			Target:     pkgID,
			Relation:   "part_of",
			Provenance: ProvenanceExtracted,
			Summary:    fmt.Sprintf("File %s belongs to package %s", relPath, pkgName),
			CreatedBy:  "codebase-indexer",
			CreatedAt:  time.Now(),
		}
		if _, exists := g.Edges[edgeFilePkg.ID]; !exists {
			summary.EdgesAdded++
		}
		g.Edges[edgeFilePkg.ID] = edgeFilePkg

		// 3. Add Symbols
		for _, sym := range symbols {
			summary.SymbolsExtracted++
			if _, exists := g.Nodes[sym.ID]; !exists {
				summary.NodesAdded++
			}
			g.Nodes[sym.ID] = sym

			// Edge: Symbol defined_in File
			edgeSymFile := Edge{
				ID:         fmt.Sprintf("edge:%s-defined_in-%s", sym.ID, nodeFile.ID),
				Source:     sym.ID,
				Target:     nodeFile.ID,
				Relation:   "defined_in",
				Provenance: ProvenanceExtracted,
				Summary:    fmt.Sprintf("Symbol %s declared in %s", sym.Label, relPath),
				CreatedBy:  "codebase-indexer",
				CreatedAt:  time.Now(),
			}
			if _, exists := g.Edges[edgeSymFile.ID]; !exists {
				summary.EdgesAdded++
			}
			g.Edges[edgeSymFile.ID] = edgeSymFile

			// Edge: Package contains Symbol
			edgePkgSym := Edge{
				ID:         fmt.Sprintf("edge:%s-contains-%s", pkgID, sym.ID),
				Source:     pkgID,
				Target:     sym.ID,
				Relation:   "contains",
				Provenance: ProvenanceExtracted,
				Summary:    fmt.Sprintf("Package %s contains symbol %s", pkgName, sym.Label),
				CreatedBy:  "codebase-indexer",
				CreatedAt:  time.Now(),
			}
			if _, exists := g.Edges[edgePkgSym.ID]; !exists {
				summary.EdgesAdded++
			}
			g.Edges[edgePkgSym.ID] = edgePkgSym
		}

		// 4. Add Import Relationships
		for _, imp := range imports {
			if strings.Contains(imp, "github.com/tasmirz/gossip") {
				impParts := strings.Split(imp, "/")
				targetPkgName := impParts[len(impParts)-1]
				targetPkgID := fmt.Sprintf("pkg:%s", targetPkgName)

				edgeImport := Edge{
					ID:         fmt.Sprintf("edge:%s-imports-%s", nodeFile.ID, targetPkgID),
					Source:     nodeFile.ID,
					Target:     targetPkgID,
					Relation:   "imports",
					Provenance: ProvenanceExtracted,
					Summary:    fmt.Sprintf("File %s imports package %s", relPath, targetPkgName),
					CreatedBy:  "codebase-indexer",
					CreatedAt:  time.Now(),
				}
				if _, exists := g.Edges[edgeImport.ID]; !exists {
					summary.EdgesAdded++
				}
				g.Edges[edgeImport.ID] = edgeImport
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("indexing error: %w", err)
	}

	// 5. Index Architectural Decisions (ADRs) and Invariants
	m.indexArchitecturalDecisionsAndInvariants(g, summary)

	// Save indexed graph & regenerate GRAPH_REPORT.md
	if err := m.SaveGraph(g); err != nil {
		return nil, fmt.Errorf("failed to save indexed graph: %w", err)
	}

	summary.DurationMs = time.Since(start).Milliseconds()
	return summary, nil
}

func parseGoSourceFile(fset *token.FileSet, absPath, relPath string) (Node, string, []Node, []string, error) {
	nodeFile := Node{
		ID:         fmt.Sprintf("file:%s", relPath),
		Type:       TypeFile,
		Label:      filepath.Base(relPath),
		SourcePath: relPath,
		CreatedBy:  "codebase-indexer",
		CreatedAt:  time.Now(),
	}

	node, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nodeFile, "", nil, nil, err
	}

	pkgName := node.Name.Name
	nodeFile.Tags = []string{"file", "go", pkgName}

	var imports []string
	for _, imp := range node.Imports {
		pathVal := strings.Trim(imp.Path.Value, `"`)
		imports = append(imports, pathVal)
	}

	var symbols []Node
	for _, decl := range node.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if ok && (genDecl.Tok == token.TYPE || genDecl.Tok == token.VAR || genDecl.Tok == token.CONST) {
			for _, spec := range genDecl.Specs {
				typeSpec, isType := spec.(*ast.TypeSpec)
				if isType && ast.IsExported(typeSpec.Name.Name) {
					kind := "type"
					switch typeSpec.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}

					docText := ""
					if genDecl.Doc != nil {
						docText = strings.TrimSpace(genDecl.Doc.Text())
					}

					symID := fmt.Sprintf("symbol:%s.%s", pkgName, typeSpec.Name.Name)
					symbols = append(symbols, Node{
						ID:         symID,
						Type:       TypeSymbol,
						Label:      typeSpec.Name.Name,
						Summary:    fmt.Sprintf("%s %s.%s: %s", kind, pkgName, typeSpec.Name.Name, docText),
						SourcePath: relPath,
						Tags:       []string{kind, pkgName, "go"},
						CreatedBy:  "codebase-indexer",
						CreatedAt:  time.Now(),
					})
				}
			}
		}

		funcDecl, isFunc := decl.(*ast.FuncDecl)
		if isFunc && ast.IsExported(funcDecl.Name.Name) {
			docText := ""
			if funcDecl.Doc != nil {
				docText = strings.TrimSpace(funcDecl.Doc.Text())
			}

			symID := fmt.Sprintf("symbol:%s.%s", pkgName, funcDecl.Name.Name)
			symbols = append(symbols, Node{
				ID:         symID,
				Type:       TypeSymbol,
				Label:      funcDecl.Name.Name,
				Summary:    fmt.Sprintf("Function %s.%s: %s", pkgName, funcDecl.Name.Name, docText),
				SourcePath: relPath,
				Tags:       []string{"function", pkgName, "go"},
				CreatedBy:  "codebase-indexer",
				CreatedAt:  time.Now(),
			})
		}
	}

	nodeFile.Summary = fmt.Sprintf("Go source file (%d symbols, package %s)", len(symbols), pkgName)
	return nodeFile, pkgName, symbols, imports, nil
}

func (m *Manager) indexArchitecturalDecisionsAndInvariants(g *KnowledgeGraph, summary *IndexSummary) {
	decisions := []struct {
		id      string
		label   string
		summary string
		targets []string
	}{
		{
			id:      "decision:adr-001",
			label:   "ADR-001: JSON-Driven SOLID Role Taxonomy",
			summary: "Roles, system prompts, directives, and tools are decoupled from Go code and loaded dynamically from JSON.",
			targets: []string{"pkg:roles", "pkg:mcp"},
		},
		{
			id:      "decision:adr-002",
			label:   "ADR-002: Deterministic Graphify/Aider Knowledge Graph RAG",
			summary: "Deterministic knowledge graph RAG with PageRank centrality, blast-radius impact analysis, and token-budgeted repo-maps.",
			targets: []string{"pkg:graph", "pkg:mcp"},
		},
		{
			id:      "decision:adr-003",
			label:   "ADR-003: MetaGPT Standard Operating Procedures (SOP)",
			summary: "Assembly-line workflows with mandatory phase gates and versioned Markdown artifacts (PRD -> Design -> Code -> QA -> Security).",
			targets: []string{"pkg:sop", "pkg:tasks"},
		},
		{
			id:      "decision:adr-004",
			label:   "ADR-004: Multi-Agent Debate (MAD) & Borda Consensus",
			summary: "Dialectic multi-round debates with stance stability convergence tracking and Borda Count ranked-choice voting.",
			targets: []string{"pkg:deliberation", "pkg:mcp"},
		},
		{
			id:      "decision:adr-005",
			label:   "ADR-005: Zero-Vulnerability Security Architecture",
			summary: "Defense-in-depth against CWE-22 path traversal, symlink escapes, shell injection, memory DoS, and flock deadlocks.",
			targets: []string{"pkg:security", "pkg:state"},
		},
		{
			id:      "decision:adr-006",
			label:   "ADR-006: Continuous Event Polling & Long-Polling Engine",
			summary: "Unified time-bounded event polling engine with long-polling timeouts, auto-heartbeat, and multi-subsystem aggregation.",
			targets: []string{"pkg:events", "pkg:mcp"},
		},
		{
			id:      "decision:adr-007",
			label:   "ADR-007: LLM-Derived Roles, Guardrails & AGENTS.md",
			summary: "Dynamic synthesis of project-specific roles, invariants, and caution rules persisted in guardrails.json and AGENTS.md.",
			targets: []string{"pkg:guardrails", "pkg:roles"},
		},
	}

	for _, d := range decisions {
		if _, exists := g.Nodes[d.id]; !exists {
			summary.NodesAdded++
		}
		g.Nodes[d.id] = Node{
			ID:        d.id,
			Type:      TypeDecision,
			Label:     d.label,
			Summary:   d.summary,
			Tags:      []string{"adr", "architecture", "decision"},
			CreatedBy: "system-architect",
			CreatedAt: time.Now(),
		}

		for _, target := range d.targets {
			edgeID := fmt.Sprintf("edge:%s-governs-%s", d.id, target)
			if _, exists := g.Edges[edgeID]; !exists {
				summary.EdgesAdded++
			}
			g.Edges[edgeID] = Edge{
				ID:         edgeID,
				Source:     d.id,
				Target:     target,
				Relation:   "governs",
				Provenance: ProvenanceInferred,
				Summary:    fmt.Sprintf("%s governs %s", d.label, target),
				CreatedBy:  "system-architect",
				CreatedAt:  time.Now(),
			}
		}
	}

	invariants := []struct {
		id      string
		label   string
		summary string
		target  string
	}{
		{
			id:      "invariant:cwe22-path-traversal-defense",
			label:   "CWE-22 Path Traversal & Symlink Escape Defense",
			summary: "Every file path access must strictly be contained within project root; eval symlinks and reject null bytes.",
			target:  "pkg:security",
		},
		{
			id:      "invariant:deadlock-free-posix-locking",
			label:   "Deadlock-Free POSIX File Locking with Exponential Backoff",
			summary: "All cross-process locks must use non-blocking try-lock with max 10s deadline; no infinite blocking.",
			target:  "pkg:state",
		},
		{
			id:      "invariant:payload-bound-512k",
			label:   "DoS Payload Size Ceiling (512 KB)",
			summary: "All messages, task specs, and payloads are bounded to 512KB to eliminate memory exhaustion vulnerabilities.",
			target:  "pkg:security",
		},
		{
			id:      "invariant:task-dag-acyclicity",
			label:   "Task Dependency DAG Acyclicity",
			summary: "Task blocker graph must be strictly acyclic; cycles are rejected with clear cycle path error.",
			target:  "pkg:tasks",
		},
	}

	for _, inv := range invariants {
		if _, exists := g.Nodes[inv.id]; !exists {
			summary.NodesAdded++
		}
		g.Nodes[inv.id] = Node{
			ID:        inv.id,
			Type:      TypeConstraint,
			Label:     inv.label,
			Summary:   inv.summary,
			Tags:      []string{"invariant", "security", "constraint"},
			CreatedBy: "security-specialist",
			CreatedAt: time.Now(),
		}

		edgeID := fmt.Sprintf("edge:%s-constrains-%s", inv.id, inv.target)
		if _, exists := g.Edges[edgeID]; !exists {
			summary.EdgesAdded++
		}
		g.Edges[edgeID] = Edge{
			ID:         edgeID,
			Source:     inv.id,
			Target:     inv.target,
			Relation:   "constrains",
			Provenance: ProvenanceInferred,
			Summary:    fmt.Sprintf("%s enforces safety constraints on %s", inv.label, inv.target),
			CreatedBy:  "security-specialist",
			CreatedAt:  time.Now(),
		}
	}
}
