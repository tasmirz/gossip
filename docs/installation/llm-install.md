# LLM Installation & Multi-Agent Setup Guide

This guide is written for **AI coding agents and LLMs** asked to install and configure Gossip (`https://github.com/tasmirz/gossip`) on a developer's machine across all supported coding agents while keeping credentials private and existing configs intact.

---

## Copyable Prompt for Any Coding Agent

Give this prompt to an LLM or coding agent on any machine:

```text
Install and configure Gossip from https://github.com/tasmirz/gossip using the LLM setup guide at https://github.com/tasmirz/gossip/blob/main/docs/installation/llm-install.md and agent config reference at https://github.com/tasmirz/gossip/blob/main/docs/installation/agent-configs.md.

1. Check if `gossip` is already installed (`command -v gossip`). If absent, install the official prebuilt binary using `--binary-only` (Linux/macOS: `curl -fsSL https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.sh | bash -s -- --binary-only`, Windows: `$env:GOSSIP_BINARY_ONLY='1'; irm https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.ps1 | iex`).
2. Preview first with `gossip install --dry-run`, then run `gossip install` (or `gossip install --client <agent> --profile <name>` if scoped) to install `SKILL.md` and register `gossip coord-mcp` across detected coding agents while preserving existing MCP servers.
3. If no project is connected yet, start `gossip coord-ui` on `http://127.0.0.1:47281` and help me connect this Git checkout (or join with a `.gossip` file + 6-digit PIN). Keep remote LAN/FRP access disabled and never print or commit private credentials or keys.
4. Verify MCP initialization (`gossip_projects`) and report the connected project name, Git remote/branch, and configured clients.
```

---

## 1. Execution Sequence for Agents

Follow these steps in order and explain any step requiring user approval (such as opening the loopback dashboard or approving a peer connection).

### Step 1 — Pre-flight checks

Run read-only inspection first:

```sh
# Check OS/arch and existing binary
uname -s -m
command -v gossip || true
gossip version 2>/dev/null || true

# Check if currently inside a Git checkout
git rev-parse --show-toplevel 2>/dev/null || true
git remote -v 2>/dev/null || true
git branch --show-current 2>/dev/null || true
```

### Step 2 — Install the precompiled binary (skip Go compilation unless requested)

- **Linux / macOS**:
  ```sh
  curl -fsSL https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.sh | bash -s -- --binary-only
  ```
  Installs `gossip` (and `gossip-frp` when present in the release archive) into `${XDG_DATA_HOME:-$HOME/.local/share}/gossip/bin/gossip` and symlinks `~/.local/bin/gossip` with SHA-256 checksum verification.
- **Windows (PowerShell)**:
  ```powershell
  $env:GOSSIP_BINARY_ONLY='1'; irm https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.ps1 | iex
  ```
  Installs `gossip.exe` (and `gossip-frp.exe` when present) into `%LOCALAPPDATA%\Gossip\bin`.
- **Go source fallback** (only if release binaries are unavailable for the platform or user requests source build):
  ```sh
  go install github.com/tasmirz/gossip/cmd/gossip@latest
  ```

### Step 3 — Auto-configure coding agents with `gossip install`

Always preview before modifying user agent configs:

```sh
gossip install --dry-run
```

Then run one of:

```sh
# Configure all detected agents/IDEs with default profile
gossip install

# Configure specific clients and/or a distinct local identity profile
gossip install --client codex,claude,antigravity --profile worker-1

# Install SKILL.md files only without touching MCP configs
gossip install --skill-only
```

**Supported `gossip install` flags:**

| Flag | Default | Purpose |
| :--- | :--- | :--- |
| `--all` | `true` | Auto-detect and configure all present agents/editors (ignored if `--client` is passed) |
| `--client` | *(empty)* | Comma-separated targets: `codex,claude,antigravity,opencode,gemini,cursor,claude-desktop,vscode` |
| `--profile` | `default` | Local Gossip identity profile passed to `gossip coord-mcp --profile <name>` |
| `--bin-dir` | `~/.local/bin` (Unix) / `%LOCALAPPDATA%\Programs\gossip` (Win) | Directory where `gossip install` ensures an executable copy resides |
| `--bin-path` | *(current binary)* | Override executable path written into generated MCP configs |
| `--skill-only` | `false` | Write `SKILL.md` only; leave MCP configs untouched |
| `--dry-run` | `false` | Print planned edits and CLI calls without writing any files |

---

## 2. Complete Coding-Agent Support Matrix

`gossip install` handles **8 coding agents / editors** with atomic JSON/JSONC writes (`0600` file permissions), timestamped config backups before replacing an existing `gossip` entry, and zero disruption to unrelated MCP servers.

| Client (`--client`) | Aliases | Auto-Detection Signal | Skill Path (`SKILL.md`) | MCP Registration Target & Format |
| :--- | :--- | :--- | :--- | :--- |
| **`codex`** | `codex-cli` | `codex` on `PATH` or `${CODEX_HOME:-~/.codex}` dir | `~/.agents/skills/gossip/SKILL.md` | Runs `codex mcp add gossip -- <bin> coord-mcp --profile <profile>` → `${CODEX_HOME:-~/.codex}/config.toml` (`[mcp_servers.gossip]`) |
| **`claude`** | `claude-code` | `claude` on `PATH` or `~/.claude` dir | `~/.claude/skills/gossip/SKILL.md` | Runs `claude mcp add --scope user gossip -- <bin> coord-mcp --profile <profile>` |
| **`antigravity`** | `agy` | `${GEMINI_CONFIG_DIR:-~/.gemini/config}` dir or `mcp_config.json` | `${GEMINI_CONFIG_DIR:-~/.gemini/config}/skills/gossip/SKILL.md` | Merges `mcpServers.gossip` into `${GEMINI_CONFIG_DIR:-~/.gemini/config}/mcp_config.json` |
| **`opencode`** | — | `opencode` on `PATH` or `${XDG_CONFIG_HOME:-~/.config}/opencode` dir | `${XDG_CONFIG_HOME:-~/.config}/opencode/skills/gossip/SKILL.md` | Parses JSON/JSONC & merges `mcp.servers.gossip` (`"type": "local"`, `"command": [...]`) into `${XDG_CONFIG_HOME:-~/.config}/opencode/opencode.json` |
| **`gemini`** | `gemini-cli` | `gemini` on `PATH` or `~/.gemini` dir | `~/.gemini/skills/gossip/SKILL.md` | Runs `gemini mcp add --scope user gossip <bin> coord-mcp --profile <profile>` |
| **`cursor`** | — | `~/.cursor` dir | `~/.cursor/skills/gossip/SKILL.md` | Merges `mcpServers.gossip` into `~/.cursor/mcp.json` |
| **`claude-desktop`** | — | Existing `claude_desktop_config.json` | *(MCP config only; see note below)* | Merges `mcpServers.gossip` into OS config path:<br>• Linux: `~/.config/Claude/claude_desktop_config.json`<br>• macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`<br>• Windows: `%APPDATA%\Claude\claude_desktop_config.json` |
| **`vscode`** | `vs-code` | `.vscode/` dir in current working directory | *(Workspace-scoped MCP; repo has `skills/gossip/SKILL.md`)* | Merges `servers.gossip` into `.vscode/mcp.json` |

For human-readable configuration snippets for every client (plus `--profile` and `gossip-project-bootstrap`), see [`agent-configs.md`](agent-configs.md).

---

## 3. Connecting a Project (Local vs. Remote Peer)

### Local Project (Same Machine)

1. Start the local dashboard:
   ```sh
   gossip coord-ui
   ```
   *(Default URL: `http://127.0.0.1:47281`. Avoids `8080`/`8081`.)*
2. Have the user open `http://127.0.0.1:47281/`, click **Create local project**, and select the Git repository root.
3. Gossip creates the SQLite project, infers repository remote & branch, creates the initial workspace, and stores the owner credential in the local device registry (`~/.config/gossip`).
4. If migrating an existing JSON credential file manually, import it without moving/exposing it:
   ```sh
   gossip coord-connection add --credential-file /private/path/member.json [--profile <name>]
   ```

### Remote Machine (LAN mTLS or Isolated FRP Tunnel)

- An agent **cannot** open host firewall/LAN/FRP listeners or decrypt `.gossip` invites automatically without user action.
- On Home (Machine A): Owner clicks **Share → Invite** in `coord-ui`, picks **Same network (LAN)** (`47282` base mTLS port) or **Remote (FRP)** (VPS relay or built-in local relay), downloads the encrypted `.gossip` file, and shares the **6-digit PIN**.
- On Peer (Machine B): Drop the `.gossip` file in `coord-ui` → **Join LAN or remote project**, or run:
  ```sh
  gossip coord-connect /path/to/invite.gossip --pin <6-DIGIT-PIN> [--profile <name>]
  ```
- On Home (Machine A): Confirm matching verification code and click **Accept**.

See [`../mcp-and-web-guide.md`](../mcp-and-web-guide.md) for custom ports (`GOSSIP_DASHBOARD_PORT`, **Settings → Connections**), VPS `gossip-frp` setup, and the messaging protocol.

---

## 4. Verification & Troubleshooting

1. Verify saved device registry connections:
   ```sh
   gossip coord-connection list
   ```
2. Restart the coding agent(s) so they reload MCP configs and skills.
3. Inside the restarted agent, call `gossip_projects` and confirm:
   - The expected project ID/name appears under the active `--profile`.
   - Repository metadata and branch match the local Git checkout.
   - Optionally inspect `# general` / inbox via `gossip_sync(operation="inbox")`.

**Common pitfalls:**
- **Agent CLI not on `PATH`**: When targeting `--client codex`, `--client claude`, or `--client gemini` explicitly, `gossip install` requires that CLI on `PATH` (use `--skill-only` or omit `--client` to skip unavailable CLIs while configuring the rest).
- **Antigravity custom config dir**: Set `GEMINI_CONFIG_DIR` if the user keeps Antigravity configs outside `~/.gemini/config`.
- **Multiple agents on one host**: Give each agent its own `--profile <name>` and separate enrolled principal credential so messages, file locks, and task claims stay attributed accurately.

---

## 5. Non-Negotiable Safety Rules for Agents

- **Never copy, print, log, or commit** `.gossip` invite files, private `.key` files, tokens, or credential JSON files into source files, Git commits, or chat output.
- **Keep unrelated MCP servers intact**: Use `gossip install` rather than overwriting config files blindly.
- **Keep `coord-mcp-bootstrap` separate**: Never add `gossip-project-bootstrap` (`--bootstrap-api-key-file`) into routine project configurations unless the user specifically asks for automated MCP project creation.
- **Leave remote access off by default**: Only the human administrator enables LAN/FRP access on the home machine.
