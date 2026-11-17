# Coding-Agent MCP & Skill Configurations

Human reference for configuring **every supported coding agent and IDE** with Gossip.

If you already installed the `gossip` binary, you can skip manual editing and let Gossip write these for you automatically:

```sh
gossip install           # auto-detect & configure all installed agents
gossip install --dry-run # preview changes without touching files
```

Use [`examples/mcp-clients/`](../../examples/mcp-clients) for raw checked-in JSON templates, or [`llm-install.md`](llm-install.md) if you want an LLM to handle setup for you.

---

## Summary Table (All 8 Supported Clients)

| Client | `gossip install` flag | Skill File (`SKILL.md`) | MCP Config Path / CLI |
| :--- | :--- | :--- | :--- |
| **Codex CLI** | `--client codex` | `~/.agents/skills/gossip/SKILL.md` | `${CODEX_HOME:-~/.codex}/config.toml` (`codex mcp add`) |
| **Claude Code** | `--client claude` | `~/.claude/skills/gossip/SKILL.md` | `claude mcp add --scope user` |
| **Antigravity** | `--client antigravity` | `${GEMINI_CONFIG_DIR:-~/.gemini/config}/skills/gossip/SKILL.md` | `~/.gemini/antigravity/mcp_config.json` & `${GEMINI_CONFIG_DIR:-~/.gemini/config}/mcp_config.json` |
| **OpenCode** | `--client opencode` | `${XDG_CONFIG_HOME:-~/.config}/opencode/skills/gossip/SKILL.md` | `${XDG_CONFIG_HOME:-~/.config}/opencode/opencode.json` |
| **Gemini CLI** | `--client gemini` | `~/.gemini/skills/gossip/SKILL.md` | `gemini mcp add --scope user` |
| **Cursor** | `--client cursor` | `~/.cursor/skills/gossip/SKILL.md` | `~/.cursor/mcp.json` |
| **Claude Desktop** | `--client claude-desktop` | *(configure MCP JSON)* | Linux: `~/.config/Claude/claude_desktop_config.json`<br>macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`<br>Win: `%APPDATA%\Claude\claude_desktop_config.json` |
| **VS Code Copilot** | `--client vscode` | `skills/gossip/SKILL.md` *(in workspace)* | `.vscode/mcp.json` *(workspace root)* |

> **Rule of thumb:** Gossip uses **registry mode** (`gossip coord-mcp [--profile <name>]`), so **no API keys or secrets ever go inside your MCP config files**. Project credentials stay in `0600` files inside Gossip's private registry (`~/.config/gossip` on Linux/macOS).

---

## Standard `mcpServers` Config (Antigravity, Cursor, Claude Desktop)

Used by `~/.gemini/config/mcp_config.json` (Antigravity), `~/.cursor/mcp.json` (Cursor), and `claude_desktop_config.json` (Claude Desktop):

```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip",
      "args": ["coord-mcp"]
    }
  }
}
```

With a named local identity profile (for running multiple separate agents on the same computer):

```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip",
      "args": ["coord-mcp", "--profile", "worker-1"]
    }
  }
}
```

*(If `/absolute/path/to/gossip` is omitted and `gossip` is already on your default login `PATH`, `"command": "gossip"` also works—just as in the root [`.mcp.json`](../../.mcp.json).)*

---

## Per-Client Configurations

### 1. Codex CLI (`--client codex`)

- **Auto-configure**: `gossip install --client codex`
- **Skill path**: `~/.agents/skills/gossip/SKILL.md`
- **CLI command**:
  ```sh
  codex mcp add gossip -- /absolute/path/to/gossip coord-mcp
  ```
- **Resulting TOML in `~/.codex/config.toml`**:
  ```toml
  [mcp_servers.gossip]
  command = "/absolute/path/to/gossip"
  args = ["coord-mcp"]
  ```
- Detailed guide: [`codex.md`](codex.md)

### 2. Claude Code (`--client claude`)

- **Auto-configure**: `gossip install --client claude`
- **Skill path**: `~/.claude/skills/gossip/SKILL.md`
- **CLI command**:
  ```sh
  claude mcp add --scope user gossip -- /absolute/path/to/gossip coord-mcp
  ```
- Verify inside Claude Code with `/mcp`.
- Detailed guide: [`claude-code.md`](claude-code.md)

### 3. Antigravity (`--client antigravity`)

- **Auto-configure**: `gossip install --client antigravity`
- **Skill path**: `~/.gemini/config/skills/gossip/SKILL.md` (respects `GEMINI_CONFIG_DIR`)
- **Config file**: `~/.gemini/antigravity/mcp_config.json` (and `~/.gemini/config/mcp_config.json` when present) — uses the standard `mcpServers` JSON snippet above.
- Detailed guide: [`antigravity.md`](antigravity.md)

### 4. OpenCode (`--client opencode`)

- **Auto-configure**: `gossip install --client opencode`
- **Skill path**: `~/.config/opencode/skills/gossip/SKILL.md` (respects `XDG_CONFIG_HOME`)
- **Config file (`~/.config/opencode/opencode.json`)**: note OpenCode uses `mcp.servers` with `"type": "local"` and an array `command`:
  ```json
  {
    "$schema": "https://opencode.ai/config.json",
    "mcp": {
      "servers": {
        "gossip": {
          "type": "local",
          "command": ["/absolute/path/to/gossip", "coord-mcp"]
        }
      }
    }
  }
  ```
- Detailed guide: [`opencode.md`](opencode.md)

### 5. Gemini CLI (`--client gemini`)

- **Auto-configure**: `gossip install --client gemini`
- **Skill path**: `~/.gemini/skills/gossip/SKILL.md`
- **CLI command**:
  ```sh
  gemini mcp add --scope user gossip /absolute/path/to/gossip coord-mcp
  ```
- Detailed guide: [`gemini-cli.md`](gemini-cli.md)

### 6. Cursor (`--client cursor`)

- **Auto-configure**: `gossip install --client cursor`
- **Skill path**: `~/.cursor/skills/gossip/SKILL.md`
- **Config file (`~/.cursor/mcp.json`)**: uses the standard `mcpServers` JSON snippet above.
- Detailed guide: [`cursor.md`](cursor.md)

### 7. Claude Desktop (`--client claude-desktop`)

- **Auto-configure**: `gossip install --client claude-desktop`
- **Config files**:
  - **Linux**: `~/.config/Claude/claude_desktop_config.json`
  - **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
  - **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`
- Uses the standard `mcpServers` JSON snippet above.
- Detailed guide: [`claude-desktop.md`](claude-desktop.md)

### 8. VS Code Copilot (`--client vscode`)

- **Auto-configure**: `gossip install --client vscode` (run from the repository root containing `.vscode/`)
- **Config file (`.vscode/mcp.json`)**: note VS Code uses top-level `"servers"` instead of `"mcpServers"`:
  ```json
  {
    "servers": {
      "gossip": {
        "command": "/absolute/path/to/gossip",
        "args": ["coord-mcp"]
      }
    }
  }
  ```
- Detailed guide: [`vscode.md`](vscode.md)

---

## Optional: Separate Create-Only Bootstrap Server

If you want an agent to create brand-new projects via MCP using a create-only key (`gossip coord-bootstrap-key`), configure `gossip-project-bootstrap` **separately** from `gossip`:

```json
{
  "mcpServers": {
    "gossip": {
      "command": "/absolute/path/to/gossip",
      "args": ["coord-mcp"]
    },
    "gossip-project-bootstrap": {
      "command": "/absolute/path/to/gossip",
      "args": [
        "coord-mcp-bootstrap",
        "--bootstrap-api-key-file",
        "/private/path/bootstrap.key",
        "--credential-dir",
        "/private/path/gossip/credentials"
      ]
    }
  }
}
```

Keep `/private/path/bootstrap.key` outside Git repositories with mode `0600`, and remove `gossip-project-bootstrap` when routine project creation is not needed.

---

## Verify or Uninstall

- **Verify registry**: `gossip coord-connection list [--profile <name>]`
- **Verify MCP**: Restart your coding agent and call `gossip_projects`.
- **Remove**: Remove the `"gossip"` entry from the client config above (or run `codex mcp remove gossip` / `claude mcp remove gossip` / `gemini mcp remove gossip`) and delete the installed `SKILL.md` folder.
