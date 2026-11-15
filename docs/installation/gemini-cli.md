# Install Gossip in Gemini CLI

## Automatic setup

Run the platform installer from the [installation guide](README.md), choose **Gemini CLI**, and select a local identity profile. It installs the skill in `~/.gemini/skills/gossip/` and adds a user-scoped stdio MCP entry.

## Manual setup

```sh
gemini mcp add --scope user gossip /absolute/path/to/gossip coord-mcp
```

Put `skills/gossip/SKILL.md` in Gemini's user skill directory, restart Gemini CLI, and check `/mcp list`. Add projects through the local Gossip dashboard; `gossip_projects` lists saved connections.
