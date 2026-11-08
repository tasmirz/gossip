package coord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const agentPresenceTTL = 45 * time.Second

type AgentPresence struct {
	PrincipalID      string            `json:"principal_id"`
	Name             string            `json:"name"`
	Role             string            `json:"role"`
	Online           bool              `json:"online"`
	LastSeenAt       int64             `json:"last_seen_at,omitempty"`
	Capabilities     []AgentCapability `json:"capabilities,omitempty"`
	MCPClientName    string            `json:"mcp_client_name,omitempty"`
	MCPClientVersion string            `json:"mcp_client_version,omitempty"`
}

// MCPClientInfo is client-provided descriptive metadata, never an identity or capability proof.
type MCPClientInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// AgentCapability is capability metadata reported by the MCP adapter or its
// configured host. Host reports are descriptive and are never permissions.
type AgentCapability struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

var adapterCapabilities = map[string]bool{
	"mcp_tools":                true,
	"git_workspace_inspection": true,
	"lease_renewal":            true,
	"durable_outbox":           true,
}

var hostCapabilities = map[string]bool{
	"notification":       true,
	"wake":               true,
	"launch":             true,
	"interrupt":          true,
	"isolated_workspace": true,
	"usage_reporting":    true,
	"model_selection":    true,
}

func AgentCapabilitySupported(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return adapterCapabilities[name] || hostCapabilities[name]
}

func normalizeRequiredCapabilities(names []string) ([]string, error) {
	if len(names) > 16 {
		return nil, errors.New("at most 16 task capabilities may be required")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !AgentCapabilitySupported(name) {
			return nil, fmt.Errorf("unsupported required capability %q", raw)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func requireReportedCapabilities(ctx context.Context, tx *sql.Tx, project, principal string, required []string, now int64) error {
	if len(required) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT instance_id FROM agent_sessions WHERE project_id=? AND principal_id=? AND ended_at=0 AND last_seen_at>=?`, project, principal, now-agentPresenceTTL.Milliseconds())
	if err != nil {
		return err
	}
	var sessions []string
	for rows.Next() {
		var session string
		if err = rows.Scan(&session); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, session)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, session := range sessions {
		rows, err = tx.QueryContext(ctx, `SELECT capability FROM agent_session_capabilities WHERE instance_id=?`, session)
		if err != nil {
			return err
		}
		reported := map[string]bool{}
		for rows.Next() {
			var capability string
			if err = rows.Scan(&capability); err != nil {
				rows.Close()
				return err
			}
			reported[capability] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		allPresent := true
		for _, capability := range required {
			if !reported[capability] {
				allPresent = false
				break
			}
		}
		if allPresent {
			return nil
		}
	}
	return fmt.Errorf("task requires a current authenticated session reporting capabilities %s: %w", strings.Join(required, ", "), ErrConflict)
}

// HostReportedCapabilities validates locally configured host capability
// labels. These labels describe the configured integration, not verified
// execution support or authorization.
func HostReportedCapabilities(names []string) ([]AgentCapability, error) {
	if len(names) > 16 {
		return nil, errors.New("at most 16 host capabilities may be configured")
	}
	out := make([]AgentCapability, 0, len(names))
	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !hostCapabilities[name] {
			return nil, fmt.Errorf("unsupported host capability %q", raw)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, AgentCapability{Name: name, Source: "host_reported"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// HeartbeatAgent records process presence for an authenticated adapter
// instance. It is metadata only and does not grant any project permissions.
func (s *Store) HeartbeatAgent(ctx context.Context, project, token, instanceID string) error {
	return s.HeartbeatAgentWithCapabilities(ctx, project, token, instanceID, nil)
}

func (s *Store) HeartbeatAgentWithCapabilities(ctx context.Context, project, token, instanceID string, capabilities []AgentCapability) error {
	return s.HeartbeatAgentWithMetadata(ctx, project, token, instanceID, capabilities, MCPClientInfo{})
}

func (s *Store) HeartbeatAgentWithMetadata(ctx context.Context, project, token, instanceID string, capabilities []AgentCapability, client MCPClientInfo) error {
	if strings.TrimSpace(instanceID) == "" || len(instanceID) > 128 {
		return errors.New("invalid agent instance ID")
	}
	if len(capabilities) > 20 {
		return errors.New("at most 20 agent capabilities may be reported")
	}
	seenCapabilities := map[string]bool{}
	for _, capability := range capabilities {
		name := strings.ToLower(strings.TrimSpace(capability.Name))
		valid := capability.Source == "adapter" && adapterCapabilities[name] || capability.Source == "host_reported" && hostCapabilities[name]
		if !valid || seenCapabilities[capability.Source+"\x00"+name] {
			return fmt.Errorf("invalid or duplicate agent capability %q", capability.Name)
		}
		seenCapabilities[capability.Source+"\x00"+name] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner", "worker", "reviewer")
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	client.Name = cleanClientInfo(client.Name, 128)
	client.Version = cleanClientInfo(client.Version, 64)
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(instance_id,project_id,principal_id,started_at,last_seen_at,ended_at,client_name,client_version)
		VALUES(?,?,?,?,?,0,?,?) ON CONFLICT(instance_id) DO UPDATE SET last_seen_at=excluded.last_seen_at,client_name=excluded.client_name,client_version=excluded.client_version
		WHERE agent_sessions.project_id=excluded.project_id AND agent_sessions.principal_id=excluded.principal_id AND agent_sessions.ended_at=0`, instanceID, project, actor.ID, now, now, client.Name, client.Version)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_session_capabilities WHERE instance_id=?`, instanceID); err != nil {
		return err
	}
	for _, capability := range capabilities {
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_session_capabilities(instance_id,capability,source) VALUES(?,?,?)`, instanceID, strings.ToLower(strings.TrimSpace(capability.Name)), capability.Source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func cleanClientInfo(value string, max int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if len(value) > max {
		value = strings.ToValidUTF8(value[:max], "")
	}
	return value
}

func (s *Store) EndAgentSession(ctx context.Context, project, token, instanceID string) error {
	if strings.TrimSpace(instanceID) == "" || len(instanceID) > 128 {
		return errors.New("invalid agent instance ID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner", "worker", "reviewer")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET ended_at=? WHERE instance_id=? AND project_id=? AND principal_id=? AND ended_at=0`, time.Now().UnixMilli(), instanceID, project, actor.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT 1 FROM agent_sessions WHERE instance_id=? AND project_id=? AND principal_id=?`, instanceID, project, actor.ID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListAgentPresence returns current project members with their freshest
// adapter session. A crashed process becomes offline after the heartbeat TTL.
func (s *Store) ListAgentPresence(ctx context.Context, project, token string) ([]AgentPresence, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-agentPresenceTTL).UnixMilli()
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,p.role,COALESCE(MAX(s.last_seen_at),0),
		COALESCE(MAX(CASE WHEN s.ended_at=0 AND s.last_seen_at>=? THEN 1 ELSE 0 END),0)
		FROM principals p LEFT JOIN agent_sessions s ON s.project_id=p.project_id AND s.principal_id=p.id
		WHERE p.project_id=? AND p.revoked=0 GROUP BY p.id ORDER BY p.name,p.id`, cutoff, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentPresence{}
	for rows.Next() {
		var item AgentPresence
		var online int
		if err = rows.Scan(&item.PrincipalID, &item.Name, &item.Role, &item.LastSeenAt, &online); err != nil {
			return nil, err
		}
		item.Online = online != 0
		metadataErr := tx.QueryRowContext(ctx, `SELECT client_name,client_version FROM agent_sessions WHERE project_id=? AND principal_id=? AND ended_at=0 AND last_seen_at>=? ORDER BY last_seen_at DESC LIMIT 1`, project, item.PrincipalID, cutoff).Scan(&item.MCPClientName, &item.MCPClientVersion)
		if metadataErr != nil && !errors.Is(metadataErr, sql.ErrNoRows) {
			return nil, metadataErr
		}
		out = append(out, item)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		var sessionID string
		err = tx.QueryRowContext(ctx, `SELECT instance_id FROM agent_sessions WHERE project_id=? AND principal_id=? ORDER BY last_seen_at DESC LIMIT 1`, project, out[i].PrincipalID).Scan(&sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		capRows, queryErr := tx.QueryContext(ctx, `SELECT capability,source FROM agent_session_capabilities WHERE instance_id=? ORDER BY source,capability`, sessionID)
		if queryErr != nil {
			return nil, queryErr
		}
		for capRows.Next() {
			var capability AgentCapability
			if queryErr = capRows.Scan(&capability.Name, &capability.Source); queryErr != nil {
				capRows.Close()
				return nil, queryErr
			}
			out[i].Capabilities = append(out[i].Capabilities, capability)
		}
		queryErr = capRows.Err()
		capRows.Close()
		if queryErr != nil {
			return nil, queryErr
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
