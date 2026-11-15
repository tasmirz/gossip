# Install Gossip for your coding agent

Install the precompiled Gossip binary first, then let the binary itself auto-configure your coding agents (`gossip install`) with the embedded `SKILL.md` and device-registry `gossip coord-mcp` server. No source compilation is required, and MCP configs store only the binary path and optional local identity profile—project credentials remain safely in Gossip's local registry.

## Two-step install & auto-configure

**1. Install the binary**

Linux and macOS (requires `curl`, `tar`, Python 3, and Bash):

```sh
curl -fsSL https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.sh | bash -s -- --binary-only
```

Or run interactively (`| bash`) to pick option `0` (auto-configure all detected agents) or a single client right inside the wizard.

Windows PowerShell:

```powershell
$env:GOSSIP_BINARY_ONLY='1'; irm https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.ps1 | iex
```

*(Or build/install from Go source with `go install github.com/tasmirz/gossip/cmd/gossip@latest`.)*

**2. Auto-configure your coding agents**

Run the installed binary to detect installed agents/IDEs and install `SKILL.md` + `gossip coord-mcp` cleanly (preserving existing MCP servers and backing up previous `gossip` entries):

```sh
gossip install
```

Supported targets: **Codex CLI**, **Claude Code**, **Antigravity**, **OpenCode**, **Gemini CLI**, **Cursor**, **Claude Desktop**, and **VS Code**.

Useful flags:
- `gossip install --dry-run` — preview exact paths and CLI commands without writing
- `gossip install --client codex,claude --profile worker-1` — target specific clients or set an identity profile
- `gossip install --skill-only` — install `SKILL.md` files only without modifying MCP configs

The installer checks the downloaded release archive's SHA-256 against the checksum published with that same GitHub release. It writes the binary under the user's application-data directory (`~/.local/share/gossip/bin` and `~/.local/bin/gossip` on Linux/macOS; `%LOCALAPPDATA%\Gossip\bin` on Windows) and never requires administrator privileges.

## First project setup

1. Run `gossip install` (or pick a target with `--client ... --profile <name>` when running multiple distinct agent identities on one machine).
2. Start `gossip coord-ui` and open `http://127.0.0.1:47281` to create a local project from an existing Git checkout or join a remote project using an encrypted `.gossip` file + 6-digit PIN. During migration, import an existing credential with `gossip coord-connection add --credential-file <private-file>`; the file is retained until you remove it.
3. Restart your coding agent. `gossip_projects` lists authorized projects and profiles; project-scoped reads verify the registry connection.

## Agent tutorials

- [Codex](codex.md)
- [Antigravity](antigravity.md)
- [OpenCode](opencode.md)
- [Claude Code](claude-code.md)
- [Gemini CLI](gemini-cli.md)
- [Cursor](cursor.md)
- [VS Code Copilot](vscode.md)
- [Claude Desktop](claude-desktop.md)
- [Full manual install, verification, and removal](manual.md)
- [MCP & Web Dashboard guide (Messaging & Pairing)](../mcp-and-web-guide.md)
- [Full access and remote setup](../coordination-preview.md)
- [Current architecture](../architecture-overview.md)

## Ask your LLM to install it

Open the repository in your coding agent and ask:

> Install Gossip for this machine using the official setup guide at `https://github.com/tasmirz/gossip/blob/main/docs/installation/README.md`. Download the matching prebuilt release binary, install the Gossip skill for this agent, and configure only this agent's MCP client with the private credential file I select. Preserve existing agent configuration. If I do not have a project credential, start the loopback dashboard and help me connect this local Git checkout first. Do not enable remote access or expose credentials. Verify MCP initialization and explain any action that needs my choice.

The agent should follow [`AGENTS.md`](../../AGENTS.md) when working inside this repository.
