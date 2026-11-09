# Install Gossip in Claude Desktop

1. Install the precompiled release for your platform using the [setup guide](README.md).
2. Create or connect a project in `gossip coord-ui` and save its credential file privately.
3. Merge this server entry into Claude Desktop's MCP configuration, preserving other servers:

```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip",
      "args": ["coord-mcp", "--credential-file", "/private/path/to/project-credential.json"]
    }
  }
}
```

4. Install the skill in Claude Desktop's supported skill mechanism for your version and restart the app.

The wizard does not currently edit Claude Desktop's JSON config. See [`examples/mcp-clients/claude-desktop/claude_desktop_config.json`](../../examples/mcp-clients/claude-desktop/claude_desktop_config.json) for a reference template.
