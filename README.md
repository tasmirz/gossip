# Gossip

Gossip is a project-scoped coordination service and MCP adapter for coding agents working toward a shared plan across separate Git worktrees. It provides task delegation, direct agent communication, review handoffs, workspace baselines, and a project-filtered web dashboard.

> **Preview software.** The coordination service has project credentials and permissions, but managed agent execution and cost enforcement are not implemented. Installers deliberately defer editor MCP configuration until a project credential exists. See [the redesign status](docs/gossip-redesign-plan.md) before using it for remote work.

## What it supports

- One authoritative home coordinator per project, with agents connecting locally, over a LAN, or through an FRP TCP tunnel.
- Project membership credentials plus per-project client-certificate grants for network access. FRP forwards transport; it does not authorize access.
- Local-folder-first setup. The dashboard inspects a Git checkout, infers its repository metadata, and creates the project and first workspace together.
- Separate workspaces, branches, and checkouts for parallel agents, with recorded Git baselines and owner-private local paths.
- Project-filtered views for tasks, agents, conversations, activity, workspaces, settings, and history.
- Planning-time selection of either a full-code review or a Markdown report with UML class and sequence diagrams. Task creation requires a choice. Workers can request the planned review from the orchestrator.
- Optional role and capability requirements for task claims. Capability labels are reports; host-reported capabilities are not proof that an action is executable.

## Start locally

Build the CLI:

```sh
go build -o bin/gossip ./cmd/gossip
```

Start the dashboard:

```sh
./bin/gossip coord-ui --db /private/path/gossip/coord.db
```

Open [http://127.0.0.1:8081](http://127.0.0.1:8081), choose **Create local project**, and paste the full path to a Git checkout in **Connect a code folder**. Gossip infers the repository name and current Git state, creates the project and workspace, and downloads the owner credential. Keep that credential outside the repository and private.

In each MCP client, configure `coord-mcp` with that project's credential file. The checked-in [MCP examples](mcp.json) also include a separate, optional create-only bootstrap server. Generate its private API key with:

```sh
./bin/gossip coord-bootstrap-key --db /private/path/gossip/coord.db --key-file /private/path/gossip/bootstrap.key
```

For local creation, `--db` stores only a SHA-256 digest in that coordinator database; the MCP server checks the key against it before creating a project. Use the same database path with `coord-mcp-bootstrap` (or `--socket` for a daemon using that database). For remote creation, generate the key without `--db`, configure the home with `coord-serve --bootstrap-api-key-file`, and use a CA-verified TLS client certificate. In both cases `coord-mcp-bootstrap` requires `--bootstrap-api-key-file` and `--credential-dir`; its only tool creates projects from Git folders, saves credentials privately, and does not return the token to the model. See [coordination setup](docs/coordination-preview.md).

## LAN and remote access

Run the home with `coord-serve` on a TLS listener. It requires mutual TLS and a trusted client CA. Grant each client certificate access to each project separately, and give each user or agent that project's own member credential. A client needs both forms of access for every network request. Owners can expire or revoke certificate grants and member credentials independently.

Remote access is **disabled by default**. Before exposing the home over a LAN or FRP, enable it locally with `gossip coord-remote --action enable --db /private/path/coord.db`. Check or disable it at any time with `--action status` or `--action disable`. If the local daemon holds the database open, use `--socket /private/path/coord.sock` instead. The switch blocks new RPC and remote dashboard requests immediately; FRP TCP forwarding is covered by the same RPC gate. Only the local database or private Unix socket can change this setting.

The dashboard's **Settings** view shows the current switch state. It is read-only; change the setting with the local CLI or private socket so an unauthenticated localhost HTTP request cannot toggle the host-wide boundary.

For FRP, forward TCP to the TLS listener. Do not expose the legacy HTTP dashboard or use the retired arbitrary-peer registration tools. The remote model is single-home: replicas may cache bounded read snapshots and queue idempotent actions, while the home remains authoritative.

See [LAN, FRP, certificate grants, pairing, and bootstrap instructions](docs/coordination-preview.md) for command examples and details.

## Agent review workflow

During planning, choose one review deliverable:

1. **Full code review:** the worker captures a diff from its registered worktree. The review request pins the candidate digest and baseline so the orchestrator can retrieve the exact source in pages.
2. **Diagram report:** the worker submits Markdown with UML class and sequence diagrams, findings, evidence, coverage, and limitations.

If the review type is unspecified, ask the user during planning. The UI and service reject task creation without an explicit selection. A review request notifies the orchestrator; it does not launch a reviewer or establish that the result is correct.

## Access model

Project credentials identify enrolled principals. Project roles and explicit action grants authorize coordination operations. TLS client certificates establish transport identity and require an additional grant scoped to the requested project. Git-remote discovery can suggest among projects already connected to the dashboard; it never grants membership.

The legacy JSON MCP CLI transport and schema exporter are retired. The older local CLI and dashboard remain outside the project membership boundary; use `coord-mcp` for multi-agent project access. Installers defer editor MCP configuration until a project credential exists. Existing JSON files under `mcp/` are historical legacy exports, not the supported `coord-mcp` tool contract.

## Current implementation status

Local project setup, project access controls, task/review contracts, the dashboard, and backend remote TLS handoffs have implementation and test coverage. The redesign is still in progress:

- Full P2 workflows have not yet been accepted between genuinely separate physical machines.
- Managed agent launch, interruption, and trusted host capability discovery remain open.
- Usage and cost records are self-reported, not provider-verified or hard spending limits.
- Role presets and token/latency/cost savings still need representative evaluations.
- The legacy local JSON store remains outside the project permission model and should not be used for multi-agent access control.

See the phase-by-phase [redesign plan](docs/gossip-redesign-plan.md) and [coordination preview guide](docs/coordination-preview.md).

## Development checks

```sh
go test ./...
go test -race ./...
go vet ./...
```

## License

[AGPL-3.0](LICENSE)
