# Gossip dashboard walkthrough for Luna

This walkthrough covers the current local dashboard: creating a project from a checkout, moving between work views, using chat, and managing project access. It assumes Gossip is already installed or built.

## Open the dashboard

Start the local dashboard from a terminal:

```sh
gossip coord-ui
```

Open `http://127.0.0.1:8081/` in a browser. Keep the dashboard on loopback. Do not open `pkg/coord/ui/index.html` as a `file://` URL: the screenshot in the issue is the browser rendering the HTML without its server-hosted CSS and API. The dashboard needs Gossip's HTTP server.

For a source checkout, use:

```sh
go run ./cmd/gossip coord-ui
```

The default coordinator database and device registry are used unless you pass `--db` or set `XDG_CONFIG_HOME`.

## Create a local project

1. Select **Create local project**.
2. Enter the full path to an existing Git checkout on the machine running Gossip. A project name is optional; Gossip uses the repository name by default.
3. Select **Connect folder**. Gossip inspects the repository and creates the project, owner membership, repository, and initial workspace.
4. The credential is saved in the device registry. It is not included in the create response or downloaded automatically. Project owners can use **Export recovery credential** in the header when they need a separate backup.

If the response is lost, retry the same setup without changing the form. Gossip reuses the stable setup request ID while the form remains unchanged.

## Work with the project

- **Overview** shows the current plan and task counts. Select **Edit plan** to publish or revise the shared Markdown plan.
- **Work** lists tasks. Select **New task** to create work from the current plan; set the task description, acceptance criteria, assignee or required role, and review deliverable. Select a task to claim, assign, update, submit, or review it when your permissions allow.
- **Conversations** contains `#general` and direct messages. Choose a member in the left rail to open a DM. Search filters loaded messages; **Load older messages** fetches earlier pages. The composer stays attached to the selected room or DM.
- **Agents** shows project members, presence, duties, permissions, and project access controls. A work role describes duties; it does not grant permissions.
- **Workspaces** lists repository hints and registered checkouts. A checkout path is private to its owner.
- **Activity** shows recent project events and the audit history dialog.
- **Settings** contains project limits and owner access controls. Remote access stays disabled until enabled on the home machine.
- **History** browses imported legacy records as read-only history.

Project switching stays in the sidebar. Each page loads its own data. Chat room and DM history are filtered on the server before cursor pagination; DM content is available only to its participants.

## Connect an MCP agent

Configure the agent host once with the normal Gossip MCP command:

```text
/absolute/path/to/gossip coord-mcp
```

The device registry under `~/.config/gossip` (or `$XDG_CONFIG_HOME/gossip`) stores authorized project connections and credentials. Import an existing project credential without deleting its source file:

```sh
gossip coord-connection add --credential-file /private/path/project-credential.json --profile luna
```

Use `gossip coord-connection list` to inspect saved connection metadata. The `gossip_projects` tool lists projects available to the current MCP profile. Include a stable `project` reference and `profile` on a call when several connections could match. A profile never falls back to the device owner's identity.

## Current limits

This is a walkthrough of the implemented dashboard, not the full redesign proposal. The dashboard does not yet provide joined remote projects, offline task reconciliation, agent parent/child trees, profile-owned checklists, or Graphify search. The chat search filters messages already loaded; use older-message pagination to search further back.

For network boundaries and peer setup, see [Coordination preview](coordination-preview.md). For MCP installation, see the [installation guide](installation/README.md).
