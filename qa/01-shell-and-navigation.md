# QA Audit — 01: Shell, Navigation & Shared Layout

**Scope:** Left navigation rail (`#app-rail`), active project dropdown (`#project-switcher-btn` / `#project-dropdown`), shared view headers/tablist bars (`.view-header`, `.subtabs`), shared `.scope-id-pill` identity pills, and route state cleanup across `1440x900`, `1100x900`, and `390x844`.

## Findings

### SHELL-01 — Project switcher dropdown lacks vertical keyboard navigation
- **Status:** Resolved & Verified
- **Severity:** Medium (Accessibility & Keyboard UX)
- **Location:** `pkg/coord/ui/app.js` (`wireShellEvents`, lines 1515–1527)
- **Symptom:** Clicking `#project-switcher-btn` opens `#project-dropdown`, and pressing `Escape` dismisses it, but `ArrowDown`, `ArrowUp`, `Home`, and `End` keys do not move focus across project items or `+ New project`.
- **UX Impact:** Keyboard-only users cannot switch projects efficiently via menu arrow conventions.
- **Planned Fix:** Handle `ArrowDown`, `ArrowUp`, `Home`, and `End` keydown events inside `#project-dropdown` over visible `[role="menuitem"]` elements, wrapping cleanly.

### SHELL-02 — Top `.subtabs` bars lack consistent WAI-ARIA `tablist` arrow navigation
- **Status:** Resolved & Verified
- **Severity:** Medium (Accessibility & Consistency)
- **Location:** `pkg/coord/ui/app.js`, `pkg/coord/ui/js/settings.js` (lines 535–549)
- **Symptom:** Only `#settings-tabs` handles `ArrowLeft`/`ArrowRight`/`Home`/`End`; equivalent `[role="tablist"]` bars (`#projects-tabs`, `#plan-view-tabs`, `#todo-scope-tabs`, `#agents-view-tabs`, `#search-mode-tabs`) do not support arrow-key switching.
- **UX Impact:** Keyboard behavior differs inconsistently across primary pages.
- **Planned Fix:** Introduce a shared `wireTablistKeyboard(selector)` helper in `app.js` covering all six `[role="tablist"]` bars and remove the duplicate `#settings-tabs` listener in `settings.js`.

### SHELL-03 — Leaving `Chat` leaves transient Chat panels open and offers no `previousView` return target
- **Status:** Resolved & Verified
- **Severity:** Low (State Hygiene & Navigation)
- **Location:** `pkg/coord/ui/app.js` (`state` lines 158–165, `setView` lines 1202–1211)
- **Symptom:** Switching away from `chat` does not remember `state.previousView`, nor does returning guarantee transient secondary panels (`#agent-dm-panel`, `#chat-search-results`) were dismissed cleanly.
- **UX Impact:** Search cannot easily offer a contextual Back button, and returning to Chat can expose stale inspection panels.
- **Planned Fix:** Track `state.previousView` (default `'chat'`) whenever transitioning from any non-`search` view into `search`, and ensure leaving `chat` invokes `closeAgentDMs()` and `closeChatSearch()`.

### SHELL-04 — `.scope-id-pill` wraps ID text and drops `Copy ID` onto a second line
- **Status:** Resolved & Verified
- **Severity:** High (Visual Defect)
- **Location:** `pkg/coord/ui/css/components.css` (lines 401–416)
- **Symptom:** Observed directly in `Settings` on mobile (`390x844`) and in `Agents -> Identity & usage` (`1100x900`): `.scope-id-pill` has `flex-wrap: wrap`, causing long `proj_...` / `principal_...` IDs to break across lines and push the `Copy ID` button below the code block.
- **UX Impact:** Wastes vertical space and visually detaches the copy button from the ID string.
- **Planned Fix:** Set `flex-wrap: nowrap; min-width: 0; max-width: 100%;` on `.scope-id-pill`, apply `min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;` to `.scope-id-pill code`, and `flex: none;` on `.scope-id-pill .btn`.

### SHELL-05 — Project switch (`selectProject`) does not close newly added dialogs or reset exported recovery output
- **Status:** Resolved & Verified
- **Severity:** Medium (State Isolation)
- **Location:** `pkg/coord/ui/app.js` (`selectProject`, lines 483–491)
- **Symptom:** Switching projects leaves newer dialogs (`#plan-dialog`, `#plan-walkthrough-dialog`, `#context-dialog`, `#work-roles-section`) open if triggered, and does not re-hide `#copy-recovery-btn` when `#recovery-output` is cleared.
- **UX Impact:** Risk of viewing stale modal content from the prior project after switching active project.
- **Planned Fix:** Include all four dialogs in `selectProject`'s close loop and hide `#copy-recovery-btn` alongside clearing `#recovery-output`.
