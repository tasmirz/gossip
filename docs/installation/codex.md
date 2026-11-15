# Install Gossip in Codex

## Automatic setup

Run the platform installer from the [installation guide](README.md), choose **Codex**, and select a local identity profile. The wizard registers Gossip with the Codex CLI and installs its skill in `~/.agents/skills/gossip/`.

## Manual setup

1. Install the Gossip release binary for your OS and CPU, and note its absolute path.
2. Create a project using `gossip coord-ui` if you do not already have its private credential file.
3. Add the MCP server:

```sh
codex mcp add gossip -- /absolute/path/to/gossip coord-mcp
```

4. Install the skill directory at `~/.agents/skills/gossip/SKILL.md` and restart Codex.
5. Run `codex mcp list` and ask the agent to list the connected project.

The MCP process reads the device registry. Use `--profile codex` when you maintain a named identity; project tokens and TLS keys do not belong in MCP configuration.
