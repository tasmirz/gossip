# Install Gossip in Cursor

1. Install the precompiled Gossip binary with the [platform setup wizard](README.md), or download the matching asset from GitHub Releases.
2. Create or connect the project in the loopback dashboard and save the project credential privately.
3. Open Cursor MCP settings and add a stdio server. The project-level `.cursor/mcp.json` example is in [`examples/mcp-clients/cursor/mcp.json`](../../examples/mcp-clients/cursor/mcp.json). Set `command` to the binary's absolute path and set args to `coord-mcp`, `--credential-file`, and the absolute credential path.
4. Copy `skills/gossip/SKILL.md` to `.cursor/skills/gossip/SKILL.md` in the project.
5. Restart Cursor and verify Gossip tools are available.

Keep the credential file outside the checkout. See [security and access](../coordination-preview.md) before configuring a remote connection.
