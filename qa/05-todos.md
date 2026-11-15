# QA Audit — 05: To-dos View, Kanban & Task Modals

**Scope:** `#view-todos` (`All tasks`, `Plan goals`, `Global goals`, `My work`, `Browse agent`), Checklist & Kanban boards, personal checklists, and `#personal-edit-dialog`.

## Findings

### TODO-01 — Kanban board renders empty exception lanes and wraps active `Needs replan` column off-screen
- **Status:** Resolved & Verified
- **Severity:** High (Layout Wrapping & Visibility)
- **Location:** `pkg/coord/ui/js/todos.js` (lines 247–262), `pkg/coord/ui/css/views.css` (lines 1051–1056)
- **Symptom:** Observed directly at `1440x900` (`step_596_shot_1_qa_todos_kanban_1440.png`) and `1100x900` (`step_596_shot_2_qa_todos_kanban_1100.png`): `renderTodosView` unconditionally outputs 6 columns (`Ready`, `In progress`, `In review`, `Done`, `Blocked`, `Needs replan`) at `minmax(15rem, 1fr)`. Even with 0 `Blocked` tasks, 6 x `15rem` exceeds the `~1096px` card inner width at `1440x900`, forcing both `Blocked · 0` and the active `Needs replan · 1` column onto a second row below the fold (only 4 columns visible above the fold at `1440px`, 3 at `1100px`).
- **UX Impact:** Users miss active `Needs replan` / `Blocked` tasks unless they scroll past the full height of `Ready`, while empty `Blocked · 0` wastes grid capacity.
- **Planned Fix:** Always render the 4 core workflow lanes (`Ready`, `In progress`, `In review`, `Done`) and only append `Blocked`, `Needs replan`, or `Other` when `tasks.length > 0`; update `#view-todos .todo-board` column minimum width in `views.css` to `minmax(12.25rem, 1fr)` so 5 active lanes fit cleanly in a single row at `1440x900` (verified across all 5 active lanes at `1440x900`).

### TODO-02 — Personal checklist rows stack controls vertically instead of aligning in `.list-item-row`
- **Status:** Resolved & Verified
- **Severity:** Medium (Layout Density)
- **Location:** `pkg/coord/ui/js/todos.js` (`renderPersonalChecklists`, lines 110–167)
- **Symptom:** Personal to-do cards stack the item title, status `<select>`, and `Edit` button in vertical block flow with a full-width `<select>`.
- **UX Impact:** Unnecessarily tall personal checklist cards and awkward wide status dropdowns.
- **Planned Fix:** Lay out the header with `.list-item-row` placing title + meta on the left and a horizontal `.btn-row` (`Edit` `btn-xs btn-secondary` + compact status `<select class="input select-compact">` or read-only status badge) on the right.

### TODO-03 — `Edit Personal To-do` modal omits `.modal-foot` and `Cancel` button
- **Status:** Resolved & Verified
- **Severity:** Medium (Modal Consistency)
- **Location:** `pkg/coord/ui/index.html` (lines 1049–1053)
- **Symptom:** `#personal-edit-dialog` shows `Save to-do` as an unaligned button with no `Cancel` button in `.modal-foot`.
- **UX Impact:** Inconsistent modal controls compared with other dialogs.
- **Planned Fix:** Place `Cancel` (`btn-secondary`, `data-close-dialog`) and `Save to-do` (`btn-primary`, `type="submit"`) inside `.modal-foot`.
