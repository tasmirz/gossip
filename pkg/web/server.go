package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tasmirz/gossip/pkg/guardrails"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/moe"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/security"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

// ProjectInfo describes a discovered or registered project workspace.
type ProjectInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	StorageDir  string `json:"storage_dir"`
	Initialized bool   `json:"initialized"`
	AgentCount  int    `json:"agent_count"`
	TaskCount   int    `json:"task_count"`
	IsActive    bool   `json:"is_active"`
}

// Server encapsulates the Gossip Multi-Project Web Dashboard.
type Server struct {
	port          int
	defaultDir    string
	activePath    string
	knownProjects map[string]bool
	mu            sync.RWMutex
	roleProv      roles.Provider
}

// NewServer initializes the Multi-Project Web Dashboard server.
func NewServer(port int, initialStore *state.Store, roleProv roles.Provider) *Server {
	if roleProv == nil {
		roleProv = roles.GetDefaultService()
	}
	defaultDir := ".local/gossip"
	activePath, _ := os.Getwd()
	if initialStore != nil {
		defaultDir = initialStore.Dir()
		cleanDir := filepath.Clean(defaultDir)
		localSuffix := filepath.Join(".local", "gossip")
		if strings.HasSuffix(cleanDir, localSuffix) {
			activePath = filepath.Dir(filepath.Dir(cleanDir))
		} else {
			// Direct custom or temporary directory
			activePath = cleanDir
		}
	}

	s := &Server{
		port:          port,
		defaultDir:    defaultDir,
		activePath:    activePath,
		knownProjects: make(map[string]bool),
		roleProv:      roleProv,
	}

	s.knownProjects[activePath] = true
	s.discoverLocalProjects()
	return s
}

func (s *Server) discoverLocalProjects() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Check parent folder of activePath (e.g. /home/user/Projects)
	parent := filepath.Dir(s.activePath)
	entries, err := os.ReadDir(parent)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				p := filepath.Join(parent, e.Name())
				// If it has .local/gossip or .git or is a sibling folder
				if _, err := os.Stat(filepath.Join(p, ".local", "gossip")); err == nil {
					s.knownProjects[p] = true
				} else if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
					s.knownProjects[p] = true
				}
			}
		}
	}
}

// Handler returns the HTTP handler with all web routes configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.handleIndex)

	// Projects Management APIs
	mux.HandleFunc("/api/projects", s.handleAPIProjects)
	mux.HandleFunc("/api/projects/select", s.handleAPIProjectSelect)
	mux.HandleFunc("/api/projects/add", s.handleAPIProjectAdd)
	mux.HandleFunc("/api/projects/init", s.handleAPIProjectInit)

	// Scoped Subsystem REST APIs
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/api/tasks", s.handleAPITasks)
	mux.HandleFunc("/api/tasks/update", s.handleAPITaskUpdate)
	mux.HandleFunc("/api/tasks/modify", s.handleAPITaskModify)
	mux.HandleFunc("/api/artifacts", s.handleAPIArtifacts)
	mux.HandleFunc("/api/artifacts/content", s.handleAPIArtifactContent)
	mux.HandleFunc("/api/artifacts/save", s.handleAPIArtifactSave)
	mux.HandleFunc("/api/artifacts/create", s.handleAPIArtifactCreate)
	mux.HandleFunc("/api/agents", s.handleAPIAgents)
	mux.HandleFunc("/api/messages", s.handleAPIMessages)
	mux.HandleFunc("/api/broadcast", s.handleAPIBroadcast)
	mux.HandleFunc("/api/moe", s.handleAPIMoE)
	mux.HandleFunc("/api/moe/convene", s.handleAPIMoEConvene)
	mux.HandleFunc("/api/moe/argument", s.handleAPIMoEArgument)
	mux.HandleFunc("/api/moe/synthesize", s.handleAPIMoESynthesize)
	mux.HandleFunc("/api/moe/dispatch", s.handleAPIMoEDispatch)
	mux.HandleFunc("/api/settings", s.handleAPISettings)

	return mux
}

// Start runs the HTTP web server.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("\n+--------------------------------------------------------+\n")
	fmt.Printf("|  GOSSIP MULTI-PROJECT CONTROL PANEL                    |\n")
	fmt.Printf("|  URL: http://localhost:%d                            |\n", s.port)
	fmt.Printf("|  STYLE: STRICT 2-COLOR MONOCHROME (LIGHT & DARK)       |\n")
	fmt.Printf("|  TYPOGRAPHY: MONOSPACE // 0PX BORDER RADIUS            |\n")
	fmt.Printf("+--------------------------------------------------------+\n\n")
	return http.ListenAndServe(addr, s.Handler())
}

// resolveProjectStore returns the state.Store for the active or requested project.
func (s *Server) resolveProjectStore(requestedPath string) (*state.Store, string, error) {
	s.mu.RLock()
	projectPath := s.activePath
	if requestedPath != "" {
		projectPath = requestedPath
	}
	defaultDir := s.defaultDir
	s.mu.RUnlock()

	var storageDir string
	if (requestedPath == "" || requestedPath == projectPath) && projectPath == defaultDir {
		storageDir = defaultDir
	} else if _, err := os.Stat(filepath.Join(projectPath, "state.json")); err == nil {
		storageDir = projectPath
	} else if _, err := os.Stat(filepath.Join(projectPath, ".local", "gossip")); err == nil {
		storageDir = filepath.Join(projectPath, ".local", "gossip")
	} else {
		storageDir = filepath.Join(projectPath, ".local", "gossip")
	}

	store, err := state.NewStore(storageDir)
	return store, projectPath, err
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func (s *Server) handleAPIProjects(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var list []ProjectInfo
	for p := range s.knownProjects {
		storageDir := filepath.Join(p, ".local", "gossip")
		if p == s.defaultDir {
			storageDir = s.defaultDir
		} else if _, err := os.Stat(filepath.Join(p, "state.json")); err == nil {
			storageDir = p
		}
		initialized := false
		agentCount := 0
		taskCount := 0

		if _, err := os.Stat(storageDir); err == nil {
			initialized = true
			if st, err := state.NewStore(storageDir); err == nil {
				if agents, err := st.ListAgents(); err == nil {
					agentCount = len(agents)
				}
				tm := tasks.NewManager(st)
				if allTasks, err := tm.ListTasks("", "", ""); err == nil {
					taskCount = len(allTasks)
				}
			}
		}

		name := filepath.Base(p)
		if name == "." || name == "/" {
			name = "root"
		}

		list = append(list, ProjectInfo{
			Name:        name,
			Path:        p,
			StorageDir:  storageDir,
			Initialized: initialized,
			AgentCount:  agentCount,
			TaskCount:   taskCount,
			IsActive:    p == s.activePath,
		})
	}

	jsonResponse(w, list)
}

func (s *Server) handleAPIProjectSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", 400)
		return
	}

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	s.mu.Lock()
	s.activePath = absPath
	s.knownProjects[absPath] = true
	s.mu.Unlock()

	jsonResponse(w, map[string]interface{}{
		"status":      "selected",
		"active_path": absPath,
		"name":        filepath.Base(absPath),
	})
}

func (s *Server) handleAPIProjectAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
		http.Error(w, fmt.Sprintf("directory '%s' does not exist", absPath), 400)
		return
	}

	s.mu.Lock()
	s.knownProjects[absPath] = true
	s.activePath = absPath
	s.mu.Unlock()

	jsonResponse(w, map[string]interface{}{
		"status": "added",
		"path":   absPath,
	})
}

func (s *Server) handleAPIProjectInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	targetPath := req.Path
	if targetPath == "" {
		s.mu.RLock()
		targetPath = s.activePath
		s.mu.RUnlock()
	}
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	storageDir := filepath.Join(absPath, ".local", "gossip")
	store, err := state.NewStore(storageDir)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	projName := req.Name
	if projName == "" {
		projName = filepath.Base(absPath)
	}
	st, _ := store.LoadProjectState()
	st.ProjectName = projName
	_ = store.SaveProjectState(st)

	s.mu.Lock()
	s.knownProjects[absPath] = true
	s.activePath = absPath
	s.mu.Unlock()

	jsonResponse(w, map[string]interface{}{
		"status":       "initialized",
		"project_path": absPath,
		"project_name": projName,
	})
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	store, projPath, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	st, err := store.LoadProjectState()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	agents, _ := store.ListAgents()
	tm := tasks.NewManager(store)
	allTasks, _ := tm.ListTasks("", "", "")
	moeMgr := moe.NewManager(store, s.roleProv)
	panels, _ := moeMgr.ListPanels("")

	resp := map[string]interface{}{
		"version":      "1.0.0",
		"project_name": st.ProjectName,
		"project_path": projPath,
		"agent_count":  len(agents),
		"task_count":   len(allTasks),
		"moe_panels":   len(panels),
		"timestamp":    time.Now().Format(time.RFC3339),
	}
	jsonResponse(w, resp)
}

func (s *Server) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tm := tasks.NewManager(store)

	if r.Method == http.MethodGet {
		tasksList, err := tm.ListTasks("", "", "")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, tasksList)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Role        string   `json:"role"`
			Priority    string   `json:"priority"`
			BlockedBy   []string `json:"blocked_by"`
			Criteria    []string `json:"criteria"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		task, err := tm.CreateTask(req.Title, req.Description, req.Role, req.Priority, req.BlockedBy, req.Criteria)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, task)
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func (s *Server) handleAPITaskUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tm := tasks.NewManager(store)

	var req struct {
		TaskID       string `json:"task_id"`
		Status       string `json:"status"`
		AssignedTo   string `json:"assigned_to"`
		ProgressNote string `json:"progress_note"`
		Artifact     string `json:"artifact"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	task, err := tm.UpdateTask(req.TaskID, req.Status, req.AssignedTo, req.ProgressNote, req.Artifact)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, task)
}

func (s *Server) handleAPITaskModify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tm := tasks.NewManager(store)

	var req struct {
		TaskID       string    `json:"task_id"`
		Title        *string   `json:"title"`
		Description  *string   `json:"description"`
		Role         *string   `json:"role"`
		Priority     *string   `json:"priority"`
		Status       *string   `json:"status"`
		AssignedTo   *string   `json:"assigned_to"`
		BlockedBy    *[]string `json:"blocked_by"`
		Criteria     *[]string `json:"criteria"`
		ProgressNote string    `json:"progress_note"`
		Artifact     string    `json:"artifact"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.TaskID == "" {
		http.Error(w, "task_id is required", 400)
		return
	}

	task, err := tm.ModifyTask(req.TaskID, tasks.TaskModifications{
		Title:              req.Title,
		Description:        req.Description,
		Role:               req.Role,
		Priority:           req.Priority,
		Status:             req.Status,
		AssignedTo:         req.AssignedTo,
		BlockedBy:          req.BlockedBy,
		AcceptanceCriteria: req.Criteria,
		ProgressNote:       req.ProgressNote,
		Artifact:           req.Artifact,
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, task)
}

// ArtifactEntry describes a plan or artifact file available for viewing and editing.
type ArtifactEntry struct {
	Name       string    `json:"name"`
	RelPath    string    `json:"rel_path"`
	Category   string    `json:"category"` // "board", "directives", "rules", "report", "plan", "artifact"
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
}

func (s *Server) handleAPIArtifacts(w http.ResponseWriter, r *http.Request) {
	store, projPath, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var results []ArtifactEntry
	seen := make(map[string]bool)

	addFile := func(absPath, relPath, category string) {
		if seen[relPath] {
			return
		}
		fi, err := os.Stat(absPath)
		if err != nil || fi.IsDir() {
			return
		}
		seen[relPath] = true
		results = append(results, ArtifactEntry{
			Name:       filepath.Base(absPath),
			RelPath:    relPath,
			Category:   category,
			SizeBytes:  fi.Size(),
			ModifiedAt: fi.ModTime(),
		})
	}

	// 1. Standard project files
	standards := []struct {
		file string
		cat  string
	}{
		{"BOARD.md", "board"},
		{"AGENTS.md", "directives"},
		{"RULES.md", "rules"},
		{"GRAPH_REPORT.md", "report"},
	}
	for _, std := range standards {
		storeP := filepath.Join(store.Dir(), std.file)
		if _, err := os.Stat(storeP); err == nil {
			addFile(storeP, std.file, std.cat)
		} else {
			projP := filepath.Join(projPath, std.file)
			if _, err := os.Stat(projP); err == nil {
				addFile(projP, std.file, std.cat)
			}
		}
	}

	// 2. Scan project root for markdown and plan files
	if entries, err := os.ReadDir(projPath); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				addFile(filepath.Join(projPath, e.Name()), e.Name(), "plan")
			}
		}
	}

	// 3. Scan store artifacts folder (.local/gossip/artifacts)
	artDir := filepath.Join(store.Dir(), "artifacts")
	if entries, err := os.ReadDir(artDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				rel := filepath.Join(".local", "gossip", "artifacts", e.Name())
				addFile(filepath.Join(artDir, e.Name()), rel, "artifact")
			}
		}
	}

	jsonResponse(w, results)
}

func (s *Server) handleAPIArtifactContent(w http.ResponseWriter, r *http.Request) {
	store, projPath, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	fileName := r.URL.Query().Get("file")
	if fileName == "" {
		http.Error(w, "file is required", 400)
		return
	}

	var targetPath string
	if strings.HasPrefix(fileName, filepath.Join(".local", "gossip")) || strings.HasPrefix(fileName, ".local/gossip") {
		sub := strings.TrimPrefix(fileName, ".local/gossip/")
		sub = strings.TrimPrefix(sub, filepath.Join(".local", "gossip")+string(filepath.Separator))
		sp, err := security.ValidateSafePath(store.Dir(), sub)
		if err == nil {
			targetPath = sp
		}
	}
	if targetPath == "" {
		sp, err := security.ValidateSafePath(store.Dir(), fileName)
		if err == nil {
			if _, err := os.Stat(sp); err == nil {
				targetPath = sp
			}
		}
	}
	if targetPath == "" {
		sp, err := security.ValidateSafePath(projPath, fileName)
		if err != nil {
			http.Error(w, "access denied: invalid path", 403)
			return
		}
		targetPath = sp
	}

	fi, err := os.Stat(targetPath)
	if err != nil {
		http.Error(w, "file not found", 404)
		return
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"file":        fileName,
		"path":        targetPath,
		"content":     string(data),
		"size_bytes":  fi.Size(),
		"modified_at": fi.ModTime().Format(time.RFC3339),
	})
}

func (s *Server) handleAPIArtifactSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, projPath, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var req struct {
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.File == "" {
		http.Error(w, "file is required", 400)
		return
	}

	var targetPath string
	if strings.HasPrefix(req.File, filepath.Join(".local", "gossip")) || strings.HasPrefix(req.File, ".local/gossip") {
		sub := strings.TrimPrefix(req.File, ".local/gossip/")
		sub = strings.TrimPrefix(sub, filepath.Join(".local", "gossip")+string(filepath.Separator))
		sp, err := security.ValidateSafePath(store.Dir(), sub)
		if err == nil {
			targetPath = sp
		}
	}
	if targetPath == "" {
		sp, err := security.ValidateSafePath(store.Dir(), req.File)
		if err == nil {
			if _, err := os.Stat(sp); err == nil {
				targetPath = sp
			}
		}
	}
	if targetPath == "" {
		sp, err := security.ValidateSafePath(projPath, req.File)
		if err != nil {
			http.Error(w, "access denied: invalid path", 403)
			return
		}
		targetPath = sp
	}

	_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
	if err := os.WriteFile(targetPath, []byte(req.Content), 0644); err != nil {
		http.Error(w, fmt.Sprintf("failed to save file: %v", err), 500)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"status":     "saved",
		"file":       req.File,
		"size_bytes": len(req.Content),
		"saved_at":   time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleAPIArtifactCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, projPath, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var req struct {
		Name     string `json:"name"`
		Content  string `json:"content"`
		Location string `json:"location"` // "artifacts" or "root"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name = name + ".md"
	}

	var targetDir string
	var relPath string
	if req.Location == "root" {
		targetDir = projPath
		relPath = name
	} else {
		targetDir = filepath.Join(store.Dir(), "artifacts")
		relPath = filepath.Join(".local", "gossip", "artifacts", name)
	}

	safePath, err := security.ValidateSafePath(targetDir, name)
	if err != nil {
		http.Error(w, "invalid file name", 400)
		return
	}

	_ = os.MkdirAll(filepath.Dir(safePath), 0755)
	if err := os.WriteFile(safePath, []byte(req.Content), 0644); err != nil {
		http.Error(w, fmt.Sprintf("failed to create file: %v", err), 500)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"status":     "created",
		"file":       relPath,
		"name":       name,
		"size_bytes": len(req.Content),
		"created_at": time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleAPIAgents(w http.ResponseWriter, r *http.Request) {
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if r.Method == http.MethodGet {
		agents, err := store.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, agents)
		return
	}
	if r.Method == http.MethodPost {
		var sess state.AgentSession
		if err := json.NewDecoder(r.Body).Decode(&sess); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if sess.ID == "" || sess.Role == "" {
			http.Error(w, "id and role required", 400)
			return
		}
		if err := store.RegisterAgent(sess); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, map[string]string{"status": "registered"})
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func (s *Server) handleAPIMessages(w http.ResponseWriter, r *http.Request) {
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	mm := messages.NewManager(store)

	if r.Method == http.MethodGet {
		msgs, err := mm.LoadAllMessages()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, msgs)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			From    string `json:"from"`
			Role    string `json:"role"`
			To      string `json:"to"`
			Subject string `json:"subject"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		msg, err := mm.SendMessage(req.From, req.Role, req.To, "", req.Subject, req.Content)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, msg)
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func (s *Server) handleAPIBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	mm := messages.NewManager(store)

	var req struct {
		From    string `json:"from"`
		Role    string `json:"role"`
		Subject string `json:"subject"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	msg, err := mm.SendMessage(req.From, req.Role, "all", "", req.Subject, req.Content)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, msg)
}

func (s *Server) handleAPIMoE(w http.ResponseWriter, r *http.Request) {
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	moeMgr := moe.NewManager(store, s.roleProv)
	panels, err := moeMgr.ListPanels("")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, panels)
}

func (s *Server) handleAPIMoEConvene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	moeMgr := moe.NewManager(store, s.roleProv)

	var req struct {
		Title       string   `json:"title"`
		Query       string   `json:"query"`
		Domain      string   `json:"domain"`
		Initiator   string   `json:"initiator"`
		Roles       []string `json:"roles"`
		MaxParallel int      `json:"max_parallel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	panel, err := moeMgr.ConvenePanel(req.Title, req.Query, req.Domain, req.Initiator, req.Roles, req.MaxParallel)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, panel)
}

func (s *Server) handleAPIMoEArgument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	moeMgr := moe.NewManager(store, s.roleProv)

	var req struct {
		PanelID     string   `json:"panel_id"`
		AgentID     string   `json:"agent_id"`
		Role        string   `json:"role"`
		Stance      string   `json:"stance"`
		Perspective string   `json:"perspective"`
		KeyPoints   []string `json:"key_points"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	arg, err := moeMgr.PostArgument(req.PanelID, req.AgentID, req.Role, req.Stance, req.Perspective, req.KeyPoints)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, arg)
}

func (s *Server) handleAPIMoESynthesize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	moeMgr := moe.NewManager(store, s.roleProv)

	var req struct {
		PanelID            string            `json:"panel_id"`
		AgentID            string            `json:"agent_id"`
		ConsensusDecision  string            `json:"consensus_decision"`
		TradeOffs          []string          `json:"trade_offs"`
		RecommendedActions []string          `json:"recommended_actions"`
		Packages           []moe.WorkPackage `json:"packages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	synth, err := moeMgr.Synthesize(req.PanelID, req.AgentID, req.ConsensusDecision, req.TradeOffs, req.RecommendedActions, req.Packages)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, synth)
}

func (s *Server) handleAPIMoEDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	moeMgr := moe.NewManager(store, s.roleProv)

	var req struct {
		PanelID string `json:"panel_id"`
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tracks, err := moeMgr.DispatchSubagents(req.PanelID, req.AgentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, tracks)
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	store, _, err := s.resolveProjectStore(r.URL.Query().Get("project"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gm := guardrails.NewManager(store)

	if r.Method == http.MethodGet {
		st, _ := store.LoadProjectState()
		gr, _ := gm.Load()
		resp := map[string]interface{}{
			"project_name": st.ProjectName,
			"goals":        st.Goals,
			"guardrails":   gr,
		}
		jsonResponse(w, resp)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			ProjectName string `json:"project_name"`
			GoalTitle   string `json:"goal_title"`
			GoalDesc    string `json:"goal_desc"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		st, err := store.LoadProjectState()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if req.ProjectName != "" {
			st.ProjectName = req.ProjectName
		}
		if req.GoalTitle != "" {
			st.Goals = append(st.Goals, state.Goal{
				ID:          fmt.Sprintf("goal-%d", time.Now().Unix()),
				Title:       req.GoalTitle,
				Description: req.GoalDesc,
				Status:      "active",
				CreatedAt:   time.Now(),
			})
		}
		_ = store.SaveProjectState(st)
		jsonResponse(w, map[string]string{"status": "updated"})
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>GOSSIP MESH // MULTI-PROJECT CONTROL PANEL</title>
<style>
/* ZERO BORDER-RADIUS // PURE 2-COLOR MONOCHROME // MONOSPACE TYPOGRAPHY */
* {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
  border-radius: 0 !important;
  font-family: 'Courier New', Courier, monospace, ui-monospace !important;
}

/* DARK THEME (DEFAULT) */
body {
  background-color: #000000;
  color: #ffffff;
  padding: 16px;
  line-height: 1.4;
}

header {
  border: 1px solid #ffffff;
  padding: 12px 16px;
  margin-bottom: 16px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

h1 {
  font-size: 16px;
  letter-spacing: 1px;
}

.status-badge {
  border: 1px solid #ffffff;
  padding: 4px 8px;
  font-size: 12px;
}

nav {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  border-bottom: 1px solid #ffffff;
  padding-bottom: 8px;
  flex-wrap: wrap;
}

.tab-btn {
  background: #000000;
  color: #ffffff;
  border: 1px solid #ffffff;
  padding: 8px 14px;
  cursor: pointer;
  font-weight: bold;
  font-size: 12px;
}

.tab-btn:hover, .tab-btn.active {
  background: #ffffff;
  color: #000000;
}

.panel {
  display: none;
}

.panel.active {
  display: block;
}

.box {
  border: 1px solid #ffffff;
  padding: 16px;
  margin-bottom: 16px;
}

.box-title {
  font-size: 13px;
  font-weight: bold;
  border-bottom: 1px solid #ffffff;
  padding-bottom: 8px;
  margin-bottom: 12px;
  letter-spacing: 1px;
}

table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 8px;
}

th, td {
  border: 1px solid #ffffff;
  padding: 8px 10px;
  text-align: left;
  font-size: 12px;
}

th {
  background-color: #ffffff;
  color: #000000;
  font-weight: bold;
}

input, select, textarea, button {
  background: #000000;
  color: #ffffff;
  border: 1px solid #ffffff;
  padding: 6px 10px;
  font-size: 12px;
  outline: none;
}

button {
  cursor: pointer;
  font-weight: bold;
}

button:hover {
  background: #ffffff;
  color: #000000;
}

.grid-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.chat-box {
  height: 380px;
  overflow-y: scroll;
  border: 1px solid #ffffff;
  padding: 12px;
  background: #000000;
  margin-bottom: 12px;
}

.chat-line {
  margin-bottom: 8px;
  font-size: 12px;
  border-bottom: 1px dashed #ffffff;
  padding-bottom: 4px;
}

.chat-meta {
  font-size: 11px;
}

.chat-content {
  margin-top: 2px;
}

.flex-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.flex-1 {
  flex: 1;
}

.badge {
  display: inline-block;
  padding: 2px 6px;
  border: 1px solid #ffffff;
  font-size: 11px;
}

.chan-tab-btn {
  background: #000000;
  color: #ffffff;
  border: 1px solid #ffffff;
  padding: 6px 10px;
  cursor: pointer;
  font-size: 11px;
  font-weight: bold;
}

.chan-tab-btn:hover, .chan-tab-btn.active {
  background: #ffffff;
  color: #000000;
}

.artifact-row {
  border: 1px solid #ffffff;
  padding: 8px 10px;
  margin-bottom: 6px;
  cursor: pointer;
  font-size: 11px;
}

.artifact-row:hover, .artifact-row.active {
  background: #ffffff;
  color: #000000;
}

.post-card {
  border: 1px solid #ffffff;
  padding: 10px;
  margin-bottom: 12px;
  background: #000000;
}

.post-card-header {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  border-bottom: 1px dashed #ffffff;
  padding-bottom: 4px;
  margin-bottom: 6px;
}

.post-card-subject {
  font-weight: bold;
  font-size: 12px;
  margin-bottom: 4px;
}

.post-card-content {
  font-size: 12px;
  white-space: pre-wrap;
  margin-bottom: 6px;
}

.post-card-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 10px;
  border-top: 1px dashed #ffffff;
  padding-top: 4px;
}

/* LIGHT THEME (STRICT 2-COLOR INVERSION) */
body.light-theme {
  background-color: #ffffff;
  color: #000000;
}

body.light-theme header,
body.light-theme nav,
body.light-theme .box,
body.light-theme .chat-box,
body.light-theme .status-badge,
body.light-theme .badge,
body.light-theme th,
body.light-theme td,
body.light-theme .box-title {
  border-color: #000000;
}

body.light-theme .chat-line {
  border-bottom-color: #000000;
}

body.light-theme th {
  background-color: #000000;
  color: #ffffff;
}

body.light-theme .tab-btn {
  background: #ffffff;
  color: #000000;
  border-color: #000000;
}

body.light-theme .tab-btn:hover,
body.light-theme .tab-btn.active {
  background: #000000;
  color: #ffffff;
}

body.light-theme .chan-tab-btn {
  background: #ffffff;
  color: #000000;
  border-color: #000000;
}

body.light-theme .chan-tab-btn:hover,
body.light-theme .chan-tab-btn.active {
  background: #000000;
  color: #ffffff;
}

body.light-theme .artifact-row {
  background: #ffffff;
  color: #000000;
  border-color: #000000;
}

body.light-theme .artifact-row:hover,
body.light-theme .artifact-row.active {
  background: #000000;
  color: #ffffff;
}

body.light-theme .post-card {
  background: #ffffff;
  color: #000000;
  border-color: #000000;
}

body.light-theme .post-card-header,
body.light-theme .post-card-footer {
  border-color: #000000;
}

body.light-theme input,
body.light-theme select,
body.light-theme textarea,
body.light-theme button {
  background: #ffffff;
  color: #000000;
  border-color: #000000;
}

body.light-theme button:hover {
  background: #000000;
  color: #ffffff;
}

body.light-theme .chat-box {
  background: #ffffff;
}

body.light-theme #task-edit-box,
body.light-theme #new-plan-box {
  background: #ffffff !important;
  border-color: #000000 !important;
  color: #000000 !important;
}

body.light-theme #social-feed {
  background: #ffffff !important;
  border-color: #000000 !important;
}
</style>
</head>
<body>

<header>
  <div>
    <h1>GOSSIP MESH // MULTI-PROJECT CONTROL PANEL</h1>
    <div style="font-size: 11px; margin-top: 4px;">
      WORKSPACE: <span id="hdr-proj-path">/loading...</span> // AGENTS: <span id="hdr-agents">0</span> // TASKS: <span id="hdr-tasks">0</span>
    </div>
  </div>
  
  <div class="flex-row">
    <!-- PROJECT SWITCHER DROPDOWN -->
    <div style="font-size: 12px; font-weight: bold;">
      PROJECT:
      <select id="project-selector" onchange="switchProject(this.value)">
        <option value="">[Scanning projects...]</option>
      </select>
    </div>
    
    <button onclick="toggleTheme()" id="theme-btn">[THEME: LIGHT]</button>
    <div class="status-badge" id="live-indicator">[SYNC: OK]</div>
  </div>
</header>

<nav>
  <button class="tab-btn active" onclick="switchTab('tasks')">[1] TASKS & DAG</button>
  <button class="tab-btn" onclick="switchTab('agents')">[2] LIVE AGENTS</button>
  <button class="tab-btn" onclick="switchTab('chats')">[3] SOCIAL FEED & CHAT</button>
  <button class="tab-btn" onclick="switchTab('moe')">[4] MOE COUNCIL</button>
  <button class="tab-btn" onclick="switchTab('plans')">[5] PLANS & ARTIFACTS</button>
  <button class="tab-btn" onclick="switchTab('projects')">[6] ALL PROJECTS</button>
  <button class="tab-btn" onclick="switchTab('settings')">[7] SETTINGS</button>
</nav>

<!-- UNINITIALIZED WARNING NOTICE -->
<div id="uninit-banner" class="box" style="display: none; border-color: #ffffff;">
  <div class="box-title">[!] PROJECT NOT INITIALIZED</div>
  <div>The selected workspace has no .local/gossip storage directory yet.</div>
  <div style="margin-top: 8px;">
    <button onclick="initActiveProject()">[INITIALIZE GOSSIP IN THIS WORKSPACE]</button>
  </div>
</div>

<!-- PANEL 1: TASKS & DAG -->
<div id="panel-tasks" class="panel active">
  <div class="box">
    <div class="box-title">[+] CREATE TASK IN GLOBAL DAG</div>
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="task-title" placeholder="Task Title" class="flex-1">
      <select id="task-role">
        <option value="worker">Role: worker</option>
        <option value="orchestrator">Role: orchestrator</option>
        <option value="network_engineer">Role: network_engineer</option>
        <option value="math_expert">Role: math_expert</option>
        <option value="systems_architect">Role: systems_architect</option>
        <option value="security_specialist">Role: security_specialist</option>
        <option value="qa_engineer">Role: qa_engineer</option>
        <option value="researcher">Role: researcher</option>
        <option value="red_team">Role: red_team</option>
        <option value="critic">Role: critic</option>
      </select>
      <select id="task-priority">
        <option value="high">Priority: high</option>
        <option value="medium" selected>Priority: medium</option>
        <option value="low">Priority: low</option>
      </select>
    </div>
    <div class="flex-row">
      <input type="text" id="task-desc" placeholder="Task description and acceptance criteria" class="flex-1">
      <input type="text" id="task-blocked-by" placeholder="Blocked by task IDs (comma separated)" style="width: 260px;">
      <button onclick="createTask()">[+ ADD TASK]</button>
    </div>
  </div>

  <!-- INLINE TASK MODIFICATION EDITOR -->
  <div class="box" id="task-edit-box" style="display: none; border-color: #ffffff; background: #000000; margin-bottom: 16px;">
    <div class="box-title flex-row" style="justify-content: space-between; align-items: center;">
      <span>[EDIT TASK] <span id="edit-task-id-disp">-</span></span>
      <button onclick="closeTaskEdit()">[X CLOSE]</button>
    </div>
    <input type="hidden" id="edit-task-id">
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="edit-task-title" placeholder="Task Title" class="flex-1">
      <select id="edit-task-role">
        <option value="worker">Role: worker</option>
        <option value="orchestrator">Role: orchestrator</option>
        <option value="network_engineer">Role: network_engineer</option>
        <option value="math_expert">Role: math_expert</option>
        <option value="systems_architect">Role: systems_architect</option>
        <option value="security_specialist">Role: security_specialist</option>
        <option value="qa_engineer">Role: qa_engineer</option>
        <option value="researcher">Role: researcher</option>
        <option value="red_team">Role: red_team</option>
        <option value="critic">Role: critic</option>
      </select>
      <select id="edit-task-priority">
        <option value="high">Priority: high</option>
        <option value="medium">Priority: medium</option>
        <option value="low">Priority: low</option>
      </select>
      <select id="edit-task-status">
        <option value="pending">Status: pending</option>
        <option value="in_progress">Status: in_progress</option>
        <option value="review">Status: review</option>
        <option value="completed">Status: completed</option>
        <option value="blocked">Status: blocked</option>
        <option value="cancelled">Status: cancelled</option>
      </select>
    </div>
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="edit-task-assignee" placeholder="Assigned Agent ID (e.g. worker-1)" style="width: 220px;">
      <input type="text" id="edit-task-blocked-by" placeholder="Blocked By IDs (comma separated)" class="flex-1">
    </div>
    <div style="margin-bottom: 8px;">
      <textarea id="edit-task-desc" placeholder="Task description and deliverables" style="width: 100%; height: 60px;"></textarea>
    </div>
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="edit-task-criteria" placeholder="Acceptance criteria (comma separated)" class="flex-1">
      <input type="text" id="edit-task-note" placeholder="Add progress note or incident status..." class="flex-1">
    </div>
    <div class="flex-row" style="justify-content: flex-end; gap: 8px;">
      <button onclick="closeTaskEdit()">[CANCEL]</button>
      <button onclick="saveTaskEdit()">[SAVE TASK CHANGES]</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">[TASKS] SHARED TASK MATRIX (DAG)</div>
    <table>
      <thead>
        <tr>
          <th>ID</th>
          <th>TITLE</th>
          <th>ROLE</th>
          <th>PRIORITY</th>
          <th>STATUS</th>
          <th>ASSIGNEE</th>
          <th>BLOCKERS</th>
          <th>ACTION</th>
        </tr>
      </thead>
      <tbody id="tasks-table-body">
        <tr><td colspan="8">Loading tasks...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- PANEL 2: LIVE AGENTS -->
<div id="panel-agents" class="panel">
  <div class="box">
    <div class="box-title">[+] REGISTER OR REIDENTIFY AGENT</div>
    <div class="flex-row">
      <input type="text" id="agent-reg-id" placeholder="Agent ID (e.g. worker-1)" class="flex-1">
      <select id="agent-reg-role">
        <option value="worker">worker</option>
        <option value="orchestrator">orchestrator</option>
        <option value="network_engineer">network_engineer</option>
        <option value="math_expert">math_expert</option>
        <option value="systems_architect">systems_architect</option>
        <option value="security_specialist">security_specialist</option>
        <option value="qa_engineer">qa_engineer</option>
        <option value="researcher">researcher</option>
        <option value="red_team">red_team</option>
        <option value="critic">critic</option>
      </select>
      <input type="text" id="agent-reg-task" placeholder="Current Task ID (optional)" style="width: 220px;">
      <button onclick="registerAgent()">[REGISTER AGENT]</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">[AGENTS] ACTIVE AGENT REGISTRY & PRESENCE</div>
    <table>
      <thead>
        <tr>
          <th>AGENT ID</th>
          <th>ROLE</th>
          <th>STATUS</th>
          <th>ACTIVE TASK</th>
          <th>SESSIONS</th>
          <th>LAST HEARTBEAT</th>
          <th>ACTIONS</th>
        </tr>
      </thead>
      <tbody id="agents-table-body">
        <tr><td colspan="7">Loading agents...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- PANEL 3: SOCIAL FEED & CHAT -->
<div id="panel-chats" class="panel">
  <div style="display: flex; gap: 16px;">
    <!-- LEFT: CHANNELS & DIRECT MESSAGES -->
    <div style="width: 280px; flex-shrink: 0;">
      <div class="box">
        <div class="box-title">[CHANNELS & CONTACTS]</div>
        
        <div style="font-size: 10px; font-weight: bold; margin-bottom: 4px;">// STREAMS</div>
        <button class="chan-tab-btn active" id="chan-tab-all" onclick="selectChannel('all', 'stream')" style="width: 100%; text-align: left; margin-bottom: 4px;">[ALL POSTS] (Mesh Feed)</button>
        <button class="chan-tab-btn" id="chan-tab-broadcast" onclick="selectChannel('broadcast', 'broadcast')" style="width: 100%; text-align: left; margin-bottom: 8px;">[#broadcast] (Directives)</button>
        
        <div style="font-size: 10px; font-weight: bold; margin-bottom: 4px;">// ROLE CHANNELS</div>
        <button class="chan-tab-btn" id="chan-tab-role-orchestrator" onclick="selectChannel('orchestrator', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#orchestrators</button>
        <button class="chan-tab-btn" id="chan-tab-role-worker" onclick="selectChannel('worker', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#workers</button>
        <button class="chan-tab-btn" id="chan-tab-role-systems_architect" onclick="selectChannel('systems_architect', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#systems_architect</button>
        <button class="chan-tab-btn" id="chan-tab-role-security_specialist" onclick="selectChannel('security_specialist', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#security_specialist</button>
        <button class="chan-tab-btn" id="chan-tab-role-qa_engineer" onclick="selectChannel('qa_engineer', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#qa_engineer</button>
        <button class="chan-tab-btn" id="chan-tab-role-math_expert" onclick="selectChannel('math_expert', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#math_expert</button>
        <button class="chan-tab-btn" id="chan-tab-role-network_engineer" onclick="selectChannel('network_engineer', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#network_engineer</button>
        <button class="chan-tab-btn" id="chan-tab-role-red_team" onclick="selectChannel('red_team', 'role')" style="width: 100%; text-align: left; margin-bottom: 2px;">#red_team</button>
        <button class="chan-tab-btn" id="chan-tab-role-general" onclick="selectChannel('general', 'role')" style="width: 100%; text-align: left; margin-bottom: 8px;">#general</button>
        
        <div style="font-size: 10px; font-weight: bold; margin-bottom: 4px;">// DIRECT MESSAGES (1-ON-1)</div>
        <div id="dm-agents-list" style="margin-bottom: 8px;">
          <div style="font-size: 11px; font-style: italic;">Scanning online agents...</div>
        </div>
        <div class="flex-row">
          <input type="text" id="custom-dm-target" placeholder="@agent-id" style="font-size: 11px;">
          <button onclick="startCustomDM()">[CHAT]</button>
        </div>
      </div>
    </div>

    <!-- RIGHT: SOCIAL POST FEED & COMPOSER -->
    <div style="flex: 1;">
      <div class="box">
        <div class="box-title flex-row" style="justify-content: space-between; align-items: center;">
          <span id="current-channel-title">[STREAM: ALL POSTS]</span>
          <span id="current-channel-target" style="font-size: 11px; font-weight: normal;">Target: all</span>
        </div>

        <div id="social-feed" style="height: 480px; overflow-y: auto; padding: 4px; margin-bottom: 12px; border: 1px solid currentColor;">
          <div style="padding: 8px; font-style: italic;">Loading social feed...</div>
        </div>

        <!-- SOCIAL COMPOSER -->
        <div style="border-top: 1px dashed currentColor; padding-top: 10px;">
          <div class="flex-row" style="margin-bottom: 6px;">
            <input type="text" id="social-from" value="user" placeholder="From (e.g. user)" style="width: 140px;">
            <input type="text" id="social-to" value="all" placeholder="To (e.g. all, role:worker, @agent-id)" style="width: 220px;">
            <input type="text" id="social-subject" placeholder="Subject / headline (optional)" class="flex-1">
          </div>
          <div>
            <textarea id="social-content" placeholder="Compose message, task dispatch, broadcast update, or reply... (Press Ctrl+Enter to post)" style="width: 100%; height: 75px;" onkeydown="handleChatKeyDown(event)"></textarea>
          </div>
          <div class="flex-row" style="justify-content: space-between; align-items: center; margin-top: 6px;">
            <span style="font-size: 10px;">[TIP] Ctrl+Enter to send immediately</span>
            <button onclick="sendSocialMessage()">[POST TO STREAM / DIRECT]</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- PANEL 4: MOE COUNCIL -->
<div id="panel-moe" class="panel">
  <div class="box">
    <div class="box-title">[MOE] CONVENE MIXTURE OF EXPERTS COUNCIL</div>
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="moe-title" placeholder="Panel Topic / Title" class="flex-1">
      <input type="text" id="moe-domain" placeholder="Domain keywords (e.g. networking, crypto, locks)" style="width: 280px;">
      <input type="number" id="moe-parallel" value="4" style="width: 140px;" placeholder="Max Parallel">
    </div>
    <div class="flex-row">
      <textarea id="moe-query" placeholder="Complex technical dilemma, architectural choice, or trade-off statement" style="width: 100%; height: 60px;"></textarea>
    </div>
    <div style="margin-top: 8px;">
      <button onclick="conveneMoE()">[CONVENE EXPERT COUNCIL]</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">[MOE] ACTIVE PANELS & SUBAGENT PARALLEL TRACKS</div>
    <div id="moe-panels-list">Loading MoE Councils...</div>
  </div>
</div>

<!-- PANEL 5: PLANS & ARTIFACTS -->
<div id="panel-plans" class="panel">
  <div style="display: flex; gap: 16px;">
    <!-- LEFT: ARTIFACTS & PLANS EXPLORER -->
    <div style="width: 320px; flex-shrink: 0;">
      <div class="box">
        <div class="box-title flex-row" style="justify-content: space-between; align-items: center;">
          <span>[PLANS & ARTIFACTS]</span>
          <button onclick="loadArtifactsList()">[REFRESH]</button>
        </div>
        
        <input type="text" id="artifact-filter" placeholder="Filter plans & files..." oninput="filterArtifactsList(this.value)" style="width: 100%; margin-bottom: 8px;">
        <button onclick="toggleNewPlanBox()" style="width: 100%; margin-bottom: 8px;">[+ NEW PLAN ARTIFACT]</button>

        <!-- INLINE NEW PLAN BOX -->
        <div id="new-plan-box" style="display: none; border: 1px solid currentColor; padding: 8px; margin-bottom: 8px;">
          <div style="font-size: 11px; font-weight: bold; margin-bottom: 4px;">CREATE NEW PLAN FILE</div>
          <input type="text" id="new-plan-name" placeholder="plan_spec.md" style="width: 100%; margin-bottom: 4px;">
          <select id="new-plan-loc" style="width: 100%; margin-bottom: 4px;">
            <option value="artifacts">.local/gossip/artifacts/</option>
            <option value="root">Project Root (/)</option>
          </select>
          <textarea id="new-plan-content" placeholder="# Plan Title&#10;&#10;Objectives and architecture..." style="width: 100%; height: 60px; margin-bottom: 4px;"></textarea>
          <div class="flex-row" style="justify-content: flex-end; gap: 4px;">
            <button onclick="toggleNewPlanBox()">[CANCEL]</button>
            <button onclick="createNewPlanArtifact()">[CREATE]</button>
          </div>
        </div>

        <div id="artifacts-list-container" style="max-height: 520px; overflow-y: auto;">
          <div style="font-style: italic; font-size: 11px;">Scanning project plans...</div>
        </div>
      </div>
    </div>

    <!-- RIGHT: LARGE PLAN ARTIFACT EDITOR -->
    <div style="flex: 1;">
      <div class="box">
        <div class="box-title flex-row" style="justify-content: space-between; align-items: center;">
          <div>
            <span id="plan-filename" style="font-weight: bold;">BOARD.md</span>
            <span id="plan-dirty-flag" style="display: none; margin-left: 6px; font-size: 10px; border: 1px solid currentColor; padding: 1px 4px;">[MODIFIED *]</span>
          </div>
          <div class="flex-row" style="gap: 6px;">
            <button onclick="reloadCurrentPlan()">[RELOAD]</button>
            <button onclick="saveCurrentPlan()">[SAVE PLAN FILE]</button>
          </div>
        </div>

        <textarea id="plan-editor-content" style="width: 100%; height: 500px; font-family: monospace, ui-monospace, 'Courier New' !important; font-size: 12px; line-height: 1.45; white-space: pre; overflow-x: auto; resize: vertical;" oninput="onPlanContentChanged()" onkeydown="handlePlanEditorKeyDown(event)"></textarea>

        <div class="flex-row" style="justify-content: space-between; align-items: center; font-size: 11px; margin-top: 6px; border-top: 1px dashed currentColor; padding-top: 4px;">
          <span id="plan-stats">Lines: 0 | Words: 0 | Bytes: 0</span>
          <span id="plan-save-status">Status: Ready (Ctrl+S to save)</span>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- PANEL 6: ALL PROJECTS -->
<div id="panel-projects" class="panel">
  <div class="box">
    <div class="box-title">[+] ADD / OPEN PROJECT DIRECTORY</div>
    <div class="flex-row">
      <input type="text" id="open-proj-path" placeholder="/absolute/path/to/project (e.g. /home/user/Projects/railproject)" class="flex-1">
      <button onclick="addProjectDirectory()">[OPEN WORKSPACE]</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">[PROJECTS] DISCOVERED & REGISTERED WORKSPACES</div>
    <table>
      <thead>
        <tr>
          <th>PROJECT NAME</th>
          <th>DIRECTORY PATH</th>
          <th>GOSSIP STATUS</th>
          <th>AGENTS</th>
          <th>TASKS</th>
          <th>ACTION</th>
        </tr>
      </thead>
      <tbody id="projects-table-body">
        <tr><td colspan="6">Loading projects...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- PANEL 7: SETTINGS -->
<div id="panel-settings" class="panel">
  <div class="grid-2">
    <div class="box">
      <div class="box-title">[SETTINGS] PROJECT METADATA</div>
      <div style="margin-bottom: 12px;">
        <label>PROJECT NAME:</label>
        <input type="text" id="set-proj-name" style="width: 100%; margin-top: 4px;">
      </div>
      <div style="margin-bottom: 12px;">
        <label>NEW STRATEGIC MILESTONE / GOAL:</label>
        <input type="text" id="set-goal-title" placeholder="Goal title" style="width: 100%; margin-top: 4px;">
        <textarea id="set-goal-desc" placeholder="Goal criteria" style="width: 100%; height: 60px; margin-top: 4px;"></textarea>
      </div>
      <button onclick="updateSettings()">[SAVE SETTINGS]</button>
    </div>

    <div class="box">
      <div class="box-title">[GUARDRAILS] ACTIVE INVARIANTS & CAUTIONS</div>
      <div id="settings-guardrails-content">Loading guardrails...</div>
    </div>
  </div>
</div>

<script>
let currentTab = 'tasks';
let activeProjectPath = '';
let activeChannel = 'all';
let activeChannelType = 'stream'; // 'stream', 'broadcast', 'role', 'dm'
let allCachedMessages = [];
let allCachedArtifacts = [];
let activePlanRelPath = '';
let cachedTasksMap = {};

// THEME MANAGEMENT (PURE 2-COLOR MONOCHROME: LIGHT OR DARK)
function initTheme() {
  const saved = localStorage.getItem('gossip_theme') || 'dark';
  applyTheme(saved);
}

function toggleTheme() {
  const isLight = document.body.classList.contains('light-theme');
  const next = isLight ? 'dark' : 'light';
  applyTheme(next);
}

function applyTheme(theme) {
  const btn = document.getElementById('theme-btn');
  if (theme === 'light') {
    document.body.classList.add('light-theme');
    btn.innerText = '[THEME: DARK]';
  } else {
    document.body.classList.remove('light-theme');
    btn.innerText = '[THEME: LIGHT]';
  }
  localStorage.setItem('gossip_theme', theme);
}

function switchTab(tab) {
  currentTab = tab;
  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  
  const btn = Array.from(document.querySelectorAll('.tab-btn')).find(b => b.getAttribute('onclick') && b.getAttribute('onclick').includes("'" + tab + "'"));
  if (btn) btn.classList.add('active');
  
  const targetPanel = document.getElementById('panel-' + tab);
  if (targetPanel) targetPanel.classList.add('active');
  syncData();
}

async function loadProjects() {
  try {
    const res = await fetch('/api/projects');
    const projs = await res.json();
    const sel = document.getElementById('project-selector');
    const tbody = document.getElementById('projects-table-body');
    
    sel.innerHTML = '';
    projs.sort((a,b) => a.name.localeCompare(b.name));
    
    projs.forEach(p => {
      const opt = document.createElement('option');
      opt.value = p.path;
      opt.innerText = p.name + (p.is_active ? ' [* ACTIVE]' : '');
      if (p.is_active) {
        opt.selected = true;
        activeProjectPath = p.path;
        document.getElementById('hdr-proj-path').innerText = p.path;
        document.getElementById('uninit-banner').style.display = p.initialized ? 'none' : 'block';
      }
      sel.appendChild(opt);
    });

    if (tbody) {
      tbody.innerHTML = projs.map(p => {
        let actionBtn = p.is_active ? '<b>[* CURRENT]</b>' : '<button onclick="visitProject(\'' + escapeHTML(p.path) + '\')">[VISIT PROJECT]</button>';
        let initBtn = !p.initialized ? ' <button onclick="initSpecificProject(\'' + escapeHTML(p.path) + '\')">[INIT]</button>' : '';
        return '<tr>' +
          '<td><b>' + escapeHTML(p.name) + '</b></td>' +
          '<td>' + escapeHTML(p.path) + '</td>' +
          '<td>' + (p.initialized ? '[OK] INITIALIZED' : '[!] NOT INITIALIZED') + '</td>' +
          '<td>' + p.agent_count + '</td>' +
          '<td>' + p.task_count + '</td>' +
          '<td>' + actionBtn + initBtn + '</td>' +
        '</tr>';
      }).join('');
    }
  } catch(e) {
    console.error('Failed to load projects', e);
  }
}

async function switchProject(path) {
  if (!path) return;
  await fetch('/api/projects/select', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({path})
  });
  activePlanRelPath = '';
  await loadProjects();
  await syncData();
}

async function visitProject(path) {
  await switchProject(path);
  switchTab('tasks');
}

async function addProjectDirectory() {
  const path = document.getElementById('open-proj-path').value.trim();
  if (!path) return alert('Path is required');
  const res = await fetch('/api/projects/add', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({path})
  });
  if (!res.ok) {
    const err = await res.text();
    return alert('Error: ' + err);
  }
  document.getElementById('open-proj-path').value = '';
  await loadProjects();
  await syncData();
}

async function initActiveProject() {
  await fetch('/api/projects/init', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({path: activeProjectPath})
  });
  await loadProjects();
  await syncData();
}

async function initSpecificProject(path) {
  await fetch('/api/projects/init', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({path})
  });
  await loadProjects();
  await syncData();
}

async function syncData() {
  try {
    const stRes = await fetch('/api/status');
    const st = await stRes.json();
    document.getElementById('hdr-agents').innerText = st.agent_count;
    document.getElementById('hdr-tasks').innerText = st.task_count;
    document.getElementById('hdr-proj-path').innerText = st.project_path || activeProjectPath;

    if (currentTab === 'tasks') await syncTasks();
    if (currentTab === 'agents') await syncAgents();
    if (currentTab === 'chats') await syncChats();
    if (currentTab === 'moe') await syncMoE();
    if (currentTab === 'plans') await loadArtifactsList();
    if (currentTab === 'projects') await loadProjects();
    if (currentTab === 'settings') await syncSettings();
    document.getElementById('live-indicator').innerText = '[SYNC: OK]';
  } catch(e) {
    document.getElementById('live-indicator').innerText = '[SYNC: ERR]';
  }
}

async function syncTasks() {
  const res = await fetch('/api/tasks');
  const tasks = await res.json();
  const tbody = document.getElementById('tasks-table-body');
  cachedTasksMap = {};
  if (!tasks || tasks.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8">No tasks in global DAG for this project. Create one above!</td></tr>';
    return;
  }
  tasks.forEach(t => { cachedTasksMap[t.id] = t; });
  tbody.innerHTML = tasks.map(t => ` + "`" + `
    <tr>
      <td><b>${t.id}</b></td>
      <td>${escapeHTML(t.title)}</td>
      <td><span class="badge">${t.role}</span></td>
      <td>${t.priority}</td>
      <td><span class="badge">${t.status}</span></td>
      <td>${t.assignee || '<i>unassigned</i>'}</td>
      <td>${(t.blocked_by || []).join(', ') || '-'}</td>
      <td>
        <button onclick="openTaskEditor('${t.id}')">[EDIT]</button>
        <button onclick="quickUpdateTask('${t.id}', 'in_progress')">[START]</button>
        <button onclick="quickUpdateTask('${t.id}', 'done')">[DONE]</button>
      </td>
    </tr>
  ` + "`" + `).join('');
}

function openTaskEditor(taskID) {
  const t = cachedTasksMap[taskID];
  if (!t) return;
  document.getElementById('edit-task-id').value = t.id;
  document.getElementById('edit-task-id-disp').innerText = t.id;
  document.getElementById('edit-task-title').value = t.title || '';
  document.getElementById('edit-task-role').value = t.role || 'worker';
  document.getElementById('edit-task-priority').value = t.priority || 'medium';
  document.getElementById('edit-task-status').value = t.status || 'pending';
  document.getElementById('edit-task-assignee').value = t.assignee || '';
  document.getElementById('edit-task-blocked-by').value = (t.blocked_by || []).join(', ');
  document.getElementById('edit-task-desc').value = t.description || '';
  document.getElementById('edit-task-criteria').value = (t.acceptance_criteria || []).join(', ');
  document.getElementById('edit-task-note').value = '';
  
  const box = document.getElementById('task-edit-box');
  box.style.display = 'block';
  box.scrollIntoView({behavior: 'smooth', block: 'nearest'});
}

function closeTaskEdit() {
  document.getElementById('task-edit-box').style.display = 'none';
}

async function saveTaskEdit() {
  const task_id = document.getElementById('edit-task-id').value;
  if (!task_id) return;
  const title = document.getElementById('edit-task-title').value.trim();
  const role = document.getElementById('edit-task-role').value;
  const priority = document.getElementById('edit-task-priority').value;
  const status = document.getElementById('edit-task-status').value;
  const assigned_to = document.getElementById('edit-task-assignee').value.trim();
  const bbRaw = document.getElementById('edit-task-blocked-by').value;
  const blocked_by = bbRaw ? bbRaw.split(',').map(s => s.trim()).filter(Boolean) : [];
  const description = document.getElementById('edit-task-desc').value;
  const critRaw = document.getElementById('edit-task-criteria').value;
  const criteria = critRaw ? critRaw.split(',').map(s => s.trim()).filter(Boolean) : [];
  const progress_note = document.getElementById('edit-task-note').value.trim();

  const res = await fetch('/api/tasks/modify', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      task_id,
      title: title ? title : undefined,
      role: role ? role : undefined,
      priority: priority ? priority : undefined,
      status: status ? status : undefined,
      assigned_to: assigned_to !== undefined ? assigned_to : undefined,
      blocked_by: blocked_by,
      criteria: criteria,
      description: description ? description : undefined,
      progress_note: progress_note
    })
  });
  if (!res.ok) {
    const err = await res.text();
    return alert('Error modifying task: ' + err);
  }
  closeTaskEdit();
  await syncTasks();
}

async function syncAgents() {
  const res = await fetch('/api/agents');
  const agents = await res.json();
  const tbody = document.getElementById('agents-table-body');
  if (!agents || agents.length === 0) {
    tbody.innerHTML = '<tr><td colspan="7">No registered agents in this project.</td></tr>';
    return;
  }
  tbody.innerHTML = agents.map(a => ` + "`" + `
    <tr>
      <td><b>${a.id}</b></td>
      <td><span class="badge">${a.role}</span></td>
      <td>${a.status === 'active' ? '[ACTIVE]' : '[' + a.status.toUpperCase() + ']'}</td>
      <td>${a.current_task_id || '-'}</td>
      <td>#${a.session_count || 1}</td>
      <td>${new Date(a.last_heartbeat).toLocaleTimeString()}</td>
      <td>
        <button onclick="quickHeartbeat('${a.id}')">[PULSE]</button>
      </td>
    </tr>
  ` + "`" + `).join('');
}

function selectChannel(channel, type) {
  activeChannel = channel;
  activeChannelType = type;
  document.querySelectorAll('.chan-tab-btn').forEach(b => b.classList.remove('active'));
  
  if (type === 'stream') {
    const btn = document.getElementById('chan-tab-' + channel);
    if (btn) btn.classList.add('active');
    document.getElementById('current-channel-title').innerText = '[STREAM: ' + channel.toUpperCase() + ' POSTS]';
    document.getElementById('current-channel-target').innerText = 'Target: all';
    document.getElementById('social-to').value = 'all';
  } else if (type === 'broadcast') {
    const btn = document.getElementById('chan-tab-broadcast');
    if (btn) btn.classList.add('active');
    document.getElementById('current-channel-title').innerText = '[BROADCAST: ANNOUNCEMENTS]';
    document.getElementById('current-channel-target').innerText = 'Target: broadcast';
    document.getElementById('social-to').value = 'broadcast';
  } else if (type === 'role') {
    const btn = document.getElementById('chan-tab-role-' + channel);
    if (btn) btn.classList.add('active');
    document.getElementById('current-channel-title').innerText = '[GROUP CHAT: #' + channel + ']';
    document.getElementById('current-channel-target').innerText = 'Target: role:' + channel;
    document.getElementById('social-to').value = 'role:' + channel;
  } else if (type === 'dm') {
    const el = document.getElementById('chan-tab-dm-' + channel);
    if (el) el.classList.add('active');
    document.getElementById('current-channel-title').innerText = '[DIRECT MESSAGE: @' + channel + ']';
    document.getElementById('current-channel-target').innerText = 'Target: agent:' + channel;
    document.getElementById('social-to').value = channel;
  }
  renderSocialFeed();
}

function startCustomDM() {
  const inp = document.getElementById('custom-dm-target');
  let target = inp.value.trim();
  if (!target) return;
  if (target.startsWith('@')) target = target.slice(1);
  inp.value = '';
  selectChannel(target, 'dm');
}

function replyToPost(author, subject) {
  if (author) {
    document.getElementById('social-to').value = author;
    document.getElementById('current-channel-title').innerText = '[DIRECT REPLY: @' + author + ']';
    document.getElementById('current-channel-target').innerText = 'Target: agent:' + author;
  }
  if (subject) {
    const s = subject.startsWith('Re: ') ? subject : 'Re: ' + subject;
    document.getElementById('social-subject').value = s;
  }
  const contentInp = document.getElementById('social-content');
  contentInp.focus();
}

function handleChatKeyDown(e) {
  if (e.ctrlKey && e.key === 'Enter') {
    e.preventDefault();
    sendSocialMessage();
  }
}

async function sendSocialMessage() {
  const from = document.getElementById('social-from').value.trim() || 'user';
  let to = document.getElementById('social-to').value.trim() || 'all';
  const subject = document.getElementById('social-subject').value.trim();
  const content = document.getElementById('social-content').value.trim();
  if (!content) return alert('Message content required');

  let role = 'operator';
  if (from !== 'user') role = 'agent';

  await fetch('/api/messages', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({from, role, to, subject, content})
  });
  document.getElementById('social-content').value = '';
  await syncChats();
}

async function syncChats() {
  try {
    const res = await fetch('/api/messages');
    allCachedMessages = await res.json() || [];
    renderSocialFeed();

    // Also populate online agents in the DM list
    const aRes = await fetch('/api/agents');
    const agents = await aRes.json() || [];
    const dmBox = document.getElementById('dm-agents-list');
    if (!agents || agents.length === 0) {
      dmBox.innerHTML = '<div style="font-size: 11px; font-style: italic;">No active agents</div>';
    } else {
      dmBox.innerHTML = agents.map(a => {
        const isActive = (activeChannelType === 'dm' && activeChannel === a.id);
        return '<button class="chan-tab-btn' + (isActive ? ' active' : '') + '" id="chan-tab-dm-' + escapeHTML(a.id) + '" onclick="selectChannel(\'' + escapeHTML(a.id) + '\', \'dm\')" style="width: 100%; text-align: left; margin-bottom: 2px;">@' + escapeHTML(a.id) + ' [' + a.role + ']</button>';
      }).join('');
    }
  } catch(e) {
    console.error('syncChats error', e);
  }
}

function renderSocialFeed() {
  const feed = document.getElementById('social-feed');
  if (!feed) return;
  
  let filtered = allCachedMessages;
  if (activeChannelType === 'broadcast') {
    filtered = allCachedMessages.filter(m => m.to === 'all' || m.to === '*' || m.to === 'broadcast');
  } else if (activeChannelType === 'role') {
    filtered = allCachedMessages.filter(m => m.to === 'role:' + activeChannel || m.to === activeChannel || m.role === activeChannel);
  } else if (activeChannelType === 'dm') {
    filtered = allCachedMessages.filter(m => m.from === activeChannel || m.to === activeChannel || m.to === 'agent:' + activeChannel);
  }

  if (filtered.length === 0) {
    feed.innerHTML = '<div style="padding: 12px; font-style: italic;">No messages in this stream yet. Post below!</div>';
    return;
  }

  feed.innerHTML = filtered.map(m => {
    const timeStr = m.created_at ? new Date(m.created_at).toLocaleTimeString() : '';
    const subjectHTML = m.subject ? '<div class="post-card-subject">SUBJECT: ' + escapeHTML(m.subject) + '</div>' : '';
    return '<div class="post-card">' +
      '<div class="post-card-header">' +
        '<span><b>@' + escapeHTML(m.from) + '</b> [' + escapeHTML(m.role || 'agent') + '] -> <b>' + escapeHTML(m.to) + '</b></span>' +
        '<span>[' + timeStr + ']</span>' +
      '</div>' +
      subjectHTML +
      '<div class="post-card-content">' + escapeHTML(m.content) + '</div>' +
      '<div class="post-card-footer">' +
        '<span>ID: ' + escapeHTML(m.id || '-') + '</span>' +
        '<button onclick="replyToPost(\'' + escapeHTML(m.from) + '\', \'' + escapeHTML(m.subject || '') + '\')">[REPLY]</button>' +
      '</div>' +
    '</div>';
  }).join('');

  feed.scrollTop = feed.scrollHeight;
}

// PLANS & ARTIFACTS MANAGEMENT
async function loadArtifactsList() {
  try {
    const res = await fetch('/api/artifacts');
    allCachedArtifacts = await res.json() || [];
    renderArtifactsList(allCachedArtifacts);
    if (!activePlanRelPath && allCachedArtifacts.length > 0) {
      selectPlanFile(allCachedArtifacts[0].rel_path, allCachedArtifacts[0].name);
    }
  } catch(e) {
    console.error('loadArtifactsList error', e);
  }
}

function filterArtifactsList(query) {
  const q = (query || '').toLowerCase().trim();
  if (!q) {
    renderArtifactsList(allCachedArtifacts);
    return;
  }
  const filtered = allCachedArtifacts.filter(a => a.name.toLowerCase().includes(q) || a.rel_path.toLowerCase().includes(q) || a.category.toLowerCase().includes(q));
  renderArtifactsList(filtered);
}

function renderArtifactsList(items) {
  const container = document.getElementById('artifacts-list-container');
  if (!container) return;
  if (!items || items.length === 0) {
    container.innerHTML = '<div style="font-size: 11px; font-style: italic; padding: 4px;">No artifact files found</div>';
    return;
  }
  container.innerHTML = items.map(a => {
    const isActive = a.rel_path === activePlanRelPath;
    const catLabel = '[' + (a.category || 'FILE').toUpperCase() + ']';
    const sizeKB = (a.size_bytes / 1024).toFixed(1) + ' KB';
    return '<div class="artifact-row' + (isActive ? ' active' : '') + '" onclick="selectPlanFile(\'' + escapeHTML(a.rel_path) + '\', \'' + escapeHTML(a.name) + '\')">' +
      '<div style="font-weight: bold; display: flex; justify-content: space-between;">' +
        '<span>' + catLabel + ' ' + escapeHTML(a.name) + '</span>' +
        '<span style="font-size: 10px; font-weight: normal;">' + sizeKB + '</span>' +
      '</div>' +
      '<div style="font-size: 10px; opacity: 0.8; margin-top: 2px;">' + escapeHTML(a.rel_path) + '</div>' +
    '</div>';
  }).join('');
}

async function selectPlanFile(relPath, name) {
  const dirty = document.getElementById('plan-dirty-flag');
  if (dirty && dirty.style.display !== 'none') {
    if (!confirm('You have unsaved changes in the current plan. Discard and switch?')) {
      return;
    }
  }
  activePlanRelPath = relPath;
  document.getElementById('plan-filename').innerText = name || relPath;
  if (dirty) dirty.style.display = 'none';
  document.getElementById('plan-save-status').innerText = 'Loading ' + relPath + '...';

  try {
    const res = await fetch('/api/artifacts/content?file=' + encodeURIComponent(relPath));
    if (!res.ok) {
      const err = await res.text();
      document.getElementById('plan-editor-content').value = '// Failed to load: ' + err;
      return;
    }
    const data = await res.json();
    document.getElementById('plan-editor-content').value = data.content || '';
    updatePlanStats(data.content || '');
    document.getElementById('plan-save-status').innerText = 'Loaded ' + relPath + ' (' + (data.size_bytes || 0) + ' bytes)';
    renderArtifactsList(allCachedArtifacts);
  } catch(e) {
    console.error('selectPlanFile error', e);
  }
}

function onPlanContentChanged() {
  document.getElementById('plan-dirty-flag').style.display = 'inline-block';
  const val = document.getElementById('plan-editor-content').value;
  updatePlanStats(val);
}

function updatePlanStats(text) {
  const lines = text.split('\n').length;
  const words = text.trim() ? text.trim().split(/\s+/).length : 0;
  const bytes = new Blob([text]).size;
  document.getElementById('plan-stats').innerText = 'Lines: ' + lines + ' | Words: ' + words + ' | Bytes: ' + bytes;
}

function handlePlanEditorKeyDown(e) {
  if ((e.ctrlKey || e.metaKey) && e.key === 's') {
    e.preventDefault();
    saveCurrentPlan();
  }
}

async function saveCurrentPlan() {
  if (!activePlanRelPath) return alert('No plan file selected to save');
  const content = document.getElementById('plan-editor-content').value;
  document.getElementById('plan-save-status').innerText = 'Saving...';

  try {
    const res = await fetch('/api/artifacts/save', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({file: activePlanRelPath, content})
    });
    if (!res.ok) {
      const err = await res.text();
      return alert('Save error: ' + err);
    }
    const data = await res.json();
    document.getElementById('plan-dirty-flag').style.display = 'none';
    const timeStr = new Date().toLocaleTimeString();
    document.getElementById('plan-save-status').innerText = 'Saved at ' + timeStr + ' (' + data.size_bytes + ' bytes)';
    const it = allCachedArtifacts.find(a => a.rel_path === activePlanRelPath);
    if (it) it.size_bytes = data.size_bytes;
    renderArtifactsList(allCachedArtifacts);
  } catch(e) {
    console.error('saveCurrentPlan error', e);
    alert('Failed to save file: ' + e);
  }
}

async function reloadCurrentPlan() {
  if (!activePlanRelPath) return;
  selectPlanFile(activePlanRelPath, document.getElementById('plan-filename').innerText);
}

function toggleNewPlanBox() {
  const box = document.getElementById('new-plan-box');
  box.style.display = box.style.display === 'none' ? 'block' : 'none';
}

async function createNewPlanArtifact() {
  const name = document.getElementById('new-plan-name').value.trim();
  if (!name) return alert('File name required');
  const location = document.getElementById('new-plan-loc').value;
  const content = document.getElementById('new-plan-content').value;

  try {
    const res = await fetch('/api/artifacts/create', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({name, location, content})
    });
    if (!res.ok) {
      const err = await res.text();
      return alert('Create error: ' + err);
    }
    const data = await res.json();
    toggleNewPlanBox();
    document.getElementById('new-plan-name').value = '';
    document.getElementById('new-plan-content').value = '';
    await loadArtifactsList();
    selectPlanFile(data.file, data.name);
  } catch(e) {
    console.error('createNewPlanArtifact error', e);
  }
}

async function syncMoE() {
  const res = await fetch('/api/moe');
  const panels = await res.json();
  const box = document.getElementById('moe-panels-list');
  if (!panels || panels.length === 0) {
    box.innerHTML = '<div>No MoE Panels convened in this project yet. Convene one above!</div>';
    return;
  }
  box.innerHTML = panels.map(p => ` + "`" + `
    <div style="border: 1px solid currentColor; padding: 12px; margin-bottom: 12px;">
      <div style="display: flex; justify-content: space-between;">
        <b>${p.id}: ${escapeHTML(p.title)}</b>
        <span class="badge">${p.status.toUpperCase()}</span>
      </div>
      <div style="margin: 6px 0; font-size: 11px;">DOMAIN: ${p.domain} // PARALLEL WORKERS: ${p.max_parallel}</div>
      <div style="margin-bottom: 8px;">${escapeHTML(p.query)}</div>
      
      <div style="border-top: 1px dashed currentColor; padding-top: 6px; margin-top: 6px;">
        <b>EXPERTS SUMMONED:</b> ${(p.selected_experts || []).map(e => e.role).join(', ')}
      </div>

      ${p.synthesis ? ` + "`" + `
        <div style="margin-top: 8px; border: 1px solid currentColor; padding: 8px;">
          <b>CONSENSUS DECISION:</b> ${escapeHTML(p.synthesis.consensus_decision)}
          <div style="margin-top: 4px; font-size: 11px;">
            <b>PARALLEL WORK PACKAGES:</b> ${(p.synthesis.parallel_work_packages || []).length} items
          </div>
        </div>
      ` + "`" + ` : ` + "`" + `
        <div style="margin-top: 8px;">
          <button onclick="promptSynthesize('${p.id}')">[SYNTHESIZE CONSENSUS]</button>
        </div>
      ` + "`" + `}

      ${(p.subagent_tracks && p.subagent_tracks.length > 0) ? ` + "`" + `
        <div style="margin-top: 8px; font-size: 11px;">
          <b>PARALLEL SUBAGENTS RUNNING:</b> ${p.subagent_tracks.length} active tracks
        </div>
      ` + "`" + ` : (p.synthesis ? ` + "`" + `
        <div style="margin-top: 8px;">
          <button onclick="dispatchSubagents('${p.id}')">[DISPATCH PARALLEL SUBAGENTS]</button>
        </div>
      ` + "`" + ` : '')}
    </div>
  ` + "`" + `).join('');
}

async function syncSettings() {
  const res = await fetch('/api/settings');
  const data = await res.json();
  if (data.project_name && !document.getElementById('set-proj-name').value) {
    document.getElementById('set-proj-name').value = data.project_name;
  }
  const grBox = document.getElementById('settings-guardrails-content');
  if (data.guardrails) {
    const g = data.guardrails;
    grBox.innerHTML = ` + "`" + `
      <div style="margin-bottom: 8px;"><b>TECH STACK:</b> ${(g.tech_stack || []).join(', ') || 'General'}</div>
      <div style="margin-bottom: 8px;"><b>PROJECT INVARIANTS:</b> ${(g.invariants || []).join('; ') || 'None set'}</div>
      <div><b>CAUTION RULES:</b> ${(g.caution_rules || []).length} active security rules</div>
    ` + "`" + `;
  }
}

async function createTask() {
  const title = document.getElementById('task-title').value.trim();
  if (!title) return alert('Task title required');
  const role = document.getElementById('task-role').value;
  const priority = document.getElementById('task-priority').value;
  const description = document.getElementById('task-desc').value;
  const bbRaw = document.getElementById('task-blocked-by').value;
  const blocked_by = bbRaw ? bbRaw.split(',').map(s => s.trim()) : [];

  await fetch('/api/tasks', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({title, role, priority, description, blocked_by})
  });
  document.getElementById('task-title').value = '';
  document.getElementById('task-desc').value = '';
  document.getElementById('task-blocked-by').value = '';
  await syncTasks();
}

async function quickUpdateTask(task_id, status) {
  await fetch('/api/tasks/update', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({task_id, status})
  });
  await syncTasks();
}

async function registerAgent() {
  const id = document.getElementById('agent-reg-id').value.trim();
  if (!id) return alert('Agent ID required');
  const role = document.getElementById('agent-reg-role').value;
  const current_task_id = document.getElementById('agent-reg-task').value.trim();

  await fetch('/api/agents', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id, role, current_task_id, status: 'active'})
  });
  document.getElementById('agent-reg-id').value = '';
  document.getElementById('agent-reg-task').value = '';
  await syncAgents();
}

async function quickHeartbeat(id) {
  await fetch('/api/agents', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id, role: 'worker', status: 'active'})
  });
  await syncAgents();
}

async function conveneMoE() {
  const title = document.getElementById('moe-title').value.trim();
  const domain = document.getElementById('moe-domain').value.trim();
  const max_parallel = parseInt(document.getElementById('moe-parallel').value) || 4;
  const query = document.getElementById('moe-query').value.trim();
  if (!title || !query) return alert('Title and Problem Statement required');

  await fetch('/api/moe/convene', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({title, query, domain, initiator: 'web-user', max_parallel})
  });
  document.getElementById('moe-title').value = '';
  document.getElementById('moe-domain').value = '';
  document.getElementById('moe-query').value = '';
  await syncMoE();
}

async function promptSynthesize(panel_id) {
  const decision = prompt('Enter MoE Council Consensus Decision:');
  if (!decision) return;
  await fetch('/api/moe/synthesize', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      panel_id,
      agent_id: 'web-orchestrator',
      consensus_decision: decision,
      trade_offs: ['Parallel speedup vs branch merge complexity'],
      recommended_actions: ['Execute parallel work packages'],
      packages: [
        {package_id: 'p1', title: 'Implementation Track Alpha', assigned_role: 'worker', can_run_in_parallel: true},
        {package_id: 'p2', title: 'Adversarial Verification Beta', assigned_role: 'red_team', can_run_in_parallel: true}
      ]
    })
  });
  await syncMoE();
}

async function dispatchSubagents(panel_id) {
  await fetch('/api/moe/dispatch', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({panel_id, agent_id: 'web-orchestrator'})
  });
  await syncMoE();
}

async function updateSettings() {
  const project_name = document.getElementById('set-proj-name').value.trim();
  const goal_title = document.getElementById('set-goal-title').value.trim();
  const goal_desc = document.getElementById('set-goal-desc').value.trim();

  await fetch('/api/settings', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({project_name, goal_title, goal_desc})
  });
  alert('Settings saved!');
  await syncSettings();
}

function escapeHTML(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

initTheme();
loadProjects().then(() => {
  syncData();
  setInterval(syncData, 2500);
});
</script>

</body>
</html>
`
