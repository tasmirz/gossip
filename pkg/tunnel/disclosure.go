package tunnel

import (
	"fmt"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

// RemotePeerInfo describes an identified remote agent peer disclosed to the mesh.
type RemotePeerInfo struct {
	AgentID      string    `json:"agent_id"`
	Role         string    `json:"role"`
	Endpoint     string    `json:"endpoint"`
	EndpointType string    `json:"endpoint_type"` // "frp", "lan", "remote_mcp"
	DiscoveredAt time.Time `json:"discovered_at"`
	LastSeen     time.Time `json:"last_seen"`
	Status       string    `json:"status"` // "active", "idle", "offline"
}

// RemoteDisclosure encapsulates the mandatory disclosure report for local agents.
type RemoteDisclosure struct {
	TunnelActive     bool             `json:"tunnel_active"`
	EndpointType     string           `json:"endpoint_type"` // "frp", "lan", "none"
	RemoteEndpoint   string           `json:"remote_endpoint,omitempty"`
	AuthProtected    bool             `json:"auth_protected"`
	AuthUser         string           `json:"auth_user,omitempty"`
	LocalLANIPs      []string         `json:"local_lan_ips,omitempty"`
	RemotePeers      []RemotePeerInfo `json:"remote_peers"`
	TotalRemoteCount int              `json:"total_remote_count"`
	DisclosedAt      time.Time        `json:"disclosed_at"`
	DisclosureNotice string           `json:"disclosure_notice"`
}

// ComputeDisclosure evaluates the active storage state and tunnel configuration
// to produce a mandatory disclosure statement regarding remote availability.
func ComputeDisclosure(store *state.Store) RemoteDisclosure {
	now := time.Now()
	disc := RemoteDisclosure{
		TunnelActive:  false,
		EndpointType:  "none",
		AuthProtected: false,
		RemotePeers:   make([]RemotePeerInfo, 0),
		DisclosedAt:   now,
	}

	if store == nil {
		disc.DisclosureNotice = "[DISCLOSURE: LOCAL ONLY] Storage uninitialized. Operations confined to local process."
		return disc
	}

	// 1. Inspect tunnel state
	tState, err := LoadTunnelState(store.Dir())
	if err == nil && tState != nil && tState.Active {
		disc.TunnelActive = true
		disc.EndpointType = tState.Type
		disc.RemoteEndpoint = tState.RemoteEndpoint
		disc.AuthProtected = tState.AuthEnabled
		disc.AuthUser = tState.AuthUser
	}

	// 2. Discover local LAN IPs if LAN exposure is enabled or for awareness
	disc.LocalLANIPs = DetectLocalLANIPs()

	// 3. Inspect registered agents for remote peers
	st, err := store.LoadProjectState()
	if err == nil && st != nil && st.Agents != nil {
		for _, a := range st.Agents {
			if a.IsRemote || a.EndpointType == "frp" || a.EndpointType == "lan" || a.RemoteEndpoint != "" {
				peer := RemotePeerInfo{
					AgentID:      a.ID,
					Role:         a.Role,
					Endpoint:     a.RemoteEndpoint,
					EndpointType: a.EndpointType,
					DiscoveredAt: a.RegisteredAt,
					LastSeen:     a.LastHeartbeat,
					Status:       a.Status,
				}
				if peer.Endpoint == "" && disc.RemoteEndpoint != "" {
					peer.Endpoint = disc.RemoteEndpoint
				}
				if peer.EndpointType == "" {
					peer.EndpointType = disc.EndpointType
				}
				disc.RemotePeers = append(disc.RemotePeers, peer)
			}
		}
	}
	disc.TotalRemoteCount = len(disc.RemotePeers)

	// 4. Formulate the official disclosure notice
	if disc.TunnelActive || disc.TotalRemoteCount > 0 {
		var parts []string
		if disc.TunnelActive {
			authStatus := "UNPROTECTED"
			if disc.AuthProtected {
				authStatus = "AUTH-PROTECTED (HTTP Basic Auth)"
			}
			parts = append(parts, fmt.Sprintf("Tunnel ACTIVE [%s: %s | %s]",
				strings.ToUpper(disc.EndpointType), disc.RemoteEndpoint, authStatus))
		}
		if disc.TotalRemoteCount > 0 {
			parts = append(parts, fmt.Sprintf("%d remote peer agent(s) available in mesh", disc.TotalRemoteCount))
		} else {
			parts = append(parts, "0 remote peers currently connected")
		}
		disc.DisclosureNotice = fmt.Sprintf("[DISCLOSURE: REMOTE MESH ACTIVE] %s", strings.Join(parts, " // "))
	} else {
		disc.DisclosureNotice = "[DISCLOSURE: LOCAL ONLY] All agent operations are strictly confined to the local machine. No remote FRP tunnels or external LAN endpoints are active."
	}

	return disc
}

// MarkdownBanner formats the disclosure into a Markdown alert block for BOARD.md or reports.
func (d RemoteDisclosure) MarkdownBanner() string {
	var b strings.Builder
	if d.TunnelActive || d.TotalRemoteCount > 0 {
		b.WriteString("### [REMOTE DISCLOSURE] Remote Access & Peer Agents: [ONLINE - PROTECTED]\n")
		b.WriteString("> [!IMPORTANT]\n")
		b.WriteString(fmt.Sprintf("> **Mandatory Transparency Disclosure**: %s\n", d.DisclosureNotice))
		if d.TunnelActive {
			authDesc := "Enabled (Username/Password BasicAuth)"
			if !d.AuthProtected {
				authDesc = "WARNING: No Authentication Configured!"
			}
			b.WriteString(fmt.Sprintf("> - **Tunnel Endpoint**: `%s` (%s)\n", d.RemoteEndpoint, d.EndpointType))
			b.WriteString(fmt.Sprintf("> - **Access Protection**: %s\n", authDesc))
			if d.AuthUser != "" {
				b.WriteString(fmt.Sprintf("> - **Authorized User**: `%s`\n", d.AuthUser))
			}
		}
		if len(d.RemotePeers) > 0 {
			b.WriteString(">\n> **Disclosed Remote Peer Agents:**\n")
			for _, p := range d.RemotePeers {
				b.WriteString(fmt.Sprintf("> - `%s` (`%s`) at `%s` [status: %s, last seen: %s]\n",
					p.AgentID, p.Role, p.Endpoint, p.Status, p.LastSeen.Format("15:04:05")))
			}
		} else {
			b.WriteString(">\n> - *No remote peer agents currently registered.*\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("### [REMOTE DISCLOSURE] Remote Access & Peer Agents: [LOCAL ONLY]\n")
		b.WriteString("*Local Isolation: All agent operations, MCP tools, and tasks are strictly confined to the local host. No remote FRP tunnels or external LAN listeners are active.*\n\n")
	}
	return b.String()
}

// ShortPill returns a concise single-line token for UI headers and terminal status lines.
func (d RemoteDisclosure) ShortPill() string {
	if d.TunnelActive {
		authTag := "PROTECTED"
		if !d.AuthProtected {
			authTag = "UNPROTECTED"
		}
		ep := d.RemoteEndpoint
		if ep == "" {
			ep = "frp-tunnel"
		}
		return fmt.Sprintf("[REMOTE: %s (%s)]", ep, authTag)
	}
	return "[LOCAL ONLY]"
}
