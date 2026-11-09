# Install Gossip in OpenCode

1. Install the precompiled binary with the [platform setup wizard](README.md), create/connect the project in the local dashboard, and save its credential.
2. Add a local MCP server through OpenCode's MCP setup wizard, or merge an entry like this into the global OpenCode config:

```json
{
  "mcp": {
    "gossip": {
      "type": "local",
      "command": ["/absolute/path/to/gossip", "coord-mcp", "--credential-file", "/private/path/to/project-credential.json"],
      "enabled": true
    }
  }
}
```

3. Install the skill at the OpenCode skill directory supported by your version, restart, and check `opencode mcp list`.

Preserve other config entries when editing JSON. The old legacy `serve --dir` command is not the project-scoped MCP server.
