# Install Gossip for your coding agent

The setup wizard downloads the precompiled Gossip binary for your OS and CPU, installs the portable skill, and can add the project MCP server to Codex, Claude Code, or Gemini CLI. It does not compile source. It asks you to select the agent and supply a project credential file; no credential is embedded in the install command.

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

1. Run the wizard and select the agent whose MCP configuration should be updated.
2. If you already have a project credential, choose its private JSON file when prompted.
3. If this is a new project, leave the credential path blank. Start `gossip coord-ui`, open `http://127.0.0.1:8081`, choose **Create local project**, and connect the existing checkout.
4. Run the setup command again and select the downloaded project credential. The wizard installs the skill for that agent and adds a local stdio MCP configuration.
5. Restart the coding agent and verify Gossip is listed as connected.

The wizard's automatic MCP registration currently supports Codex CLI, Claude Code, and Gemini CLI. Other clients can use the manual guides below. The project access guide covers adding LAN/FRP agents.

## Agent tutorials

- [Codex](codex.md)
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
