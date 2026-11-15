# Dashboard fixes from the four screenshots

Scope this work to the Gossip dashboard. The screenshots were taken with a populated sample project selected. Use an existing populated project to reproduce the layout with existing data and use a disposable project for write tests.

## 1. Plans · tabs, revision list, and a readable plan

- Replace the two stacked revision dropdowns with **Plan** and **Project context** tabs and a revision list beside the plan reader. Use the Chat layout as the interaction reference. On phones, show the revision list and the reader as separate panes with a Back button.
- Use the first Markdown heading as the revision label when available. Render Markdown in a readable column, with author, revision, and publication date above it. Keep comparison inside the reader and show it when there is a second revision.
- Label the current revision **Active**, explicitly completed revisions **Done**, and replaced unfinished revisions **Superseded**. Store completion against the project and revision. Require plan-edit permission and recheck the active revision when marking it done. Keep completion separate from each member’s acknowledgment.
- With no published plan, show one empty state and a **Create plan** action for editors. Keep stories, invariants, decisions, and revision links accessible under Project context.
- Verify empty, active, done, and superseded states, reload persistence, read-only access, keyboard tab navigation, and phone Back behavior.

## 2. To-dos · project and agent scopes with Checklist and Kanban

- Put **Project**, **My work**, and **Browse agent** above one work area. The selected scope controls both layouts. In agent scope, show the agent’s assigned project tasks and personal checklist separately. Open the matching scope directly from the agent profile.
- Show visible checkmarks in Checklist. An agent can check and uncheck its personal items. Shared tasks show completion when the coordinator accepts their report. Route shared-task actions through the existing claim, submission, review, and replanning operations.
- In project Kanban, show Ready, In progress, In review, and Done, with Blocked and Needs replan visible. In personal Kanban, show To do, Doing, and Done. Use the same records in both layouts. Keep compact filters and task creation beside the work area.
- Verify cross-layout updates, permission checks for other agents’ checklists, failed transitions, empty lanes, and project switches with a request in flight.

## 3. Agents · roster and profile

- Replace the repeated owner and worker summaries and full-width role form with one roster and a focused profile. Show name, project role, and presence as the first line. Put technical IDs, reported capabilities, and usage under Details. Preserve explicit hierarchy where stored and keep identities distinct.
- Give each profile clear **Message**, **To-dos**, and **Assign work role** actions according to permissions. Open the role form from the selected profile. Keep agent-to-agent DM inspection accessible to owners with read-only labeling and audit evidence.
- Verify keyboard selection, long agent names, offline presence, profile-to-To-dos links, authorized role assignment, and the mobile list/profile Back path.

## 4. Settings and shared header · fit the controls to the available space

- Fix the cramped 1120px header. Keep the project and view labels together, give search one SVG magnifying-glass button with an accessible name, and keep **Share project** easy to find. Move recovery export to Access and audit events to History. Put secondary shell actions in one menu when space is limited.
- Use consistent SVGs for icon controls, including navigation, search, Back, and close. Give icon-only controls visible focus and accessible labels. Keep the project switcher keyboard operable, preserve the current Settings section when switching projects, and clear stale project data.
- Replace the raw project ID banner with a compact project header and place the copyable ID in Details. Keep Access, Connections, Workspaces, and History distinct. In Access, make Members, Roles, Requests, and Devices easy to locate. Keep LAN and FRP credentials in their relevant flows.
- Verify at 1440, 1120, 768, and 390px. Check that every action stays reachable, text wraps without clipping, the settings section survives project changes, and local access opens without credentials.

## Delivery checks

Fix the shared header first, then Plans, To-dos, and Agents. Use fixture data with multiple plan revisions, two agents, shared tasks in several states, and personal items. Check keyboard and phone flows in Chromium, run the relevant Go tests and the full suite, then rebuild and restart the development dashboard with `bash scripts/build.sh --restart`. Verify the served version at `127.0.0.1:8081`.
