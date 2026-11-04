package roles

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed default_roles.json
var embeddedDefaultRoles []byte

// RoleDefinition describes an agent's specialization, scope, and behavioral directives.
type RoleDefinition struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Category         string   `json:"category"` // "management", "engineering", "specialist", "assurance"
	Description      string   `json:"description"`
	SystemPrompt     string   `json:"system_prompt"`
	Directives       []string `json:"directives"`
	RecommendedTools []string `json:"recommended_tools"`
	CapabilityTags   []string `json:"capability_tags"`
}

// RolesFileSchema represents the JSON schema for roles.json.
type RolesFileSchema struct {
	Version string           `json:"version"`
	Roles   []RoleDefinition `json:"roles"`
}

// Provider defines the interface for querying and managing agent roles (SOLID Interface Segregation).
type Provider interface {
	GetRole(roleID string) (RoleDefinition, bool)
	ListRoles() []RoleDefinition
	RegisterRole(role RoleDefinition) error
	FormatRoleSummary() string
	SaveToFile(path string) error
}

// Service manages role resolution loaded entirely from JSON configurations.
type Service struct {
	mu    sync.RWMutex
	roles map[string]RoleDefinition
}

// DefaultService is the shared singleton loaded from JSON.
var (
	defaultService     *Service
	defaultServiceOnce sync.Once
)

// NewService instantiates a role service loaded from JSON.
// Search order:
// 1. customFilePath if provided and exists
// 2. <storageDir>/roles.json if exists
// 3. config/roles.json if exists
// 4. Embedded default_roles.json
func NewService(storageDir, customFilePath string) (*Service, error) {
	s := &Service{
		roles: make(map[string]RoleDefinition),
	}

	// Always load baseline from embedded JSON first
	if err := s.loadFromBytes(embeddedDefaultRoles); err != nil {
		return nil, fmt.Errorf("failed to load embedded default roles: %w", err)
	}

	// Then check disk paths for overrides or additions
	candidatePaths := []string{}
	if customFilePath != "" {
		candidatePaths = append(candidatePaths, customFilePath)
	}
	if storageDir != "" {
		candidatePaths = append(candidatePaths, filepath.Join(storageDir, "roles.json"))
	}
	candidatePaths = append(candidatePaths, filepath.Join("config", "roles.json"))

	for _, p := range candidatePaths {
		data, err := os.ReadFile(p)
		if err == nil {
			_ = s.loadFromBytes(data)
			break
		}
	}

	return s, nil
}

// GetDefaultService returns the initialized default service.
func GetDefaultService() *Service {
	defaultServiceOnce.Do(func() {
		srv, err := NewService("", "")
		if err != nil {
			panic(fmt.Sprintf("critical error initializing default roles service from JSON: %v", err))
		}
		defaultService = srv
	})
	return defaultService
}

func (s *Service) loadFromBytes(data []byte) error {
	var fileSchema RolesFileSchema
	if err := json.Unmarshal(data, &fileSchema); err != nil {
		// Try unmarshaling as map or slice
		var list []RoleDefinition
		if err2 := json.Unmarshal(data, &list); err2 == nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, r := range list {
				norm := normalizeRoleID(r.ID)
				s.roles[norm] = r
			}
			return nil
		}
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range fileSchema.Roles {
		norm := normalizeRoleID(r.ID)
		s.roles[norm] = r
	}
	return nil
}

func normalizeRoleID(id string) string {
	norm := strings.ToLower(strings.TrimSpace(id))
	norm = strings.ReplaceAll(norm, "-", "_")
	return norm
}

// GetRole returns the definition for a given role ID.
func (s *Service) GetRole(roleID string) (RoleDefinition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[normalizeRoleID(roleID)]
	return r, ok
}

// ListRoles returns all registered role definitions.
func (s *Service) ListRoles() []RoleDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]RoleDefinition, 0, len(s.roles))
	for _, r := range s.roles {
		list = append(list, r)
	}
	return list
}

// RegisterRole registers a new or custom role at runtime dynamically.
func (s *Service) RegisterRole(role RoleDefinition) error {
	if role.ID == "" || role.Title == "" || role.SystemPrompt == "" {
		return fmt.Errorf("role 'id', 'title', and 'system_prompt' are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[normalizeRoleID(role.ID)] = role
	return nil
}

// SaveToFile exports the current role registry to a JSON file.
func (s *Service) SaveToFile(path string) error {
	s.mu.RLock()
	list := make([]RoleDefinition, 0, len(s.roles))
	for _, r := range s.roles {
		list = append(list, r)
	}
	s.mu.RUnlock()

	out := RolesFileSchema{
		Version: "1.0.0",
		Roles:   list,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// FormatRoleSummary returns a formatted human-readable summary of all roles.
func (s *Service) FormatRoleSummary() string {
	var b strings.Builder
	b.WriteString("# Gossip Multi-Agent Role Directory (JSON-Driven)\n\n")
	for _, r := range s.ListRoles() {
		b.WriteString(fmt.Sprintf("### %s (`%s`) [%s]\n", r.Title, r.ID, r.Category))
		b.WriteString(fmt.Sprintf("%s\n\n", r.Description))
		if len(r.Directives) > 0 {
			b.WriteString("**Directives:**\n")
			for _, d := range r.Directives {
				b.WriteString(fmt.Sprintf("- %s\n", d))
			}
		}
		if len(r.CapabilityTags) > 0 {
			b.WriteString(fmt.Sprintf("\n**Capabilities:** `%s`\n\n---\n\n", strings.Join(r.CapabilityTags, "`, `")))
		}
	}
	return b.String()
}

// Package-level functions for convenience delegating to DefaultService

func GetRole(roleID string) (RoleDefinition, bool) {
	return GetDefaultService().GetRole(roleID)
}

func ListRoles() []RoleDefinition {
	return GetDefaultService().ListRoles()
}

func RegisterRole(role RoleDefinition) error {
	return GetDefaultService().RegisterRole(role)
}

func FormatRoleSummary() string {
	return GetDefaultService().FormatRoleSummary()
}

// GetDefaultRolesJSON returns the raw default JSON bytes.
func GetDefaultRolesJSON() []byte {
	return embeddedDefaultRoles
}
