# Manual installation and MCP setup

This guide covers the same setup as the wizard without running an installer. It never requires compiling Gossip. Select and download a matching prebuilt archive from [GitHub Releases](https://github.com/tasmirz/gossip/releases). Archives contain the Gossip executable, `skills/gossip/SKILL.md`, and the license.

## 1. Select and verify a release

Choose the newest release and match both OS and CPU architecture:

| System | Release asset |
| --- | --- |
| Linux x86-64 | `gossip_<version>_linux_amd64.tar.gz` |
| Linux ARM64 | `gossip_<version>_linux_arm64.tar.gz` |
| macOS Intel | `gossip_<version>_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `gossip_<version>_darwin_arm64.tar.gz` |
| Windows x86-64 | `gossip_<version>_windows_amd64.zip` |

Download the adjacent `.sha256` file and verify it before extracting:

```sh
sha256sum -c gossip_<version>_<os>_<arch>.tar.gz.sha256
```

On macOS use `shasum -a 256 -c <archive>.sha256`; on Windows compare `Get-FileHash <archive> -Algorithm SHA256` with the checksum file.

Extract the archive into a private, user-owned directory. For example, Linux/macOS can use `~/.local/share/gossip`; Windows can use `%LOCALAPPDATA%\Gossip`. Do not place credentials in the archive directory.

## 2. Install the skill

Copy `skills/gossip/SKILL.md` from the archive into the agent's personal or project skills directory—or run `gossip install --skill-only` using the installed binary. The portable Agent Skills format is a directory named after the skill with a required `SKILL.md` file. See each [agent tutorial](README.md#agent-tutorials) for the current discovery path.

## 3. Create or connect a project

Run the installed binary with `coord-ui`, for example:

```sh
~/.local/share/gossip/bin/gossip coord-ui
```

Open `http://127.0.0.1:47281`, select **Create local project**, and enter the full path to an existing checkout. Gossip infers repository metadata and creates the project and initial workspace. The owner credential is saved in the device registry; it is not downloaded automatically. Use **Export recovery credential** in the project header only when you want a separate backup. Do not open the UI's `index.html` with a `file://` URL; the dashboard requires its local HTTP server. See the [Luna dashboard walkthrough](../walkthrough-luna.md) and the [MCP & Web Dashboard guide](../mcp-and-web-guide.md) for the everyday project and File + 6-digit PIN remote-connect flow.

## 4. Add the project MCP server

Use the agent's documented CLI or configuration editor, following its dedicated tutorial. The common stdio command and arguments are:

```text
/absolute/path/to/gossip coord-mcp
```

Running `gossip install` (or the setup wizard) automatically configures Codex, Claude Code, Antigravity, OpenCode, Gemini CLI, Cursor, Claude Desktop, and VS Code while preserving existing MCP servers. The device registry under `~/.config/gossip` (or `$XDG_CONFIG_HOME/gossip`) stores project connections and credentials. Existing project credentials can be imported with `gossip coord-connection add --credential-file <private-file> [--profile <name>]`; source files are left in place.

## 5. Verify

Restart the agent client and confirm Gossip MCP reports connected. Ask it to read the selected project and report the repository and branch. Add other agents with credentials scoped to the same intended project. For LAN/FRP, enroll each client certificate with a project-specific grant and enable the home remote-access switch locally only when needed.

## Recovery and removal

- If a credential is rejected, confirm it belongs to the database passed to the dashboard and MCP command.
- If an MCP server fails to start, check its absolute executable and credential paths, then use the client MCP status command.
- To remove integration, use the agent's MCP remove command and delete the installed skill directory and binary. Removing a local credential revokes only that copy; use the project owner controls to revoke the project principal.
- Full access boundaries and remote deployment are documented in [coordination preview](../coordination-preview.md).

### Background inbox and host wake adapters

The per-user dashboard service polls registered identities at startup and every
two minutes. Registry MCP exposes pending items through `gossip_sync` with
`operation: queued`. Use `dismiss_queued` with an item ID after handling it.
Gossip leaves coordinator message acknowledgments to the agent.

To connect a host that supports waking an agent, create a `wake` directory in
Gossip's device configuration directory. For each profile, create a file named
with the lowercase SHA-256 of the UTF-8 profile name followed by `.json`.
Its contents are a JSON string containing the absolute path to an executable
on this device. Keep the file private. The executable receives a JSON object
on stdin with `batch_id`, `profile`, `home_id`, `project_id`, `principal_id`,
and `item_ids`. A zero exit status confirms delivery. Use the batch ID to
deduplicate retries. Without an adapter, the next agent check-in can read the queue.

For local development on Linux or macOS, run `bash scripts/build.sh --restart`
from the checkout. It replaces the installed executable behind
`~/.local/bin/gossip`, restarts the dashboard, and waits for its health check.
Set `GOSSIP_DEV_BIN` if your dashboard service uses a different executable path.
