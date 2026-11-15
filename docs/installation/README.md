# Install Gossip for your coding agent

The setup wizard downloads the precompiled Gossip binary for your OS and CPU, installs the portable skill, and can add the device-level Gossip MCP server to Codex, Antigravity, OpenCode, Claude Code, or Gemini CLI. It does not compile source. MCP configuration contains only the executable and an optional local identity profile; project credentials stay in Gossip's local registry.

## One-line setup

Linux and macOS (requires `curl`, `tar`, Python 3, and Bash):

```sh
curl -fsSL https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.sh | bash
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.ps1 | iex
```

The installer checks the downloaded release archive's SHA-256 against the checksum published with that same GitHub release. Review the installer source before running it if you want to inspect each action. It writes the binary under the user's application-data directory and never requires administrator privileges.

## First project setup

1. Run the wizard and select the agent whose MCP configuration should be updated. Choose `default` for a single identity, or a profile name when the device runs multiple agents.
2. Start `gossip coord-ui` and open `http://127.0.0.1:8081` to add a local project or connect a remote project. During migration, import an existing credential with `gossip coord-connection add --credential-file <private-file>`; the file is retained until you remove it.
3. Restart the coding agent. `gossip_projects` lists authorized projects and profiles; project-scoped reads verify the registry connection.

The wizard's automatic MCP registration supports Codex CLI, Antigravity, OpenCode, Claude Code, and Gemini CLI. Other clients can use the manual guides below. The project access guide covers adding LAN/FRP agents.

## Agent tutorials

- [Codex](codex.md)
- [Antigravity](antigravity.md)
- [OpenCode](opencode.md)
- [Claude Code](claude-code.md)
- [Gemini CLI](gemini-cli.md)
- [Cursor](cursor.md)
- [VS Code Copilot](vscode.md)
- [OpenCode](opencode.md)
- [Claude Desktop](claude-desktop.md)
- [Full manual install, verification, and removal](manual.md)
- [Full access and remote setup](../coordination-preview.md)
- [Current architecture](../architecture-overview.md)

## Ask your LLM to install it

Open the repository in your coding agent and ask:

> Install Gossip for this machine using the official setup guide at `https://github.com/tasmirz/gossip/blob/main/docs/installation/README.md`. Download the matching prebuilt release binary, install the Gossip skill for this agent, and configure only this agent's MCP client with the private credential file I select. Preserve existing agent configuration. If I do not have a project credential, start the loopback dashboard and help me connect this local Git checkout first. Do not enable remote access or expose credentials. Verify MCP initialization and explain any action that needs my choice.

The agent should follow [`AGENTS.md`](../../AGENTS.md) when working inside this repository.
