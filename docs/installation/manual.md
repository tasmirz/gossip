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

Copy `skills/gossip/SKILL.md` from the archive into the agent's personal or project skills directory. The portable Agent Skills format is a directory named after the skill with a required `SKILL.md` file. See each [agent tutorial](README.md#agent-tutorials) for the current discovery path. The Codex plugin manifest lives at `.codex-plugin/plugin.json`; Claude plugin metadata is in `.claude-plugin/plugin.json`.

## 3. Create or connect a project

Run the installed binary with `coord-ui`, for example:

```sh
~/.local/share/gossip/bin/gossip coord-ui
```

Open `http://127.0.0.1:8081`, select **Create local project**, and choose the checkout. Gossip infers repository metadata and creates the project and initial workspace. Save the downloaded owner credential in a private file outside the repository. On Unix, restrict it with `chmod 600 /path/to/project-credential.json`.

## 4. Add the project MCP server

Use the agent's documented CLI or configuration editor, following its dedicated tutorial. The common stdio command and arguments are:

```text
/absolute/path/to/gossip coord-mcp --credential-file /private/path/to/project-credential.json
```

The setup wizard can add this entry to Codex, Claude Code, or Gemini CLI. Cursor, VS Code Copilot, OpenCode, and Claude Desktop have manual walkthroughs. Preserve other MCP servers and do not paste the credential into a model prompt or commit it.

## 5. Verify

Restart the agent client and confirm Gossip MCP reports connected. Ask it to read the selected project and report the repository and branch. Add other agents with credentials scoped to the same intended project. For LAN/FRP, enroll each client certificate with a project-specific grant and enable the home remote-access switch locally only when needed.

## Recovery and removal

- If a credential is rejected, confirm it belongs to the database passed to the dashboard and MCP command.
- If an MCP server fails to start, check its absolute executable and credential paths, then use the client MCP status command.
- To remove integration, use the agent's MCP remove command and delete the installed skill directory and binary. Removing a local credential revokes only that copy; use the project owner controls to revoke the project principal.
- Full access boundaries and remote deployment are documented in [coordination preview](../coordination-preview.md).
