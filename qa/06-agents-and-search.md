# QA Audit — 06: Agents Roster/Profiles & Global Search

**Scope:** `#view-agents` (Roster, profile sections, Work roles, `#work-roles-section`, `#agent-parent-dialog`) and `#view-search` (All results & Knowledge graph).

## Findings

### AGNT-01 — Mobile agent profile back button hides its text label via `.icon-only-control`
- **Status:** Resolved & Verified
- **Severity:** High (Mobile Navigation Clarity)
- **Location:** `pkg/coord/ui/js/agents-search.js` (line 148)
- **Symptom:** `#agent-profile-back` applies `icon-only-control`, stripping `"Back to roster"` visually on mobile (`390x844`) and leaving only an unlabelled arrow icon.
- **UX Impact:** Users cannot immediately tell what the small back icon inside the mobile detail panel navigates back to.
- **Planned Fix:** Remove `icon-only-control` from `#agent-profile-back` so `← Back to roster` shows clearly.

### AGNT-02 — `Work roles` section lacks a direct `+ Assign role` button
- **Status:** Resolved & Verified
- **Severity:** Medium (Feature Discoverability)
- **Location:** `pkg/coord/ui/js/agents-search.js` (`showRoles`, lines 231–235)
- **Symptom:** While viewing an agent's `Work roles` card/tab, owners/admins must open the profile `...` overflow menu to find `Assign work role`; there is no `+ Assign role` button inside the `Work roles` header itself.
- **UX Impact:** Poor discoverability when managing work roles directly inside the `Work roles` tab.
- **Planned Fix:** When `canEditSettings()` is true, append a `+ Assign role` (`btn-xs btn-secondary`) button inside `Work roles` section header that opens `#work-roles-section` pre-selected for the active agent.

### AGNT-03 — `Assign work role` and `Edit reporting line` modals lack `.modal-foot` and `Cancel` buttons
- **Status:** Resolved & Verified
- **Severity:** Medium (Modal Consistency)
- **Location:** `pkg/coord/ui/index.html` (lines 532–549)
- **Symptom:** Observed directly at `1100x900` (`step_600_shot_1_qa_agent_work_roles_modal.png`): `#work-roles-section` renders `Assign role` left-aligned with no `Cancel` button, and `#agent-parent-dialog` has the same omission (`Save hierarchy` alone).
- **UX Impact:** Inconsistent modal footers and no bottom `Cancel` affordance.
- **Planned Fix:** Wrap `Cancel` (`btn-secondary`, `data-close-dialog`) + primary submit button inside `.modal-foot` in both dialogs.

### SRCH-01 — Search view has no Back navigation button to return to the previous route
- **Status:** Resolved & Verified
- **Severity:** High (Navigation & Dead-End UX)
- **Location:** `pkg/coord/ui/index.html` (lines 553–559), `pkg/coord/ui/app.js`
- **Symptom:** Observed directly at `1100x900` (`step_602_shot_1_qa_search_view_1100.png`): submitting a query in the top search bar navigates to `#view-search` (which has no sidebar item of its own), and `#view-search > .view-header` contains only the `All results` / `Knowledge graph` tabs with no `← Back` control.
- **UX Impact:** Users must remember which sidebar page they came from rather than clicking a single Back button.
- **Planned Fix:** Add `#search-back-btn` (`← Back`) in `#view-search > .view-header` and wire it in `app.js` to return to `state.previousView || 'chat'`.
