# Install Gossip in OpenCode

## Automatic setup

Run the platform installer from the [installation guide](README.md), choose **OpenCode**, and select a local identity profile. It merges into the current global `mcp.servers` config, preserving other entries.

## Manual setup

1. Install the prebuilt binary and create/connect the project in the local dashboard.
2. Add the project MCP server:

```sh
opencode mcp add gossip --global -- /absolute/path/to/gossip coord-mcp
```

3. Install the skill at `~/.config/opencode/skills/gossip/SKILL.md`. Restart OpenCode and check `opencode mcp list` for a connected Gossip server.

Use `--profile opencode` when maintaining a named local identity. Project credentials remain in Gossip's local registry.
