# Install Gossip in Gemini CLI

## Automatic setup

Run the platform installer from the [installation guide](README.md), choose **Gemini CLI**, and select the private project credential JSON file. It installs the skill in `~/.gemini/skills/gossip/` and adds a user-scoped stdio MCP entry.

## Manual setup

```sh
gemini mcp add --scope user gossip /absolute/path/to/gossip coord-mcp --credential-file /private/path/to/project-credential.json
```

Put `skills/gossip/SKILL.md` in Gemini's user skill directory, restart Gemini CLI, and check `/mcp list`. If the local project does not exist, run `gossip coord-ui`, choose **Create local project**, and save its owner credential before adding the MCP server.
