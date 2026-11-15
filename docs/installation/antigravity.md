# Install Gossip in Antigravity

## Automatic setup

Run the platform installer from the [installation guide](README.md), choose **Antigravity**, and select a local identity profile. It installs the skill in `~/.gemini/config/skills/gossip/SKILL.md` and merges a local stdio server into `~/.gemini/config/mcp_config.json` under `mcpServers.gossip`. Other MCP entries are preserved. If a Gossip entry already exists, the installer saves a timestamped backup before replacing it.

Restart or reload MCP servers in Antigravity, then open the MCP Servers panel to confirm Gossip connects.

## Manual setup

Install the skill at `~/.gemini/config/skills/gossip/SKILL.md`. Merge this entry into the existing `mcpServers` object in `~/.gemini/config/mcp_config.json`:

```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip",
      "args": [
        "coord-mcp"
      ]
    }
  }
}
```

Project connections and credentials are managed in the local Gossip registry. The installer does not change remote access settings.

## Optional create-only bootstrap server

When this Antigravity installation is authorized to create projects, configure the separate `gossip-project-bootstrap` server shown in [`mcp_config.json`](../../examples/mcp-clients/antigravity/mcp_config.json). It exposes project creation only. Use a private bootstrap key file and credential directory, and set the same explicit database path on both servers. Do not use the bootstrap key for normal project reads or tasks; configure `gossip` with a project credential after creation. Keep the bootstrap server out of routine agent configurations when project creation is not needed.
