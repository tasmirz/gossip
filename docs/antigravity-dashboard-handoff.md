# Handoff from Codex to Antigravity

Antigravity, I’m handing you the dashboard redesign and local access fix. The user asked for a redesign from the core. The first acceptance check is simple: start Gossip, open the local dashboard in a fresh browser, and open an existing local project without importing a credential. LAN and remote access still require project-scoped enrollment.

Use [the detailed redesign plan](dashboard-redesign-plan.md) as the implementation checklist. Keep this handoff in the repo. It has not been sent through MCP.

Start from the current worktree at the repository root. It contains extensive uncommitted work, including inbox polling, wake delivery, reservations, installers, and UI changes. Inspect the diff before editing. Preserve unrelated changes and existing project data. In particular:

- `pkg/coord/web.go` and `pkg/coord/store.go` contain my unfinished local access prototype. Look for `localDashboardOwners`, `localDashboardToken`, and `dashboardOwners`. Replace the synthetic-token path with an explicit local authority boundary. The default local profile currently enumerates owners, but this prototype has not passed authorization or migration tests. Check local project creation and recovery export when changing session behavior.
- `pkg/coord/ui/index.html`, `app.js`, and `app.css` hold the current dashboard. The screenshot failures are structural. The overview renders panels it does not fetch, and embeds the channel rail in a narrow second column. Replace the shell and route structure before polishing individual cards.
- In conversation, identify the current principal as **You** and use **Antigravity** for that agent when viewed by others. Do not rename historical principals or merge identities. Address me as Codex or ChatGPT in handoff notes.
- The installed Linux service previously passed its PID health check and the isolated service registration, reinstall, and port conflict tests. Focused inbox and file reservation tests passed before the access prototype. The latest complete Go suite result was not collected. Native sign-in and macOS/Windows installer acceptance remain unverified.

Implement the access boundary first, then the routes. Keep changes reviewable and report which checks passed, which platform checks still need a native host, and whether the installed dashboard was restarted. Do not use or log project tokens while demonstrating local access.
