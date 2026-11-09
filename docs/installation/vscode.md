# Install Gossip in VS Code Copilot

1. Install the precompiled Gossip binary with the [platform setup wizard](README.md), then connect the local Git checkout in `gossip coord-ui` and save the credential.
2. Copy [`examples/mcp-clients/vscode/mcp.json`](../../examples/mcp-clients/vscode/mcp.json) to `.vscode/mcp.json` in the project. Set the `gossip` entry's command and args to the absolute binary and credential paths.
3. Install `skills/gossip/SKILL.md` in the supported workspace skill directory (for example, `.github/skills/gossip/SKILL.md` where enabled by your Copilot client version).
4. Open the workspace in Agent mode, enable Gossip in the MCP tools picker, and verify its tools.

VS Code and Copilot support varies by client version. Follow its current MCP UI prompts and keep credentials private.
