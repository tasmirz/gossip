# QA Audit — 02: Projects View & Onboarding Modals

**Scope:** `#view-projects` (`On this device`, `Joined projects`) and project onboarding/sharing modals (`#local-project-dialog`, `#share-project-dialog`).

## Findings

### PROJ-01 — `Create Local Project` dialog lacks an initial footer / Cancel button and retains stale preview state on reopen
- **Status:** Resolved & Verified
- **Severity:** High (Modal Consistency & UX)
- **Location:** `pkg/coord/ui/index.html` (lines 1014–1026), `pkg/coord/ui/js/projects.js` (lines 365–465)
- **Symptom:** Observed directly in `Create Local Project` modal at `1440x900`: before inspecting a checkout path, only the path field, `Browse`, and `Preview checkout` appear, with no bottom `.modal-foot` `Cancel` button (only top-right `×`). Additionally, closing the modal without submitting leaves `#checkout-status`, `#checkout-preview`, or `#checkout-details` visible if reopened.
- **UX Impact:** Asymmetric modal anatomy compared to other dialogs and confusing stale preview state after dismissal.
- **Planned Fix:** Add `#checkout-initial-foot` (`.modal-foot` with `Cancel`) shown while `#checkout-details` is hidden, toggle its visibility when `#checkout-details.hidden` changes, and reset form/preview/details/status on `#local-project-dialog` `close`.

### PROJ-02 — `Share project` dialog lacks `.modal-foot` Cancel button and uses mismatched `.view-switch` tabs
- **Status:** Resolved & Verified
- **Severity:** Medium (Visual & Modal Consistency)
- **Location:** `pkg/coord/ui/index.html` (lines 1058–1085)
- **Symptom:** Observed directly in `Share project` modal at `1440x900`: the submit button (`Create invitation`) stretches full-width at the bottom without a `.modal-foot` `Cancel` action, while the `Invite` / `Requests` toggle uses `.view-switch` buttons instead of `.subtabs` pill styling used elsewhere.
- **UX Impact:** Visual inconsistency between dialogs and primary views, plus no bottom `Cancel` affordance.
- **Planned Fix:** Preserve the existing `.view-switch` segmented pill styling (styled in `components.css` and driven by `aria-pressed` in `projects.js`) for `#share-invite-tab` and `#share-requests-tab`, and wrap `Cancel` (`btn-secondary`, `data-close-dialog`) + `Create invitation` (`btn-primary`, `type="submit"`) inside `.modal-foot`.

### PROJ-03 — Remote joined project cards omit quick `Plans` action button
- **Status:** Resolved & Verified
- **Severity:** Low (Feature Parity)
- **Location:** `pkg/coord/ui/js/projects.js` (`renderRemoteProjectCard`, lines 153–171)
- **Symptom:** Local checkout cards offer `Chat`, `Plans`, and `To-dos` quick-open actions, whereas joined remote project cards only offer `Chat` and `To-dos`.
- **UX Impact:** Unnecessary extra click to reach `Plans` on a joined remote project.
- **Planned Fix:** Add a `Plans` quick-open button calling `openRemoteProject(rp, 'plans')` between `Chat` and `To-dos`.
