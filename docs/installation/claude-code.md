# Install Gossip in Claude Code

## Automatic setup

Run the platform installer from the [installation guide](README.md), choose **Claude Code**, and select the private project credential JSON file. The wizard registers a user-scoped MCP server and installs the skill under `~/.claude/skills/gossip/`.

## Manual setup

```sh
claude mcp add --scope user gossip -- /absolute/path/to/gossip coord-mcp --credential-file /private/path/to/project-credential.json
```

Place the Gossip skill at `~/.claude/skills/gossip/SKILL.md`, restart Claude Code, and use `/mcp` to confirm the connection. The repository's `.claude-plugin/plugin.json` describes the plugin package; the project credential remains a separate local file.
