# QA Audit — 03: Chat View, Channels & Agent Conversations

**Scope:** `#view-chat` conversation rail, channel bar (`#channel-bar`), `#channel-dialog` ("Create Group Chat"), and Agent-to-Agent DM inspector (`#inspect-agent-dms` / `#agent-dm-panel`).

## Findings

### CHAT-01 — Agent-to-agent DMs icon button lacks active styling and does not toggle closed on second click
- **Status:** Resolved & Verified
- **Severity:** Medium (Interactive State & Visual Feedback)
- **Location:** `pkg/coord/ui/css/components.css` (lines 166–173), `pkg/coord/ui/js/chat.js` (`renderChatView`, lines 462–474)
- **Symptom:** `#inspect-agent-dms` sets `aria-pressed="true"` while `#agent-dm-panel` is open, but `.icon-btn` defines no `[aria-pressed="true"]` visual highlight, and clicking `#inspect-agent-dms` again reloads the panel instead of toggling `#agent-dm-panel` closed.
- **UX Impact:** Users cannot tell at a glance whether DM inspection is active from the header icon and must hunt for the inner close button instead of toggling the trigger.
- **Planned Fix:** Add `.icon-btn[aria-pressed="true"]` accent highlight styling in `components.css`, and update `#inspect-agent-dms.onclick` in `renderChatView` to call `closeAgentDMs()` + `renderChatView()` when `$('#agent-dm-panel')` is already visible.

### CHAT-02 — Group channel invite `<select>` stretches uncompacted without `aria-label`
- **Status:** Resolved & Verified
- **Severity:** Low (Layout Polish & Accessibility)
- **Location:** `pkg/coord/ui/js/chat.js` (`renderChannelBar`, lines 186–205)
- **Symptom:** Inside custom group channels, the agent invite `<select>` uses bare `.input` (full-width flex growth) and lacks an explicit `aria-label`.
- **UX Impact:** Overly wide select control in compact channel bars and reduced screen-reader clarity.
- **Planned Fix:** Add `select-compact` class and `aria-label="Invite agent to channel"` to the `<select>` element in `renderChannelBar`.

### CHAT-03 — `#channel-members-picks` has no max-height scroll container in Create Group Chat modal
- **Status:** Resolved & Verified
- **Severity:** Low (Scalability Guard)
- **Location:** `pkg/coord/ui/css/views.css`
- **Symptom:** Inside `#channel-dialog`, `#channel-members-picks` grows indefinitely with member count rather than scrolling cleanly within the modal body.
- **UX Impact:** Projects with many agents can push modal action buttons offscreen.
- **Planned Fix:** Add `max-height: 12rem; overflow-y: auto; padding: 0.25rem 0;` styling for `#channel-members-picks` in `views.css`.
