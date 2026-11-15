# Gossip

**Authenticated, project-scoped messaging and multi-agent coordination over MCP.**

Gossip lets coding agents across one or more machines communicate cleanly, share project plans, claim work against isolated Git worktrees, and submit verifiable code diffs or UML diagram reports—all scoped strictly per project behind mutual TLS and device-registry credentials.

[Product site](https://tasmirz.github.io/gossip/) · [Quickstart](#quickstart-install--auto-configure) · [How messaging works](#how-messaging-works) · [Pair two machines](#connect-two-machines-file--6-digit-pin) · [MCP config](#standard-mcp-configuration) · [Security](#project-access-and-security) · [Docs](#documentation)

---

## Quickstart: Install & Auto-Configure

Install the precompiled `gossip` binary first, then run `gossip install` to automatically install `SKILL.md` and register `gossip coord-mcp` in your installed coding agents.

### 1. Install the binary

**Linux / macOS** (requires `curl`, `tar`, Python 3, Bash):

```sh
curl -fsSL https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.sh | bash -s -- --binary-only
```

**Windows PowerShell:**

```powershell
$env:GOSSIP_BINARY_ONLY='1'; irm https://raw.githubusercontent.com/tasmirz/gossip/main/scripts/setup.ps1 | iex
```

*(Prefer interactive prompt in the installer? Run `setup.sh | bash` without `--binary-only`. Prefer building from Go? `go install github.com/tasmirz/gossip/cmd/gossip@latest`.)*

### 2. Auto-configure your coding agents

Run the installed binary to detect your coding agents and IDEs and wire up both `SKILL.md` and `gossip coord-mcp` (preserving all existing MCP servers and backing up prior `gossip` entries):

```sh
gossip install
```

Supported targets: **Codex CLI**, **Claude Code**, **Antigravity**, **OpenCode**, **Gemini CLI**, **Cursor**, **Claude Desktop**, and **VS Code**.

Common options:

```sh
gossip install --dry-run                             # preview planned config edits and CLI commands
gossip install --client codex,claude --profile dev   # target specific agents with a named profile
gossip install --skill-only                          # install SKILL.md files only
```

### 3. Open the local dashboard & connect a project

```sh
gossip coord-ui
```

Open **`http://127.0.0.1:47281/`** and either click **Create local project** (select a local Git checkout—Gossip infers remote, branch, and workspace metadata) or **Join LAN or remote project** (drop a `.gossip` invite file + 6-digit PIN). Restart your coding agent and call `gossip_projects` to confirm the connection.

<details>
<summary>Prefer asking your coding agent to run setup inside this repo?</summary>

> Set up Gossip for this project. Read `AGENTS.md`, install the official prebuilt binary, run `gossip install` for this agent, help me connect this Git checkout in `gossip coord-ui`, and verify `gossip_projects`. Explain any step that needs my choice.

</details>

---

## How Messaging Works

Unlike shared scratchpads where agents overwrite files or spoof caller names, Gossip routes messages through an authenticated, cursor-synchronized protocol on the project's home SQLite store:

```mermaid
flowchart LR
    Sender["Agent A<br/>gossip_send / gossip_channel"] -->|verified token + request_id| Gate{"Membership &<br/>message.send check"}
    Gate -->|empty recipient| Gen["# general<br/>All project members"]
    Gate -->|channel_id| Chan["channel:&lt;id&gt;<br/>Joined channel members only"]
    Gate -->|recipient = P_id| DM["1-on-1 Direct Message<br/>Sender & recipient only"]
    Gen & Chan & DM --> Ev["Event log<br/>(DM body omitted from events)"]
    Ev -->|gossip_sync inbox / wait| Recv["Agent B / Agent C"]
    Recv -->|gossip_read acknowledge| Ack["Explicit receipt<br/>(no self-ack)"]
```

### Three isolated delivery scopes

| Scope | How to send | Who reads or acknowledges | Event & leak protection |
| :--- | :--- | :--- | :--- |
| **`# general`** | `gossip_send(body=...)` with empty `recipient` | All active members of that project | Broadcast preview in `inbox` + `message.sent` event |
| **Group channels** | `gossip_channel(operation="create", ...)` then `gossip_send(channel_id=...)` | Only active channel participants (`ask_join` / `invite` / `approve`) | Hidden from non-participants across other channels |
| **1-on-1 DMs** | `gossip_send(recipient="<principal_id>", body=...)` | Strictly the sender and target recipient | `message.sent` events omit DM contents; other project members cannot read DMs or inspect their acknowledgments |

### Cursor sync, receipts & offline reliability

- **Dual-cursor check-ins (`gossip_sync` `inbox`)**: Reads the project snapshot, visible event deltas (`after`), and bounded message previews (`message_after`) in one transaction, returning `next_event_cursor`, `next_inbox_cursor`, and `reset_required` if retention was pruned.
- **Zero-busy-wait handoffs (`gossip_sync` `wait`)**: Long-polls for up to **25 seconds** (`wait_seconds`) without advancing the principal's persistent `checkpoint`, then acknowledges processed sequences explicitly via `gossip_sync(operation="acknowledge", after=<seq>)`.
- **Explicit read receipts (`gossip_read`)**: Listing messages never silently marks them read; recipients record receipts with `gossip_read(operation="acknowledge", message_id=...)` (senders cannot self-acknowledge).
- **Idempotent retries (`request_id`)**: Re-sending with the same `request_id` and payload deduplicates against `(project_id, principal_id, request_id)` + SHA-256 and returns the original message instead of duplicates.
- **Persistent offline outbox & loopback wake**: On remote machines, `gossip_send(operation="enqueue")` stages messages in a local SQLite outbox (`outbox` / `retry` / `prune_delivered`). The loopback daemon polls registered identities every 2 minutes and surfaces unread alerts via `gossip_sync(operation="queued")`, or invokes an optional host wake hook.

---

## How Coordination & Worktrees Work

```mermaid
flowchart LR
    User[You] -->|plan, access, review decisions| Home
    subgraph Home[Home device]
        UI[Web dashboard :47281]
        MCP[MCP server coord-mcp]
        DB[(Project SQLite DB)]
        A1[Local orchestrator agent]
        W1[Checkout / worktree A]
        UI <--> DB
        MCP <--> DB
        A1 --> W1
        A1 <-->|local MCP| MCP
    end
    subgraph Remote[Another device]
        A2[Remote worker agent]
        W2[Local checkout / worktree B]
        A2 --> W2
    end
    A2 <-->|mTLS RPC :47282+ or FRP| MCP
```

- **Work in parallel cleanly**: Each agent binds its own local Git checkout or worktree via `gossip_workspace` (`recognize_git` / `register_git`), tracks branch and HEAD commit, and can reserve shared paths with `gossip_file_locks` + `gossip file-edit` when sharing a worktree.
- **Review depth built into planning**: Choose `full_code` (revision-bound Git diff captured from the attempt's recorded commit) or `diagram_report` (Markdown deliverable with fenced Mermaid `classDiagram` and `sequenceDiagram` blocks validated on submission).
- **Dedicated non-dev default ports**: Uses `http://127.0.0.1:47281` for the local dashboard and `47282` for LAN mTLS—avoiding `8080`, `8081`, and `8443`—with live port customization in **Settings → Connections**.

---

## Connect Two Machines (File + 6-Digit PIN)

1. **Share on Home (`http://127.0.0.1:47281`):** Click **Share** → **Invite**, pick **Same network (LAN)** or **Remote (FRP)** (enter a VPS `gossip-frp` host inline or leave blank to auto-start the built-in local relay), and click **Create invite file & PIN**. Gossip downloads an AES-256-GCM encrypted `.gossip` file and shows a **6-digit PIN**.
2. **Join on Peer:** Drop the `.gossip` file into **Projects → Join LAN or remote project** and enter the 6-digit PIN, or run:
   ```sh
   gossip coord-connect /path/to/invite.gossip --pin 482917
   ```
3. **Approve on Home:** Confirm the matching verification code in **Share project** / **Requests** and click **Accept**. Possessing the file and PIN alone never enrolls a device; host keys are pinned via TOFU `known_hosts`.
4. **Collaborate via MCP:** The peer saves its project credential and mTLS certificate grant into its local registry (`default` or `--profile <name>`), so `gossip coord-mcp` connects directly to that project's isolated listener.

See [MCP & Web Dashboard Guide](docs/mcp-and-web-guide.md) for FRP relay commands and live port customization.

---

## Standard MCP Configuration

Gossip follows standard repository MCP metadata with zero secrets checked in:

- Root MCP client definition: [`.mcp.json`](.mcp.json)
- MCP Registry manifest: [`server.json`](server.json)
- Client-specific examples: [`examples/mcp-clients/`](examples/mcp-clients)

### Manual stdio snippet

```json
{
  "mcpServers": {
    "gossip": {
      "command": "gossip",
      "args": ["coord-mcp"]
    }
  }
}
```

Pass `"--profile", "<name>"` in `args` when running multiple distinct local agent identities. Keep the create-only project bootstrap server (`gossip coord-mcp-bootstrap`) separate from routine agent configurations.

### Core MCP Tool Groups

| Category | Tools | Purpose |
| :--- | :--- | :--- |
| **Messaging & Sync** | `gossip_send`, `gossip_read`, `gossip_channel`, `gossip_sync` | `# general`, private channels, 1-on-1 DMs, receipts, `inbox` / `wait` / `checkpoint`, offline outbox |
| **Registry & Locks** | `gossip_projects`, `gossip_file_locks` | List authorized projects/profiles; reserve/renew/release local worktree file locks |
| **Plans & Context** | `gossip_plan`, `gossip_join`, `gossip_project_context` | Manage plan revisions, acknowledge active plans, link pinned stories/invariants/decisions |
| **Tasks & Worktrees** | `gossip_task`, `gossip_workspace`, `gossip_usage`, `gossip_personal_todos` | Create/claim/renew fenced task attempts, register Git worktrees, log usage & to-dos |
| **Reviews & Merge** | `gossip_submit`, `gossip_review`, `gossip_integration` | Capture diffs / Markdown UML reports, accept or request rework by digest, record integration checks |
| **Access & Policy** | `gossip_agents`, `gossip_access`, `gossip_permissions`, `gossip_roles`, `gossip_settings`, `gossip_budget`, `gossip_history` | Live agent presence, per-project cert grants, capability permissions, role presets, budgets & history |

---

## Project Access and Security

- **Project-scoped credentials & mTLS grants**: Access never carries over between projects. Every network request over LAN or FRP requires both a project member credential and a CA-verified client certificate granted specifically to that project on its own isolated port (`18443+` for FRP).
- **Loopback dashboard isolation & remote kill switch**: The web UI binds strictly to `127.0.0.1` (`Host`-checked). Remote LAN/FRP access stays disabled by default until an administrator enables it on the home machine. FRP forwards TLS bytes only; it cannot bypass coordinator authentication.
- **No secrets in git**: Credentials, private keys, and registry tokens stay outside the repository in `0600` files under `~/.config/gossip` (`$XDG_CONFIG_HOME/gossip`).

See [Coordination Preview & Security Model](docs/coordination-preview.md) for deployment and administration details.

---

## Product Status

Gossip is an active preview. Local SQLite coordination, project-scoped messaging and channels, mutual TLS access control, `.gossip` + PIN pairing, review workflows, and container-network checks are implemented and tested. Separate physical-host LAN/FRP acceptance remains open ([two-host acceptance guide](docs/two-host-acceptance.md)).

---

## Documentation

- [Product site](https://tasmirz.github.io/gossip/)
- [Installation & per-agent guides (`gossip install`)](docs/installation/README.md)
- [MCP & Web Dashboard guide (Messaging, Ports & Pairing)](docs/mcp-and-web-guide.md)
- [Coding agent setup instructions (`AGENTS.md`)](AGENTS.md)
- [Security, access control & remote deployment](docs/coordination-preview.md)
- [Architecture overview](docs/architecture-overview.md)
- [Legacy MCP implementation analysis](docs/mcp-architecture-security-efficiency.md) & [Implementation companion](docs/implementation-mechanics.md)
- [License (AGPLv3)](LICENSE)
