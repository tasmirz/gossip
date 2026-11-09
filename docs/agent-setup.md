# Set up Gossip through your coding agent

Use this guide when you want an AI coding agent to connect a repository and coordinate work through Gossip. The root [`AGENTS.md`](../AGENTS.md) tells repo-aware coding agents when and how to follow this setup.

## Prompt to give your agent

> Set up Gossip for this project. Read `AGENTS.md`, install or build the local coordinator if needed, help me connect this Git folder as a project, and configure this agent with the project-scoped MCP credential. Explain any step that needs my choice or access.

## What the agent should do

1. Detect this checkout's Git remote, branch, head revision, and dirty state.
2. Check for the Gossip coordinator and MCP support; build or install Gossip only if absent or outdated.
3. Start the local dashboard and guide you to **Create local project** with this checkout. Gossip infers repository metadata and creates the project and initial workspace together.
4. Save the owner credential to a private, untracked file. Keep it out of prompts, logs, source control, and shared workspace directories.
5. Configure the coding agent's regular Gossip MCP server to use the project database and owner/member credential file. Keep the create-only bootstrap server separate and remove its key from routine agent configuration after project creation where possible.
6. Restart or refresh the MCP client, verify a project-scoped read, and tell you the project name, repository, and branch it connected.
7. To add another agent, create a separate project credential and, for LAN/FRP, grant that machine's client certificate specifically to this project. Use a separate checkout or Git worktree for each worker.

## User choices and security

- The owner chooses whether the orchestrator delegates work or lets agents work independently.
- During planning, the orchestrator asks for the worker's review deliverable if not already selected: full source review, or a Markdown report with UML class and sequence diagrams.
- A project credential grants membership; role and action permissions govern operations. Network agents also need a project-specific client certificate grant.
- Remote access is disabled by default. Only the home machine's local administrator can enable it.
- FRP forwards transport to the TLS home coordinator. It does not grant project access.
- Do not expose the legacy JSON dashboard through LAN, reverse proxy, or FRP.

Detailed command examples and recovery steps are in [`coordination-preview.md`](coordination-preview.md). The architecture is in [`mcp-architecture-security-efficiency.md`](mcp-architecture-security-efficiency.md).
