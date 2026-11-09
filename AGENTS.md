# Set up Gossip for this project

When a user asks to set up Gossip, connect this checkout, or let coding agents coordinate, do the setup work in the repository. Do not send the user off to manually edit MCP JSON when you can configure it safely.

1. Read [`docs/agent-setup.md`](docs/agent-setup.md) for the setup sequence and security constraints. Use [`README.md`](README.md) to explain Gossip in user terms.
2. Inspect the repository root, available Gossip binary, Go toolchain, and current MCP configuration. Preserve existing user configuration and unrelated MCP servers.
3. Use the official prebuilt installer at [`docs/installation/README.md`](docs/installation/README.md) unless the user asks to build from source. Start with local project setup from the existing Git checkout; let Gossip infer its remote and branch. Do not create an empty project with guessed metadata when the user has a local checkout.
4. Help the user connect or create the project using the local dashboard or the separate create-only bootstrap MCP server. Save the owner credential in a private file and verify its permissions. Never print, paste into chat, or commit credentials or private keys.
5. Configure this coding agent with the regular project-scoped MCP server using the credential file. Bootstrap credentials are create-only; use the project credential for normal reads, tasks, messages, and review workflows.
6. Verify that the MCP server initializes and can read the selected project. Report the project name, detected repository/branch, connection method, and any remaining user decision. Do not claim agents are connected until their authenticated presence is observed.
7. Ask the user to choose whether the orchestrator should work independently or delegate to other authorized agents. During task planning, ask the user's review choice when missing: full source review or a Markdown UML class-and-sequence report.

For LAN or FRP, follow the access-control instructions in [`docs/coordination-preview.md`](docs/coordination-preview.md). Keep remote access disabled until the owner explicitly enables it on the home machine. Enroll each agent with a project credential and, for network access, a certificate granted specifically to that project. Never expose the legacy JSON dashboard through a tunnel.

## Completion check

Setup is complete only when the MCP client starts successfully with the project credential, a project-scoped read succeeds, the private credential stays outside Git, and the user knows how to add another agent or revoke access. If the required local UI action or secret provisioning is not available, state exactly which step needs the user and continue all other safe setup steps.
