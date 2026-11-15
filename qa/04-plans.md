# QA Audit — 04: Plans View & Project Context

**Scope:** `#view-plans` (`Plan` / `Project context` subtabs), plan list & reader (`#plan-reader-back`, `#plan-variants`, revision actions), and plan/context modals (`#context-dialog`).

## Findings

### PLAN-01 — Mobile `Plans` reader hides back button label via `.icon-only-control`
- **Status:** Resolved & Verified
- **Severity:** High (Mobile Navigation Clarity)
- **Location:** `pkg/coord/ui/index.html` (line 331)
- **Symptom:** Observed directly at `390x844` in `step_594_shot_4_qa_plans_mobile_390.png`: `#plan-reader-back` includes `.icon-only-control`, which hides the `<span>Back to plans</span>` text and leaves a lone small `←` icon floating top-left inside the reader card.
- **UX Impact:** Ambiguous mobile back navigation inside the plan detail view.
- **Planned Fix:** Remove `.icon-only-control` from `#plan-reader-back` so `← Back to plans` remains readable at compact/mobile widths.

### PLAN-02 — `Variants` disclosure gives no count preview until expanded
- **Status:** Resolved & Verified
- **Severity:** Low (Information Scent)
- **Location:** `pkg/coord/ui/index.html` (lines 356–364), `pkg/coord/ui/js/plans.js` (lines 287–332)
- **Symptom:** Observed directly in `Plans`: the collapsed summary reads plain `▸ Variants`, forcing users to open it only to discover `"No variants for this revision."`.
- **UX Impact:** Unnecessary clicks to check whether alternative plan variants exist.
- **Planned Fix:** Add `<span id="plan-variants-count" class="count-pill">0</span>` inside the `Variants` `<summary>` and update it in `renderPlanVariants` on reset and load.

### PLAN-03 — `Project context` pin revision form lacks compact styling/Cancel, and record headers stack loosely
- **Status:** Resolved & Verified
- **Severity:** Medium (Layout & Form UX)
- **Location:** `pkg/coord/ui/js/plans.js` (`renderProjectContext` lines 56–97, `renderContextTarget` lines 155–162)
- **Symptom:** `renderProjectContext` stacks the context record title, type/revision/status badges, and `Edit record` button in multiple vertical rows rather than a crisp horizontal `.list-item-row`, and `renderContextTarget`'s inline `Pin revision` form uses full-width `.input` `<select>` without a `Cancel` button to back out.
- **UX Impact:** Bloated vertical density in Project context and no clean cancel path from inline pinning.
- **Planned Fix:** Wrap each context record header in `.list-item-row` (title + `.btn-row` badges on left, `btn-xs btn-secondary` `Edit record` button on right), and update `renderContextTarget` to use `.select-compact`, `btn-sm btn-primary` `Pin revision`, plus a `Cancel` (`btn-sm btn-ghost`) restoring the trigger button.

### PLAN-04 — `Project context` dialog renders bare full-width submit button without Cancel footer
- **Status:** Resolved & Verified
- **Severity:** Medium (Modal Consistency)
- **Location:** `pkg/coord/ui/index.html` (lines 1037–1041)
- **Symptom:** Observed directly at `1440x900` (`step_594_shot_1_qa_context_dialog.png`): `Publish revision` renders as a lone full-width button at the bottom of `#context-dialog` without a `.modal-foot` `Cancel` button.
- **UX Impact:** Inconsistent modal controls and missing bottom Cancel button.
- **Planned Fix:** Wrap `Cancel` (`btn-secondary`, `data-close-dialog`) and `Publish revision` (`btn-primary`, `type="submit"`) inside `.modal-foot`.
