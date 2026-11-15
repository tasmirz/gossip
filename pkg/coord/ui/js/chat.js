'use strict';

let agentDMRequest = 0;
let chatChecking = false;
let chatLoadingOlder = false;
let agentDMBrowseState = { pair: null, agent: '', rows: [], hasMore: false, loading: false };

function activeChatDraftKey() {
  return JSON.stringify([state.project?.home_id, state.project?.project_id, state.project?.id, state.channelId, state.recipient]);
}

function conversationRoom(channel = state.channelId, recipient = state.recipient) {
  return channel ? 'channel:' + channel : recipient ? 'dm:' + recipient : 'general';
}

function seenConversation(room) {
  const key = JSON.stringify([
    'gossip-chat-seen',
    state.project?.home_id || state.dashboardAccess.home_id,
    state.project?.project_id,
    state.project?.id,
    room
  ]);
  if (!state.chatSeen.has(key)) {
    let sequence = 0;
    try { sequence = Number(localStorage.getItem(key)) || 0; } catch {}
    state.chatSeen.set(key, sequence);
  }
  return { key, sequence: state.chatSeen.get(key) };
}

function roomHasUnread(room) {
  return (state.chatActivity.get(room) || 0) > seenConversation(room).sequence;
}

function renderConversationBadges() {
  for (const button of $$('#chat-channels [data-room]')) {
    const room = button.dataset.room;
    const unread = roomHasUnread(room);
    button.classList.toggle('has-unread', unread);
    const indicator = button.querySelector('.unread-indicator');
    if (indicator) indicator.hidden = !unread;
  }
  const channelsUnread = roomHasUnread(conversationRoom('', '')) || state.channels.some(ch => roomHasUnread(conversationRoom(ch.id, '')));
  const chanDot = $('#chat-tab-channels .unread-indicator');
  if (chanDot) chanDot.hidden = !channelsUnread;
  const peers = state.agents.filter(a => !a.revoked && (!state.project || a.id !== state.project.id));
  const dmsUnread = peers.some(a => roomHasUnread(conversationRoom('', a.id)));
  const dmDot = $('#chat-tab-dms .unread-indicator');
  if (dmDot) dmDot.hidden = !dmsUnread;
}

function markVisibleConversationRead() {
  const timeline = $('#chat-timeline');
  if (!state.project || state.view !== 'chat' || !state.chatLoaded || !timeline?.clientHeight || document.hidden) return;
  if (timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight > 32) return;
  const { key, sequence } = seenConversation(conversationRoom());
  const last = Math.max(sequence, state.chatFetchedSequence);
  if (last > sequence) {
    state.chatSeen.set(key, last);
    try { localStorage.setItem(key, String(last)); } catch {}
    renderConversationBadges();
  }
  state.chatUnseen = 0;
  if ($('#chat-jump-latest-btn')) $('#chat-jump-latest-btn').hidden = true;
}

async function openConversation(channelId, recipient) {
  saveCurrentDraft();
  closeAgentDMs();
  closeChatSearch();
  if ($('#header-search-input')) $('#header-search-input').value = '';
  state.channelId = channelId;
  state.recipient = recipient;
  state.chatTab = recipient && !channelId ? 'dms' : 'channels';
  state.mobilePane = 'thread';
  state.messages = [];
  state.messagesHasOlder = false;
  state.messagesOldestSeq = 0;
  state.chatLoaded = false;
  state.chatUnseen = 0;
  syncUrlState();
  const generation = ++state.generation;
  render();
  try {
    await loadChatMessages(generation);
    if (generation === state.generation) render();
  } catch (error) {
    if (generation === state.generation) showError(error.message);
  }
}

function selectedGroupChannel() {
  if (!state.channelId) return null;
  return state.channels.find(c => c.id === state.channelId) || null;
}

function openChannelDialog(preselectedIds = []) {
  if ($('#channel-name')) $('#channel-name').value = '';
  if ($('#channel-topic')) $('#channel-topic').value = '';
  if ($('#channel-auto-suffix')) $('#channel-auto-suffix').checked = true;
  if ($('#channel-announce')) $('#channel-announce').checked = true;
  const picks = $('#channel-members-picks');
  if (picks) {
    const wanted = new Set(Array.isArray(preselectedIds) ? preselectedIds : []);
    const candidates = state.agents.filter(a => !a.revoked && (!state.project || a.id !== state.project.id));
    renderList(picks, candidates, 'No other active agents to pre-enroll yet.', a => {
      const label = createEl('label', 'field field-checkbox');
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.value = a.id;
      cb.checked = wanted.has(a.id);
      label.append(cb, createEl('span', '', `@ ${a.name} (${a.role || 'worker'})`));
      return label;
    });
  }
  openDialog('channel-dialog');
}

async function runChannelAction(payload, nextChannelId) {
  await request(projectApi('channels'), { method: 'POST', body: payload });
  if (nextChannelId !== undefined) {
    state.channelId = nextChannelId;
    if (nextChannelId) state.recipient = '';
    syncUrlState();
  }
  await refresh();
}

function renderChannelBar(bar, ch) {
  if (!bar) return;
  if (!ch) {
    bar.hidden = true;
    clearNode(bar);
    return;
  }
  bar.hidden = false;
  clearNode(bar);
  const myId = state.project?.id || '';
  const isOwner = state.project?.role === 'owner';
  const canManage = isOwner || ch.created_by === myId;
  const members = Array.isArray(ch.members) ? ch.members : [];
  const joined = members.filter(m => m.status === 'joined');
  const pending = members.filter(m => m.status === 'requested');
  const invited = members.filter(m => m.status === 'invited');

  const metaRow = createEl('div', 'btn-row');
  metaRow.appendChild(uiBadge(`${joined.length || ch.member_count || 0} joined`, 'muted'));
  for (const m of joined) {
    const label = m.principal_id === myId ? 'You' : (m.name || m.principal_id);
    metaRow.appendChild(uiBadge(`${label}${m.role ? ` · ${m.role}` : ''}`));
  }
  for (const m of invited) {
    const label = m.principal_id === myId ? 'You' : (m.name || m.principal_id);
    metaRow.appendChild(uiBadge(`Invited: ${label}`, 'muted'));
  }

  const channelActionBtn = (label, cls, payload) => uiButton({
    label,
    cls,
    onClick: () => runChannelAction(payload).catch(e => showError(e.message))
  });

  if (ch.my_status === 'invited') {
    metaRow.append(
      channelActionBtn('Accept invite', 'btn btn-xs btn-primary', { operation: 'join', channel_id: ch.id }),
      channelActionBtn('Decline', 'btn btn-xs btn-ghost', { operation: 'decline', channel_id: ch.id })
    );
  } else if (ch.my_status === 'none' && !isOwner) {
    metaRow.appendChild(channelActionBtn('Ask to join', 'btn btn-xs btn-primary', {
      operation: 'ask_join', channel_id: ch.id, announce: true
    }));
  } else if (ch.my_status === 'requested') {
    metaRow.appendChild(uiBadge('Join request pending', 'warn'));
  } else if (ch.my_status === 'joined' && ch.created_by !== myId) {
    metaRow.appendChild(channelActionBtn('Leave', 'btn btn-xs btn-ghost', { operation: 'leave', channel_id: ch.id }));
  }
  bar.appendChild(metaRow);

  if (canManage && pending.length) {
    const reqRow = createEl('div', 'btn-row');
    reqRow.appendChild(createEl('span', 'muted small', 'Pending join requests:'));
    for (const p of pending) {
      const pName = p.name || principalLabel(p.principal_id);
      reqRow.append(
        channelActionBtn(`Approve @${pName}`, 'btn btn-xs btn-primary', {
          operation: 'approve', channel_id: ch.id, recipient: p.principal_id
        }),
        channelActionBtn(`Decline @${pName}`, 'btn btn-xs btn-ghost', {
          operation: 'decline', channel_id: ch.id, recipient: p.principal_id
        })
      );
    }
    bar.appendChild(reqRow);
  }

  if (canManage || ch.my_status === 'joined') {
    const memberIds = new Set(members.filter(m => m.status === 'joined' || m.status === 'invited').map(m => m.principal_id));
    const invitees = state.agents.filter(a => !a.revoked && a.id !== myId && !memberIds.has(a.id));
    if (invitees.length) {
      const invRow = createEl('div', 'btn-row');
      const sel = createEl('select', 'input select-compact');
      sel.setAttribute('aria-label', 'Invite agent to channel');
      populateSelect(sel, invitees, {
        getValue: a => a.id,
        getLabel: a => `@ ${a.name} (${a.role || 'worker'})`
      });
      const invBtn = uiButton({
        label: 'Invite in # general',
        cls: 'btn btn-xs btn-secondary',
        onClick: () => {
          if (!sel.value) return;
          runChannelAction({ operation: 'invite', channel_id: ch.id, recipient: sel.value, announce: true })
            .catch(e => showError(e.message));
        }
      });
      invRow.append(sel, invBtn);
      bar.appendChild(invRow);
    }
  }
}

function canInspectAgentDMs() {
  return state.project?.role === 'owner' && state.project?.source !== 'remote' && !state.project?.remote;
}

function closeAgentDMs() {
  agentDMRequest += 1;
  if ($('#agent-dm-panel')) $('#agent-dm-panel').hidden = true;
  clearNode($('#agent-dm-results'));
  renderChatPanels();
}

function renderChatPanels() {
  const inspecting = Boolean($('#agent-dm-panel') && !$('#agent-dm-panel').hidden);
  const overlay = inspecting || Boolean(state.chatSearch);
  if ($('#chat-layout')) $('#chat-layout').dataset.mobilePane = overlay ? 'thread' : state.mobilePane;
  if ($('#chat-thread-pane > header')) $('#chat-thread-pane > header').hidden = overlay;
  if ($('#chat-search-panel')) $('#chat-search-panel').hidden = !state.chatSearch;
  if ($('#chat-timeline')) $('#chat-timeline').hidden = overlay;
  if ($('#message-form')) $('#message-form').hidden = overlay;
  if (overlay && $('#chat-channel-bar')) $('#chat-channel-bar').hidden = true;
  $('#inspect-agent-dms')?.setAttribute('aria-pressed', String(inspecting));
}

function renderAgentDMRow(pair, agent, row) {
  if (!pair) {
    return uiButton({
      label: principalLabel(row.first) + ' and ' + principalLabel(row.second) + ' · ' + formatTime(row.last_activity),
      cls: 'btn btn-secondary',
      onClick: () => browseAgentDMs({ first: row.first, second: row.second }, 0, agent)
    });
  }
  const message = createEl('article', 'list-item');
  const meta = createEl('header', 'agent-dm-meta');
  meta.append(createEl('strong', '', principalLabel(row.sender)), createEl('time', 'muted small', formatTime(row.created_at)));
  message.appendChild(meta);
  if (row.subject) message.appendChild(createEl('div', 'small', `Subject: ${row.subject}`));
  const body = createEl('div', 'msg-body markdown-body');
  renderSafeMarkdown(body, row.body || '');
  message.appendChild(body);
  return message;
}

function renderAgentDMContent(pair, agent, preserveScroll = null) {
  const target = $('#agent-dm-results');
  const panel = $('#agent-dm-panel');
  if (!target || !panel) return;
  clearNode(target);
  const { rows, hasMore } = agentDMBrowseState;
  const addButton = (label, action) => target.appendChild(uiButton({ label, cls: 'btn btn-secondary', onClick: action }));
  if (pair) addButton('All agent conversations', () => browseAgentDMs(null, 0, agent));
  if (pair && hasMore && rows.length) {
    const olderBtn = uiButton({
      label: agentDMBrowseState.loading ? 'Loading older messages…' : 'Older messages',
      cls: 'btn btn-secondary',
      disabled: agentDMBrowseState.loading,
      onClick: () => browseAgentDMs(pair, rows[0].sequence, agent)
    });
    olderBtn.id = 'agent-dm-older-btn';
    target.appendChild(olderBtn);
  }
  if (!rows.length) {
    target.appendChild(createEl('p', 'muted', pair ? 'No older messages.' : 'No agent conversations yet.'));
    return;
  }
  if (!pair) {
    appendProgressiveItems(target, rows, row => renderAgentDMRow(null, agent, row), {
      pageSize: 25,
      root: panel,
      hasMore: () => agentDMBrowseState.hasMore,
      loadMore: async () => {
        if (!agentDMBrowseState.hasMore || agentDMBrowseState.loading || !agentDMBrowseState.rows.length) {
          return { appended: [], hasMore: false };
        }
        const before = agentDMBrowseState.rows.at(-1).last_sequence;
        const appended = await fetchAgentDMsPage(null, before, agent);
        return { appended, hasMore: agentDMBrowseState.hasMore };
      }
    });
    panel.scrollTop = 0;
    return;
  }
  const olderFirstSlice = rows.length > 60 && !preserveScroll ? rows.slice(-60) : rows;
  if (rows.length > olderFirstSlice.length) {
    const showRestBtn = uiButton({
      label: `Show ${rows.length - olderFirstSlice.length} earlier loaded messages`,
      cls: 'btn btn-xs btn-ghost',
      onClick: () => renderAgentDMContent(pair, agent, { height: panel.scrollHeight, top: panel.scrollTop })
    });
    target.appendChild(showRestBtn);
  }
  for (const row of (preserveScroll ? rows : olderFirstSlice)) {
    target.appendChild(renderAgentDMRow(pair, agent, row));
  }
  panel.dataset.dmScrollSettled = '0';
  if (preserveScroll) {
    panel.scrollTop = preserveScroll.top + (panel.scrollHeight - preserveScroll.height);
  } else {
    panel.scrollTop = panel.scrollHeight;
  }
  const raf = typeof requestAnimationFrame === 'function' ? requestAnimationFrame : fn => setTimeout(fn, 16);
  raf(() => {
    if (panel.isConnected) panel.dataset.dmScrollSettled = '1';
  });
}

async function fetchAgentDMsPage(pair = null, before = 0, agent = '') {
  const panel = $('#agent-dm-panel');
  const requestID = agentDMRequest;
  const generation = state.generation;
  agentDMBrowseState.loading = true;
  try {
    const rows = await request(
      projectApi('agent-dms', pair ? {} : { before, agent }),
      pair ? { method: 'POST', body: { ...pair, before } } : {}
    );
    if (requestID !== agentDMRequest || generation !== state.generation || panel?.hidden) return [];
    const list = Array.isArray(rows) ? rows : [];
    agentDMBrowseState.hasMore = list.length === 50;
    if (!before) {
      agentDMBrowseState.rows = list;
    } else if (pair) {
      const seen = new Set(agentDMBrowseState.rows.map(m => m.id || m.sequence));
      const older = list.filter(m => !seen.has(m.id || m.sequence));
      agentDMBrowseState.rows = [...older, ...agentDMBrowseState.rows];
      return older;
    } else {
      const seen = new Set(agentDMBrowseState.rows.map(r => `${r.first}:${r.second}`));
      const more = list.filter(r => !seen.has(`${r.first}:${r.second}`));
      agentDMBrowseState.rows.push(...more);
      return more;
    }
    return list;
  } finally {
    agentDMBrowseState.loading = false;
  }
}

async function browseAgentDMs(pair = null, before = 0, agent = '') {
  const target = $('#agent-dm-results');
  if (!canInspectAgentDMs() || !target) return;
  if (state.view !== 'chat') setView('chat', { chatTab: 'agent-dms' });
  state.chatTab = 'agent-dms';
  closeChatSearch();
  const panel = $('#agent-dm-panel');
  const opening = panel.hidden;
  panel.hidden = false;
  syncUrlState();
  renderChatSidebarChannels();
  renderChatPanels();
  const heading = $('#agent-dm-title');
  heading.textContent = pair ? principalLabel(pair.first) + ' and ' + principalLabel(pair.second) : 'Agent conversations';
  if (opening || !before) heading.focus();
  const requestID = ++agentDMRequest;
  const generation = state.generation;
  const preserveScroll = before && pair ? { height: panel.scrollHeight, top: panel.scrollTop } : null;
  if (!before) {
    agentDMBrowseState = { pair, agent, rows: [], hasMore: false, loading: false };
    target.textContent = 'Loading…';
  } else {
    const olderBtn = $('#agent-dm-older-btn');
    if (olderBtn) {
      olderBtn.disabled = true;
      olderBtn.textContent = 'Loading older messages…';
    }
  }
  try {
    await fetchAgentDMsPage(pair, before, agent);
    if (requestID !== agentDMRequest || generation !== state.generation || panel.hidden) return;
    renderAgentDMContent(pair, agent, preserveScroll);
  } catch (error) {
    if (requestID === agentDMRequest && generation === state.generation && !panel.hidden) {
      if (!before) target.textContent = error.message;
      else showError(error.message);
    }
  }
}

function closeChatSearch() {
  clearTimeout(chatSearchTimer);
  chatSearchRequest += 1;
  state.chatSearch = '';
  renderChatPanels();
}

async function searchChat(query) {
  clearTimeout(chatSearchTimer);
  if (!query) {
    closeChatSearch();
    return;
  }
  const generation = state.generation;
  const searchID = ++chatSearchRequest;
  state.chatSearch = query;
  closeAgentDMs();
  renderChatPanels();
  const target = $('#chat-search-results');
  const panel = $('#chat-search-panel');
  const status = $('#chat-search-status');
  clearNode(target);
  status.textContent = 'Searching…';
  try {
    const matches = await request(projectApi('conversation-search', { q: query }));
    if (generation !== state.generation || searchID !== chatSearchRequest) return;
    const list = Array.isArray(matches) ? [...matches].reverse() : [];
    status.textContent = list.length ? list.length + ' matching messages' : 'No messages match “' + query + '”.';
    renderList(target, list, '', message => {
      const result = createEl('button', 'chat-search-result list-item');
      result.type = 'button';
      const room = message.recipient
        ? 'Direct message'
        : state.channels.find(channel => 'channel:' + channel.id === message.thread_id)?.name
          || (message.thread_id.startsWith('channel:') ? 'Group chat' : 'General');
      result.append(
        createEl('strong', '', principalLabel(message.sender) + ' · ' + room),
        createEl('span', 'muted small', formatTime(message.created_at)),
        createEl('span', 'chat-result-excerpt', message.subject || message.body)
      );
      result.onclick = () => openSourceMessage(message.id);
      return result;
    }, { pageSize: 25, root: panel || null });
  } catch (error) {
    if (generation === state.generation && searchID === chatSearchRequest) status.textContent = error.message;
  }
}

function renderAgentDMQuickList() {
  if (state.chatTab === 'agent-dms') renderChatSidebarChannels();
}

async function loadAgentDMThreads(gen) {
  if (!canInspectAgentDMs() || !state.project) {
    state.agentDMs = [];
    state.agentDMsHasMore = false;
    state.agentDMsLoadingMore = false;
    renderAgentDMQuickList();
    return;
  }
  try {
    const rows = await request(projectApi('agent-dms', { before: 0, agent: '' }));
    if (gen !== state.generation) return;
    const list = Array.isArray(rows) ? rows : [];
    state.agentDMs = list;
    state.agentDMsHasMore = list.length >= 50;
    state.agentDMsLoadingMore = false;
    renderAgentDMQuickList();
  } catch {
    if (gen === state.generation) {
      state.agentDMs = [];
      state.agentDMsHasMore = false;
      state.agentDMsLoadingMore = false;
      renderAgentDMQuickList();
    }
  }
}

async function loadMoreAgentDMThreads() {
  if (!canInspectAgentDMs() || !state.project || !state.agentDMsHasMore || state.agentDMsLoadingMore || !state.agentDMs.length) {
    return { appended: [], hasMore: false };
  }
  const gen = state.generation;
  const before = state.agentDMs.at(-1)?.last_sequence || 0;
  if (!before) {
    state.agentDMsHasMore = false;
    return { appended: [], hasMore: false };
  }
  state.agentDMsLoadingMore = true;
  try {
    const rows = await request(projectApi('agent-dms', { before, agent: '' }));
    if (gen !== state.generation) return { appended: [], hasMore: false };
    const list = Array.isArray(rows) ? rows : [];
    const seen = new Set(state.agentDMs.map(r => `${r.first}:${r.second}`));
    const appended = list.filter(r => !seen.has(`${r.first}:${r.second}`));
    state.agentDMs.push(...appended);
    state.agentDMsHasMore = list.length >= 50 && appended.length > 0;
    return { appended, hasMore: state.agentDMsHasMore };
  } catch {
    return { appended: [], hasMore: state.agentDMsHasMore };
  } finally {
    state.agentDMsLoadingMore = false;
  }
}

function renderChatSidebarChannels() {
  const allowedAgentDMs = canInspectAgentDMs();
  if (state.chatTab === 'agent-dms' && !allowedAgentDMs) state.chatTab = 'channels';
  if ($('#chat-tab-agent-dms')) $('#chat-tab-agent-dms').hidden = !allowedAgentDMs;
  $$('#chat-view-tabs .subtab').forEach(btn => {
    const active = btn.dataset.chatTab === state.chatTab;
    btn.classList.toggle('subtab-active', active);
    btn.setAttribute('aria-selected', String(active));
  });

  const list = $('#chat-channels');
  if (!list) return;
  clearNode(list);

  const decorateRoom = (button, room, metaNode = null) => {
    button.dataset.room = room;
    const right = createEl('span', 'channel-item-meta');
    if (metaNode) right.appendChild(metaNode);
    const indicator = createEl('span', 'unread-indicator');
    indicator.hidden = true;
    indicator.appendChild(createEl('span', 'visually-hidden', 'Unread messages'));
    right.appendChild(indicator);
    button.appendChild(right);
    return button;
  };
  const addRoom = (button, room, metaNode = null) => {
    list.appendChild(decorateRoom(button, room, metaNode));
  };

  if (state.chatTab === 'dms') {
    list.appendChild(createEl('div', 'channel-section-label', 'Direct messages'));
    const peers = state.agents.filter(a => !a.revoked && (!state.project || a.id !== state.project.id));
    if (!peers.length) {
      list.appendChild(createEl('p', 'muted small chat-empty-note', 'No other active agents in this project yet.'));
      return;
    }
    appendProgressiveItems(list, peers, a => {
      const pres = presenceFor(a.id);
      const btn = createEl('button', `channel-btn ${!state.channelId && state.recipient === a.id ? 'is-active' : ''}`);
      btn.type = 'button';
      const left = createEl('span', 'btn-row');
      left.append(createEl('span', `presence-dot ${pres?.online ? 'online' : ''}`), createEl('span', '', `@ ${a.name}`));
      btn.appendChild(left);
      btn.addEventListener('click', () => openConversation('', a.id));
      return decorateRoom(btn, conversationRoom('', a.id), uiBadge(a.role || 'worker', 'muted'));
    }, { pageSize: 35, root: list });
    return;
  }

  if (state.chatTab === 'agent-dms' && allowedAgentDMs) {
    list.appendChild(createEl('div', 'channel-section-label', 'Agent chats'));
    const inspecting = Boolean($('#agent-dm-panel') && !$('#agent-dm-panel').hidden);
    const allBtn = createEl('button', `channel-btn ${inspecting ? 'is-active' : ''}`);
    allBtn.id = 'inspect-agent-dms';
    allBtn.type = 'button';
    allBtn.setAttribute('aria-pressed', String(inspecting));
    allBtn.appendChild(createEl('span', '', 'All agent conversations'));
    allBtn.addEventListener('click', () => browseAgentDMs(null, 0, ''));
    list.appendChild(allBtn);

    const threads = Array.isArray(state.agentDMs) ? state.agentDMs : [];
    if (!threads.length) {
      list.appendChild(createEl('p', 'muted small chat-empty-note', 'No agent-to-agent direct messages yet.'));
      return;
    }
    appendProgressiveItems(list, threads, row => {
      const itemBtn = createEl('button', 'channel-btn agent-dm-quick-btn');
      itemBtn.type = 'button';
      const meta = createEl('span', 'channel-item-meta');
      meta.appendChild(uiBadge('DM', 'muted'));
      itemBtn.append(
        createEl('span', 'agent-dm-pair-label', `${principalLabel(row.first)} ↔ ${principalLabel(row.second)}`),
        meta
      );
      itemBtn.addEventListener('click', () => browseAgentDMs({ first: row.first, second: row.second }, 0, ''));
      return itemBtn;
    }, {
      pageSize: 25,
      root: list,
      hasMore: () => state.agentDMsHasMore,
      loadMore: loadMoreAgentDMThreads
    });
    return;
  }

  const genBtn = createEl('button', `channel-btn ${!state.recipient && !state.channelId ? 'is-active' : ''}`);
  genBtn.type = 'button';
  genBtn.appendChild(createEl('span', '', '# general'));
  genBtn.addEventListener('click', () => openConversation('', ''));
  addRoom(genBtn, conversationRoom('', ''));

  list.appendChild(createEl('div', 'channel-section-label', 'Group chats'));
  if (!state.channels.length) {
    list.appendChild(createEl('p', 'muted small chat-empty-note', 'No group chats yet.'));
  }
  appendProgressiveItems(list, state.channels, ch => {
    const btn = createEl('button', `channel-btn ${state.channelId === ch.id ? 'is-active' : ''}`);
    btn.type = 'button';
    btn.appendChild(createEl('span', '', `# ${ch.name}`));
    let statusLabel = `${ch.member_count || 0}`;
    let variant = 'muted';
    if (ch.pending_count > 0) {
      statusLabel = `${ch.pending_count} req`;
      variant = 'warn';
    } else if (ch.my_status === 'invited') {
      statusLabel = 'Invited';
      variant = 'warn';
    } else if (ch.my_status === 'requested') {
      statusLabel = 'Requested';
    } else if (ch.my_status === 'joined') {
      statusLabel = `${ch.member_count || 0}`;
    } else if (state.project?.role === 'owner') {
      statusLabel = `${ch.member_count || 0}`;
    } else {
      statusLabel = 'Join';
    }
    btn.addEventListener('click', () => openConversation(ch.id, ''));
    return decorateRoom(btn, conversationRoom(ch.id, ''), uiBadge(statusLabel, variant));
  }, { pageSize: 35, root: list });
}

function buildChatMessageCard(m, { activeGroup, targetAgent }) {
  const isYou = state.project && m.sender === state.project.id;
  const card = createEl('article', `msg-card ${isYou ? 'is-you' : ''}`);
  card.dataset.msgId = m.id || '';
  const avatar = createEl('span', 'chat-avatar', labelInitial(m.sender_name || principalLabel(m.sender)));
  avatar.setAttribute('aria-hidden', 'true');
  card.appendChild(avatar);
  const head = createEl('div', 'msg-head');
  const senderWrap = createEl('div', 'msg-sender');
  const label = isYou ? 'You' : principalLabel(m.sender, m.sender_name);
  senderWrap.appendChild(createEl('strong', '', label));
  if (!isYou && m.sender_role) senderWrap.appendChild(createEl('span', 'muted small', m.sender_role));
  if (m.recipient && !targetAgent && !activeGroup) {
    senderWrap.appendChild(createEl('span', 'muted small', `→ ${principalLabel(m.recipient)}`));
  }
  head.append(senderWrap, createEl('span', 'muted small', `${formatShortTime(m.created_at)} · #${m.sequence || m.id}`));

  const openActions = () => openMessageActions(m, label, Boolean(m.recipient || activeGroup));
  const menu = uiButton({
    label: 'Message actions',
    icon: 'more',
    cls: 'btn btn-ghost message-menu',
    iconOnly: true,
    onClick: openActions
  });
  menu.setAttribute('aria-haspopup', 'dialog');
  card.addEventListener('contextmenu', event => {
    if (window.getSelection()?.toString()) return;
    event.preventDefault();
    openActions();
  });
  head.appendChild(menu);
  card.appendChild(head);

  if (m.subject) card.appendChild(createEl('div', 'small', `Subject: ${m.subject}`));
  const body = createEl('div', 'msg-body markdown-body');
  renderSafeMarkdown(body, m.body || '');
  card.appendChild(body);
  return card;
}

function updateChatHeaderActions(canReadGroup) {
  const jump = $('#chat-jump-latest-btn');
  if (jump) {
    jump.hidden = !state.chatLoaded || !state.chatUnseen;
    const span = jump.querySelector('span');
    if (span) span.textContent = state.chatUnseen === 1 ? '1 new message' : state.chatUnseen + ' new messages';
  }
  const olderBtn = $('#chat-load-older-btn');
  if (olderBtn) olderBtn.hidden = !state.messagesHasOlder || !canReadGroup;
}

async function loadOlderChatMessages() {
  const older = $('#chat-load-older-btn');
  const timeline = $('#chat-timeline');
  if (!timeline || !state.messagesHasOlder || !state.messagesOldestSeq || chatLoadingOlder || older?.disabled) return;
  chatLoadingOlder = true;
  if (older) older.disabled = true;
  const generation = ++state.generation;
  const key = activeChatDraftKey();
  const previousHeight = timeline.scrollHeight;
  const previousTop = timeline.scrollTop;
  try {
    await loadChatMessages(generation, { before: state.messagesOldestSeq });
    if (generation !== state.generation || key !== activeChatDraftKey()) return;
    renderChatView();
    timeline.scrollTop = previousTop + (timeline.scrollHeight - previousHeight);
    state.chatScroll.set(key, timeline.scrollTop);
  } catch (error) {
    if (generation === state.generation) showError(error.message);
  } finally {
    chatLoadingOlder = false;
    if (older) older.disabled = false;
  }
}

function renderChatView() {
  const newGroupBtn = $('#open-channel-dialog-btn');
  if (newGroupBtn) {
    newGroupBtn.hidden = !state.project;
    newGroupBtn.onclick = () => {
      if (!state.project) return;
      openChannelDialog(state.recipient ? [state.recipient] : []);
    };
  }

  const onlineCount = state.presence.filter(p => p.online).length;
  const countBadge = $('#chat-agent-count');
  if (countBadge) {
    countBadge.textContent = `${onlineCount} online`;
    countBadge.className = `badge ${onlineCount > 0 ? 'badge-ok' : 'badge-muted'}`;
  }

  renderChatSidebarChannels();
  renderConversationBadges();

  const activeGroup = selectedGroupChannel();
  const targetAgent = !activeGroup && state.recipient ? agentObject(state.recipient) : null;
  const isOwner = state.project?.role === 'owner';
  const canReadGroup = !activeGroup || isOwner || activeGroup.my_status === 'joined';
  const canPostGroup = canSendMessage() && canReadGroup;

  renderChannelBar($('#chat-channel-bar'), activeGroup);
  renderChatPanels();

  if ($('#chat-thread-title')) {
    $('#chat-thread-title').textContent = activeGroup ? `# ${activeGroup.name}` : (targetAgent ? `@ ${targetAgent.name}` : '# general');
  }
  if ($('#chat-thread-scope')) {
    if (activeGroup) {
      const prefix = activeGroup.topic ? `${activeGroup.topic} · ` : '';
      const accessNote = isOwner
        ? 'Private group chat (owner oversight enabled)'
        : activeGroup.my_status === 'joined'
          ? 'Private group chat for joined members'
          : 'Private group chat — ask to join or accept an invite above';
      $('#chat-thread-scope').textContent = `${prefix}${accessNote}`;
    } else {
      $('#chat-thread-scope').textContent = targetAgent
        ? 'Direct message · visible to project owners'
        : 'Visible to project members';
    }
  }
  if ($('#message-recipient-pill')) {
    $('#message-recipient-pill').textContent = activeGroup
      ? (canPostGroup ? `Posting to # ${activeGroup.name}` : `Join # ${activeGroup.name} to post`)
      : (targetAgent ? `Direct message to @ ${targetAgent.name}` : 'Posting to # general');
  }
  if ($('#message-body')) {
    const key = activeChatDraftKey();
    $('#message-body').disabled = !canPostGroup;
    $('#message-body').value = state.messageDrafts[key] || '';
    $('#message-body').placeholder = activeGroup
      ? (canPostGroup ? `Message # ${activeGroup.name}…` : `Request access above to post in # ${activeGroup.name}`)
      : (targetAgent ? `Message @ ${targetAgent.name}…` : 'Write a message to # general…');
  }
  const sendBtn = $('#message-form button[type="submit"]');
  if (sendBtn) sendBtn.disabled = !canPostGroup;

  updateChatHeaderActions(canReadGroup);

  const timeline = $('#chat-timeline');
  if (timeline) {
    const key = activeChatDraftKey();
    const restore = timeline.dataset.conversation === key && timeline.dataset.loaded === 'true'
      ? timeline.scrollTop : state.chatScroll.get(key);
    timeline.dataset.conversation = key;
    timeline.dataset.loaded = String(state.chatLoaded);
    timeline.dataset.scrollSettled = '0';
    clearNode(timeline);
    const ordered = canReadGroup ? [...state.messages].sort((a, b) => (a.sequence || 0) - (b.sequence || 0)) : [];
    if (!canReadGroup && activeGroup) {
      timeline.appendChild(createEl('p', 'muted', 'You are not a member of this group chat yet. Use Ask to join or Accept invite above.'));
    } else if (!state.chatLoaded) {
      timeline.appendChild(createEl('p', 'muted', 'Loading conversation…'));
    } else if (!ordered.length) {
      timeline.appendChild(createEl('p', 'muted', 'No messages in this conversation yet. Send one below to begin.'));
    } else if (state.messagesHasOlder) {
      timeline.appendChild(createEl('p', 'muted small chat-older-hint', 'Scroll up to load older messages…'));
    }
    for (const m of ordered) {
      timeline.appendChild(buildChatMessageCard(m, { activeGroup, targetAgent }));
    }
    if (state.chatLoaded) timeline.scrollTop = restore ?? timeline.scrollHeight;
    const raf = typeof requestAnimationFrame === 'function' ? requestAnimationFrame : fn => setTimeout(fn, 16);
    raf(() => {
      if (timeline.isConnected && timeline.dataset.conversation === key) {
        timeline.dataset.scrollSettled = '1';
      }
    });
    markVisibleConversationRead();
    renderConversationBadges();
  }
}

async function openSourceMessage(id) {
  const project = state.project;
  if (!project) return;
  const generation = state.generation;
  const dialog = $('#message-source-dialog');
  const panel = $('#message-source-body');
  panel.textContent = 'Loading message…';
  openDialog(dialog.id);
  try {
    const message = await request(projectApi('conversation-message', { id }));
    if (project !== state.project || generation !== state.generation || !dialog.open) return;
    panel.replaceChildren(
      createEl('p', 'muted small', principalLabel(message.sender) + ' · ' + formatTime(message.created_at))
    );
    if (message.subject) panel.appendChild(createEl('strong', '', message.subject));
    const body = createEl('div', 'msg-body markdown-body');
    renderSafeMarkdown(body, message.body || '');
    panel.appendChild(body);
  } catch (error) {
    if (project === state.project && generation === state.generation && dialog.open) {
      panel.textContent = error.status === 404 ? 'This message is unavailable to your current identity.' : error.message;
    }
  }
}

function openMessageActions(message, label, restricted) {
  const project = state.project;
  const conversation = activeChatDraftKey();
  const list = $('#message-actions-list');
  clearNode(list);
  const isCurrent = () => state.project === project && conversation === activeChatDraftKey() && state.view === 'chat';
  const action = (name, callback, disabled = false) => {
    list.appendChild(uiButton({
      label: name,
      cls: 'btn btn-secondary',
      disabled,
      onClick: async () => {
        closeDialog('message-actions-dialog');
        if (!isCurrent()) return;
        try {
          await callback();
        } catch (error) {
          if (isCurrent()) showError(error.message);
        }
      }
    }));
  };
  if (message.sender !== project.id) {
    action(message.acknowledged_by_me ? 'Acknowledged' : 'Acknowledge', async () => {
      await request(projectApi('messages'), { method: 'POST', body: { operation: 'acknowledge', id: message.id } });
      if (isCurrent()) {
        const current = state.messages.find(item => item.id === message.id);
        if (current) current.acknowledged_by_me = true;
        renderChatView();
      }
    }, Boolean(message.acknowledged_by_me));
  }
  const text = message.body || '';
  const source = '[Source message](gossip:message:' + message.id + ')';
  const confirmCopy = title => !restricted || askConfirmation(title, 'Publishing this draft shares the quoted text with project members.', text);
  if (canDelegateTask()) {
    action('Create task from message', async () => {
      if (!await confirmCopy('Copy this message into a task draft?') || !isCurrent()) return;
      $('#task-form').reset();
      $('#task-title').value = (message.subject || text.split('\n')[0]).slice(0, 80);
      $('#task-description').value = `From ${label}:\n\n${text}\n\n${source}`;
      openDialog('task-dialog');
    });
  }
  if (canEditPlan()) {
    action('Use in plan', async () => {
      if (!await confirmCopy('Copy this message into a plan draft?') || !isCurrent()) return;
      const existing = state.plan?.body || '';
      openPlanEditor(`${existing ? existing + '\n\n' : ''}- Note (${label}): ${text}\n\n${source}`);
    });
  }
  if (!list.children.length) list.appendChild(createEl('p', 'muted', 'No actions available for this message.'));
  openDialog('message-actions-dialog');
}

function saveCurrentDraft() {
  const input = $('#message-body');
  if (!input) return;
  state.messageDrafts[activeChatDraftKey()] = input.value;
}

async function checkOpenConversation() {
  if (chatChecking || $('#message-actions-dialog')?.open || document.hidden || state.view !== 'chat' || !state.project) return;
  if (state.chatSearch || ($('#agent-dm-panel') && !$('#agent-dm-panel').hidden)) return;
  const timeline = $('#chat-timeline');
  const generation = state.generation;
  const previousCount = state.messages.length;
  chatChecking = true;
  try {
    await loadConversationActivity(generation);
    if (generation !== state.generation || !state.chatLoaded || !timeline?.clientHeight || $('#chat-load-older-btn')?.disabled) return;
    const seenBefore = new Set(state.messages.map(m => m.id));
    await loadChatMessages(generation, { incremental: true });
    if (generation !== state.generation || state.messages.length === previousCount) return;
    const followsLatest = timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight <= 32;
    if (followsLatest) state.chatUnseen = 0;
    const added = state.messages.filter(m => !seenBefore.has(m.id));
    if (previousCount > 0 && added.length && timeline.dataset.conversation === activeChatDraftKey()) {
      const activeGroup = selectedGroupChannel();
      const targetAgent = !activeGroup && state.recipient ? agentObject(state.recipient) : null;
      const canReadGroup = !activeGroup || state.project?.role === 'owner' || activeGroup.my_status === 'joined';
      for (const m of added) timeline.appendChild(buildChatMessageCard(m, { activeGroup, targetAgent }));
      updateChatHeaderActions(canReadGroup);
      renderConversationBadges();
    } else {
      renderChatView();
    }
    if (followsLatest) {
      timeline.scrollTop = timeline.scrollHeight;
      markVisibleConversationRead();
    }
  } catch {
    // Keep the loaded conversation during transient failures.
  } finally {
    chatChecking = false;
  }
}

function wireChatEvents() {
  $('#channel-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    if (!state.project) return;
    const name = $('#channel-name')?.value?.trim() || '';
    if (!name) return;
    const members = Array.from(document.querySelectorAll('#channel-members-picks input[type="checkbox"]:checked'))
      .map(cb => cb.value)
      .filter(Boolean);
    try {
      const created = await request(projectApi('channels'), {
        method: 'POST',
        body: {
          operation: 'create',
          name,
          auto_suffix: Boolean($('#channel-auto-suffix')?.checked),
          topic: $('#channel-topic')?.value?.trim() || '',
          members,
          announce: Boolean($('#channel-announce')?.checked)
        }
      });
      closeDialog('channel-dialog');
      state.channelId = created?.id || '';
      state.recipient = '';
      state.chatTab = 'channels';
      state.mobilePane = 'thread';
      syncUrlState();
      await refresh();
    } catch (err) {
      showError(err.message);
    }
  });

  $$('#chat-view-tabs .subtab').forEach(btn => {
    btn.addEventListener('click', () => {
      const tab = btn.dataset.chatTab || 'channels';
      state.chatTab = tab;
      if (tab === 'agent-dms') {
        browseAgentDMs(null, 0, '');
        return;
      }
      closeAgentDMs();
      if (typeof window !== 'undefined' && window.matchMedia?.('(max-width: 700px)')?.matches) {
        state.mobilePane = 'conversations';
      }
      syncUrlState();
      renderChatView();
    });
  });

  if ($('#close-agent-dms')) $('#close-agent-dms').onclick = () => {
    closeAgentDMs();
    state.chatTab = state.recipient ? 'dms' : 'channels';
    syncUrlState();
    renderChatView();
  };
  if ($('#close-chat-search')) $('#close-chat-search').onclick = () => {
    closeChatSearch();
    if ($('#header-search-input')) $('#header-search-input').value = '';
    renderChatView();
  };
  $('#chat-mobile-back')?.addEventListener('click', () => {
    closeChatSearch();
    state.mobilePane = 'conversations';
    renderChatView();
  });

  setInterval(checkOpenConversation, 5000);
  document.addEventListener('visibilitychange', checkOpenConversation);
  $('#chat-jump-latest-btn')?.addEventListener('click', () => {
    const timeline = $('#chat-timeline');
    if (timeline) timeline.scrollTop = timeline.scrollHeight;
    markVisibleConversationRead();
  });
  $('#chat-timeline')?.addEventListener('scroll', event => {
    const timeline = event.currentTarget;
    if (timeline.dataset.loaded === 'true' && timeline.clientHeight) {
      state.chatScroll.set(timeline.dataset.conversation, timeline.scrollTop);
      markVisibleConversationRead();
      if (timeline.dataset.scrollSettled === '1'
        && timeline.scrollTop <= 140
        && state.messagesHasOlder
        && state.messagesOldestSeq
        && timeline.scrollHeight > timeline.clientHeight) {
        void loadOlderChatMessages();
      }
    }
  });
  $('#agent-dm-panel')?.addEventListener('scroll', event => {
    const panel = event.currentTarget;
    if (!panel.hidden
      && agentDMBrowseState.pair
      && panel.dataset.dmScrollSettled === '1'
      && panel.scrollTop <= 140
      && agentDMBrowseState.hasMore
      && !agentDMBrowseState.loading
      && agentDMBrowseState.rows.length
      && panel.scrollHeight > panel.clientHeight) {
      void browseAgentDMs(agentDMBrowseState.pair, agentDMBrowseState.rows[0].sequence, agentDMBrowseState.agent);
    }
  });
  $('#message-body')?.addEventListener('input', saveCurrentDraft);
  $('#message-body')?.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      $('#message-form')?.requestSubmit?.();
    }
  });

  $('#message-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    const input = $('#message-body');
    const draft = input?.value || '';
    const body = draft.trim();
    if (!body || !state.project) return;
    const key = activeChatDraftKey();
    const generation = state.generation;
    const send = $('#message-form button[type="submit"]');
    if (send.disabled) return;
    send.disabled = true;
    try {
      const message = await request(projectApi('messages'), {
        method: 'POST',
        body: {
          channel_id: state.channelId || '',
          recipient: state.channelId ? '' : (state.recipient || ''),
          body,
          request_id: `msg-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
        }
      });
      if (state.messageDrafts[key] === draft || state.messageDrafts[key] === undefined) {
        state.messageDrafts[key] = '';
      }
      if (generation !== state.generation || state.view !== 'chat' || key !== activeChatDraftKey()) return;
      if (message?.id && !state.messages.some(item => item.id === message.id)) {
        state.messages.push(message);
        state.messagesOldestSeq ||= message.sequence || 0;
      }
      renderChatView();
      const timeline = $('#chat-timeline');
      if (timeline) timeline.scrollTop = timeline.scrollHeight;
    } catch (err) {
      if (generation === state.generation) showError(err.message);
    } finally {
      if (generation === state.generation) send.disabled = false;
    }
  });

  $('#chat-load-older-btn')?.addEventListener('click', () => {
    void loadOlderChatMessages();
  });
}
