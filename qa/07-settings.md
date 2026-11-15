# QA Audit — 07: Settings View (Roles, Permissions, Recovery & History)

**Scope:** `#view-settings` across `Access` (`Members`, `Roles`, `Requests`, `Devices`, `Permissions`, `Limits`, `Recovery`), `Connections`, `Workspaces`, and `History`.

## Findings

### SET-01 — Built-in project roles show disabled checkboxes and disabled `Save custom role` without guidance
- **Status:** Resolved & Verified
- **Severity:** Medium (Clarity & Discoverability)
- **Location:** `pkg/coord/ui/js/settings.js` (`loadProjectRoleEditor`, lines 111–147)
- **Symptom:** Observed directly at `1440x900` (`step_606_shot_1_qa_settings_roles.png`): selecting built-in roles (`Viewer`, `Worker`, `Reviewer`) renders disabled permission checkboxes alongside a grayed-out `Save custom role` button with no explanation that built-in role permissions are fixed.
- **UX Impact:** Users think the role editor is broken instead of realizing custom roles must be created via `+ New custom role` (or `+ New role`).
- **Planned Fix:** Hide `save` (`Save custom role`) when viewing a built-in role and display an inline helper note (`.role-readonly-note`): `"Built-in role permissions are fixed. Choose + New custom role above to define custom permissions."` vs `"Select which permissions this custom role grants."`.

### SET-02 — Fine-grained permissions list lacks active filtering, `.perm-code` styling, and source badges; `task.delegate` missing from grant select
- **Status:** Resolved & Verified
- **Severity:** Medium (Visual Hierarchy & Completeness)
- **Location:** `pkg/coord/ui/index.html` (lines 727–738), `pkg/coord/ui/js/settings.js` (lines 250–273)
- **Symptom:** Observed directly at `1440x900` (`step_606_shot_2_qa_settings_permissions.png`): `#permissions-list` displays all entries (without filtering `!p.revoked`) as plain muted text (`chatgpt -> message.send`) without code badges or grant-source context, and `#permission-name` omits `task.delegate` even though roles expose `task.delegate`.
- **UX Impact:** Hard to distinguish explicit grants from role defaults at a glance, and `task.delegate` cannot be granted individually.
- **Planned Fix:** Add `<option value="task.delegate">task.delegate</option>` alongside all existing 8 options in `#permission-name`, filter `#permissions-list` to `!p.revoked`, and render `.member-row-main` with `strong` principal name, `<code class="perm-code">`, and source badge (`Individual grant` or `p.source`).

### SET-03 — `Settings -> History` shows no inline recent audit events and never fetches `'events'`
- **Status:** Resolved & Verified
- **Severity:** High (Missing Primary Content)
- **Location:** `pkg/coord/ui/index.html` (lines 950–965), `pkg/coord/ui/app.js` (lines 1096–1103), `pkg/coord/ui/js/settings.js` (line 446)
- **Symptom:** Observed directly at `1440x900` (`step_606_shot_4_qa_settings_history.png`): `#settings-panel-history` renders only event retention/pruning controls, a top-right `Browse events` button, and `Legacy Imported Archives`. Switching to `History` does not request `'events'` in `loadSettingsRouteData`, so users have zero inline visibility of recent audit events on the page.
- **UX Impact:** Users must open a separate dialog to view audit activity even when inspecting the History tab.
- **Planned Fix:** Fetch `'events'` alongside `'retention'` and `'legacy-imports'` in `loadSettingsRouteData`, add `<div id="history-events-list" class="stack-list">` inside the `Audit history` card, and render up to 25 recent events inline via `renderSettingsOtherPanels`.

### SET-04 — Invite & Recovery credential outputs lack `Copy JSON` actions and invite input does not clear
- **Status:** Resolved & Verified
- **Severity:** Medium (Operator Workflow Ergonomics)
- **Location:** `pkg/coord/ui/index.html` (lines 840–846), `pkg/coord/ui/js/settings.js` (lines 572–578, 640–661)
- **Symptom:** `#recovery-output` and `#invite-result` show raw JSON in a `<pre>` block with no one-click copy button, and creating an invite leaves `#invite-name` populated with the prior invitee string.
- **UX Impact:** Error-prone manual text selection when transferring credentials/invites and risk of accidentally re-submitting the same invitee name.
- **Planned Fix:** Add `#copy-recovery-btn` (initially hidden alongside `#recovery-output`, revealed on export, copying `#recovery-output.textContent` with clipboard feedback), clear `#invite-name.value` on submit, and render a `Copy JSON` button above the `#invite-result` `<pre>`.

### SET-05 — Duplicate `#settings-tabs` keydown handler
- **Status:** Resolved & Verified
- **Severity:** Low (Code Quality)
- **Location:** `pkg/coord/ui/js/settings.js` (lines 535–549)
- **Symptom:** `settings.js` attaches its own arrow/Home/End listener on `#settings-tabs` instead of sharing a central shell tablist helper.
- **UX Impact:** Duplicated keyboard navigation logic across UI modules.
- **Planned Fix:** Remove the local handler from `settings.js` once `wireTablistKeyboard` is added in `app.js`.
