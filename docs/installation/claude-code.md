# Install Gossip in Claude Code

## Automatic setup

Run `gossip install --client claude` (or `gossip install` to auto-configure all detected coding agents) after installing the binary, or run the platform installer from the [installation guide](README.md) and choose **Claude Code**. It registers the user-scoped `gossip` MCP server (`coord-mcp`) and installs the skill under `~/.claude/skills/gossip/SKILL.md`.

## Manual setup

```sh
claude mcp add --scope user gossip -- /absolute/path/to/gossip coord-mcp
```

Place the Gossip skill at `~/.claude/skills/gossip/SKILL.md`, restart Claude Code, and use `/mcp` to confirm the connection.
