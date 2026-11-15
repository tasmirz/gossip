'use strict';

function renderAgentTree(agents, matches, selected, list) {
  const selectAgent = agent => {
    state.profileAgent = agent.id;
    state.agentPane = 'profile';
    renderAgentsView();
    if (state.agentTasksAgentID !== agent.id) void loadAgentTaskPreview(agent.id);
    if (matchMedia('(max-width: 700px)').matches) $('#agent-profile-back')?.focus();
    else $('#agents-list [aria-selected="true"]')?.focus();
  };

  const byID = new Map(agents.map(agent => [agent.id, agent]));
  const visibleIDs = new Set(matches.map(agent => agent.id));
  for (const agent of matches) {
    let parentID = agent.parent_id;
    while (parentID && byID.has(parentID) && !visibleIDs.has(parentID)) {
      visibleIDs.add(parentID);
      parentID = byID.get(parentID).parent_id;
    }
  }
  const children = new Map();
  const roots = [];
  for (const agent of agents) {
    const parent = byID.get(agent.parent_id);
    if (!parent || parent.id === agent.id) roots.push(agent);
    else {
      if (!children.has(parent.id)) children.set(parent.id, []);
      children.get(parent.id).push(agent);
    }
  }

  const tree = createEl('div', 'agent-tree');
  tree.setAttribute('role', 'tree');
  tree.setAttribute('aria-label', 'Project agent hierarchy');

  const renderNode = (agent, parent) => {
    if (!visibleIDs.has(agent.id)) return null;
    const node = createEl('div', 'agent-tree-node');
    const row = createEl('button', 'agent-roster-row', '');
    row.type = 'button';
    row.setAttribute('role', 'treeitem');
    row.setAttribute('aria-selected', String(agent.id === selected.id));
    row.setAttribute('aria-level', String(parent ? Number(parent.dataset.level || 0) + 1 : 1));
    node.dataset.level = row.getAttribute('aria-level');

    const avatar = createEl('span', 'agent-avatar', labelInitial(agent.name));
    avatar.setAttribute('aria-hidden', 'true');
    const summary = createEl('span', 'agent-roster-summary');
    summary.append(
      createEl('strong', '', agent.name),
      createEl('span', 'muted small', (agent.project_role || agent.role) + ' · ' + (presenceFor(agent.id)?.online ? 'Online' : 'Offline'))
    );
    row.append(avatar, summary);
    row.onclick = () => selectAgent(agent);
    node.appendChild(row);

    const descendants = (children.get(agent.id) || []).filter(item => visibleIDs.has(item.id));
    if (descendants.length) {
      const expanded = !state.collapsedAgentIDs.has(agent.id);
      const toggle = uiButton({
        label: (expanded ? 'Collapse ' : 'Expand ') + agent.name + ' team',
        icon: 'chevron-right',
        cls: `agent-tree-toggle icon-btn-sm${expanded ? '' : ' is-collapsed'}`,
        iconOnly: true,
        title: expanded ? 'Collapse team' : 'Expand team',
        onClick: () => {
          if (state.collapsedAgentIDs.has(agent.id)) state.collapsedAgentIDs.delete(agent.id);
          else state.collapsedAgentIDs.add(agent.id);
          renderAgentsView();
        }
      });
      toggle.classList.remove('btn');
      toggle.setAttribute('aria-expanded', String(expanded));
      row.before(toggle);

      const group = createEl('div', 'agent-tree-children');
      group.setAttribute('role', 'group');
      group.hidden = !expanded;
      for (const child of descendants) group.appendChild(renderNode(child, node));
      node.appendChild(group);
    }
    return node;
  };

  if (!matches.length) {
    list.appendChild(createEl('p', 'muted small', state.agentQuery ? 'No agents match this search.' : 'No project members yet.'));
  } else {
    for (const root of roots) {
      if (visibleIDs.has(root.id)) tree.appendChild(renderNode(root, null));
    }
    list.appendChild(tree);
  }
}

function renderAgentsView() {
  const layout = $('#agent-layout');
  const list = $('#agents-list');
  const profile = $('#agent-profile');
  if (!layout || !list || !profile) return;

  const agents = state.agents.filter(agent => !agent.revoked);
  const query = state.agentQuery.toLowerCase();
  const matches = agents.filter(agent => [agent.name, agent.project_role, agent.role]
    .some(value => value?.toLowerCase().includes(query)));
  const selected = agents.find(agent => agent.id === state.profileAgent) || agents[0];
  layout.dataset.pane = state.agentPane || 'roster';
  if ($('#agent-roster-count')) $('#agent-roster-count').textContent = matches.length;
  clearNode(list);
  clearNode(profile);
  if (!selected) {
    list.appendChild(createEl('p', 'muted small', 'No project members yet.'));
    return;
  }

  const activeTab = state.agentsTab || 'all';
  $$('#agents-view-tabs .subtab').forEach(btn => {
    const active = btn.dataset.agentTab === activeTab;
    btn.classList.toggle('subtab-active', active);
    btn.setAttribute('aria-selected', String(active));
  });

  renderAgentTree(agents, matches, selected, list);

  const presence = presenceFor(selected.id);
  const isYou = selected.id === state.project?.id;
  const actions = createEl('div', 'btn-row agent-profile-actions');
  const action = (parent, label, callback, icon, classes = 'btn btn-secondary', iconOnly = false) => {
    const button = uiButton({
      label,
      icon,
      cls: classes,
      iconOnly,
      title: label,
      onClick: event => {
        callback(event);
        parent.closest('details')?.removeAttribute('open');
      }
    });
    parent.appendChild(button);
    return button;
  };

  const back = action(profile, 'Back to roster', () => {
    state.agentPane = 'roster';
    renderAgentsView();
    $('#agents-list [aria-selected="true"]')?.focus();
  }, 'back', 'btn btn-sm btn-ghost plan-reader-back');
  back.id = 'agent-profile-back';

  const header = createEl('div', 'card-head agent-profile-header');
  const avatarLg = createEl('div', 'agent-avatar agent-avatar-lg', labelInitial(selected.name));
  avatarLg.setAttribute('aria-hidden', 'true');
  const identityGroup = createEl('div', 'agent-profile-identity');
  const titleBadge = createEl('div', 'member-row-main');
  titleBadge.append(
    createEl('h2', '', selected.name + (isYou ? ' (You)' : '')),
    uiBadge(presence?.online ? 'Online' : 'Offline', presence?.online ? 'ok' : 'muted'),
    uiBadge(selected.project_role || selected.role || 'worker', 'muted')
  );
  identityGroup.append(titleBadge, createEl('p', 'muted small agent-principal-preview', selected.id));
  const headerLeft = createEl('div', 'agent-profile-header-lead');
  headerLeft.append(avatarLg, identityGroup);
  header.append(headerLeft, actions);
  profile.appendChild(header);

  if (!isYou && canSendMessage()) {
    action(actions, 'Message', () => setView('chat', { recipient: selected.id, mobilePane: 'thread' }), 'chat', 'btn btn-sm btn-primary icon-only-control', false);
  }
  action(actions, 'To-dos', () => setView('todos', { todoAgent: selected.id }), 'todos', 'btn btn-sm btn-secondary icon-only-control', false);

  const actionMenu = createEl('details', 'agent-profile-action-menu');
  const actionSummary = createEl('summary', 'btn btn-sm btn-secondary icon-btn');
  actionSummary.setAttribute('aria-label', 'More agent actions');
  actionSummary.title = 'More agent actions';
  actionSummary.appendChild(svgIcon('more'));
  actionMenu.appendChild(actionSummary);
  const actionList = createEl('div', 'agent-profile-action-list');

  if (!isYou && canSendMessage()) {
    action(actionList, 'Start group', () => {
      setView('chat', { mobilePane: 'thread' });
      openChannelDialog([selected.id]);
    }, 'agents', 'btn btn-sm btn-ghost');
  }
  if (canInspectAgentDMs() && selected.role !== 'owner') {
    action(actionList, 'Agent conversations', () => browseAgentDMs(null, 0, selected.id), 'lock', 'btn btn-sm btn-ghost');
  }
  if (canEditSettings()) {
    action(actionList, 'Assign work role', () => {
      $('#role-form').reset();
      populateSelect($('#role-agent'), agents, { getValue: a => a.id, getLabel: a => a.name, selectedValue: selected.id });
      populateSelect($('#role-preset'), state.rolePresets, {
        placeholder: 'Custom role',
        getValue: p => p.id,
        getLabel: p => p.name || p.id
      });
      openDialog('work-roles-section');
    }, 'edit', 'btn btn-sm btn-ghost');
  }
  if (state.project?.role === 'owner' && !isYou) {
    action(actionList, selected.parent_id ? 'Change parent' : 'Set parent', () => {
      const form = $('#agent-parent-form');
      form.elements.child_id.value = selected.id;
      $('#agent-parent-child').textContent = selected.name;
      populateSelect(
        form.elements.parent_id,
        agents.filter(a => a.id !== selected.id),
        {
          placeholder: 'Project root',
          getValue: a => a.id,
          getLabel: a => `${a.name} · ${a.project_role || a.role}`,
          selectedValue: selected.parent_id || ''
        }
      );
      $('#agent-parent-error').textContent = '';
      openDialog('agent-parent-dialog');
    }, 'agents', 'btn btn-sm btn-ghost');
  }
  if (actionList.childElementCount) {
    actionMenu.appendChild(actionList);
    actions.appendChild(actionMenu);
  }

  const showRoles = activeTab === 'all' || activeTab === 'roles';
  const showTasks = activeTab === 'all' || activeTab === 'tasks';
  const showDetails = activeTab === 'all' || activeTab === 'details';

  const workRoles = state.roles.filter(role => role.principal_id === selected.id && !role.task_id && role.active);
  if (showRoles) {
    const roles = createEl('section', 'agent-profile-section');
    const rolesHead = createEl('div', 'card-head');
    const rolesTitle = createEl('div', 'member-row-main');
    rolesTitle.append(createEl('h3', '', 'Work roles'), uiBadge(String(workRoles.length), 'muted'));
    rolesHead.appendChild(rolesTitle);
    if (canEditSettings()) {
      rolesHead.appendChild(uiButton({
        label: '+ Assign role',
        cls: 'btn btn-xs btn-secondary',
        onClick: () => {
          $('#role-form').reset();
          populateSelect($('#role-agent'), agents, { getValue: a => a.id, getLabel: a => a.name, selectedValue: selected.id });
          populateSelect($('#role-preset'), state.rolePresets, {
            placeholder: 'Custom role',
            getValue: p => p.id,
            getLabel: p => p.name || p.id
          });
          openDialog('work-roles-section');
        }
      }));
    }
    roles.appendChild(rolesHead);
    if (!workRoles.length) {
      roles.appendChild(createEl('p', 'muted small', 'No project-wide work role assigned.'));
    } else {
      for (const role of workRoles) {
        const card = createEl('article', 'list-item agent-role-card');
        card.appendChild(createEl('strong', '', role.name || 'Work role'));
        if (role.duties) {
          const duties = createEl('details', 'agent-role-duties');
          duties.append(createEl('summary', '', 'Responsibilities'), createEl('p', 'muted small', role.duties));
          card.appendChild(duties);
        }
        roles.appendChild(card);
      }
    }
    profile.appendChild(roles);
  }

  const checklistItems = (state.checklists?.items || []).filter(item => item.agent_id === selected.id);
  const assignedTasks = state.agentTasksAgentID === selected.id ? state.agentTasks : [];
  if (showTasks) {
    const taskSection = createEl('section', 'agent-profile-section agent-profile-assigned');
    const taskHead = createEl('div', 'card-head');
    taskHead.append(
      createEl('h3', '', 'Assigned project tasks'),
      uiBadge(state.agentTasksLoading && state.agentTasksAgentID === selected.id ? '…' : String(assignedTasks.length), 'muted')
    );
    taskSection.appendChild(taskHead);
    if (state.agentTasksLoading && state.agentTasksAgentID === selected.id) {
      taskSection.appendChild(createEl('p', 'muted small', 'Loading assigned tasks…'));
    } else if (state.agentTasksError && state.agentTasksAgentID === selected.id) {
      taskSection.appendChild(createEl('p', 'muted small', 'Tasks could not be loaded: ' + state.agentTasksError));
    } else if (!assignedTasks.length) {
      taskSection.appendChild(createEl('p', 'muted small', 'No project tasks assigned.'));
    } else {
      const taskList = createEl('ul', 'agent-profile-checklist-items');
      const visibleCount = activeTab === 'tasks' ? assignedTasks.length : 6;
      for (const task of assignedTasks.slice(0, visibleCount)) {
        const row = createEl('li', 'agent-profile-task-row');
        const open = uiButton({
          label: task.title || task.id,
          cls: 'agent-profile-task',
          title: task.title || task.id,
          onClick: () => openTaskDetail(task.id)
        });
        const taskStatus = task.status || 'ready';
        const taskStatusText = taskStatusLabels[taskStatus] || taskStatus.replaceAll('_', ' ');
        row.append(open, uiBadge(taskStatusText, ['done', 'accepted'].includes(taskStatus) ? 'ok' : 'muted'));
        taskList.appendChild(row);
      }
      taskSection.appendChild(taskList);
      if (assignedTasks.length > visibleCount) {
        taskSection.appendChild(createEl('p', 'muted small', `${assignedTasks.length - visibleCount} more · open To-dos for the full list.`));
      }
    }
    profile.appendChild(taskSection);

    const checklistSection = createEl('section', 'agent-profile-section');
    const checklistHead = createEl('div', 'card-head');
    checklistHead.append(createEl('h3', '', 'Personal to-dos'), uiBadge(String(checklistItems.length), 'muted'));
    checklistSection.appendChild(checklistHead);
    if (!state.checklists?.available) {
      checklistSection.appendChild(createEl('p', 'muted small', state.checklists?.reason || 'Personal checklist unavailable.'));
    } else if (!checklistItems.length) {
      checklistSection.appendChild(createEl('p', 'muted small', 'No personal to-dos yet.'));
    } else {
      const items = createEl('ul', 'agent-profile-checklist-items');
      const visibleChecklist = activeTab === 'tasks' ? checklistItems.length : 8;
      for (const item of checklistItems.slice(0, visibleChecklist)) {
        const row = createEl('li', 'agent-profile-checklist-item');
        row.append(
          createEl('span', '', item.title || item.id),
          uiBadge(item.status === 'doing' ? 'Doing' : item.status === 'done' ? 'Done' : 'To do', item.status === 'done' ? 'ok' : 'muted')
        );
        items.appendChild(row);
      }
      checklistSection.appendChild(items);
      if (checklistItems.length > visibleChecklist) {
        checklistSection.appendChild(createEl('p', 'muted small', `And ${checklistItems.length - visibleChecklist} more · open To-dos to see the full list.`));
      }
    }
    profile.appendChild(checklistSection);
  }

  if (showDetails) {
    const details = createEl('section', 'agent-profile-section agent-details-panel');
    details.appendChild(createEl('h3', '', 'Identity & usage'));
    const grid = createEl('dl', 'agent-details-grid');
    const addDetail = (term, valNode) => {
      const dt = createEl('dt', '', term);
      const dd = createEl('dd', '');
      dd.appendChild(valNode);
      grid.append(dt, dd);
    };
    const idWrap = createEl('div', 'scope-id-pill');
    const idCode = createEl('code', '', selected.id);
    const copyBtn = uiButton({
      label: 'Copy ID',
      icon: 'copy',
      cls: 'btn btn-xs btn-secondary',
      onClick: async () => {
        try {
          await navigator.clipboard.writeText(selected.id);
          copyBtn.querySelector('span').textContent = 'Copied';
          setTimeout(() => { if (copyBtn.isConnected) copyBtn.querySelector('span').textContent = 'Copy ID'; }, 1500);
        } catch {}
      }
    });
    idWrap.append(idCode, copyBtn);
    addDetail('Principal ID', idWrap);
    addDetail('Project role', uiBadge(selected.project_role || selected.role || 'worker', 'muted'));
    if (presence?.reported_capabilities?.length) {
      const capsWrap = createEl('div', 'btn-row');
      for (const cap of presence.reported_capabilities) capsWrap.appendChild(uiBadge(cap, 'muted'));
      addDetail('Capabilities', capsWrap);
    }
    if (presence?.client_name) {
      addDetail('Client', createEl('span', '', `${presence.client_name} ${presence.client_version || ''}`.trim()));
    }
    details.appendChild(grid);
    appendAgentUsageDetails(details, presence);
    profile.appendChild(details);
  }
}

function renderSearchView() {
  const showGraph = state.searchTab === 'graph';
  const status = $('#search-status');
  const resultsTab = $('#search-results-tab');
  const graphTab = $('#search-graph-tab');
  resultsTab?.classList.toggle('subtab-active', !showGraph);
  resultsTab?.setAttribute('aria-selected', String(!showGraph));
  graphTab?.classList.toggle('subtab-active', showGraph);
  graphTab?.setAttribute('aria-selected', String(showGraph));
  if ($('#graphify-open-workspaces')) $('#graphify-open-workspaces').onclick = () => {
    state.settingsTab = 'workspaces';
    setView('settings');
  };
  if ($('#graphify-panel')) $('#graphify-panel').hidden = !showGraph;
  if ($('#search-results-container')) $('#search-results-container').hidden = showGraph || !state.searchResults;
  if ($('#search-welcome')) $('#search-welcome').hidden = showGraph || Boolean(state.searchQuery) || state.searchPending;
  if (status) {
    status.hidden = showGraph || (!state.searchQuery && !state.searchPending);
    const resultCount = ['context', 'tasks', 'plans', 'messages', 'events']
      .reduce((sum, key) => sum + (state.searchResults?.[key]?.length || 0), 0);
    if (state.searchPending) status.textContent = 'Searching…';
    else if (!state.searchQuery) status.textContent = 'Search tasks, plans, messages, and project context from the header.';
    else if (!state.searchResults) status.textContent = 'Search results are unavailable.';
    else {
      status.textContent = resultCount
        ? `${resultCount} ${resultCount === 1 ? 'result' : 'results'} for “${state.searchQuery}”`
        : `No results for “${state.searchQuery}”`;
      if ($('#search-results-container')) $('#search-results-container').hidden = showGraph || !resultCount;
    }
  }

  const g = state.graphify;
  const gBadge = $('#graphify-status-badge');
  const gText = $('#graphify-status-text');
  const gList = $('#graphify-nodes-list');
  if (gBadge) {
    gBadge.textContent = g?.status || 'unavailable';
    gBadge.className = `badge ${g?.status === 'ready' ? '' : g?.status === 'stale' ? 'badge-warn' : 'badge-muted'}`;
  }
  if (gText) {
    gText.textContent = g?.reason || (g?.source_path ? `Loaded ${g.node_count || 0} nodes, ${g.edge_count || 0} edges from ${g.source_path}` : 'No Graphify index detected.');
  }
  if (gList) {
    clearNode(gList);
    const nodes = g?.matched_nodes || [];
    if (!nodes.length) {
      if (g?.status !== 'unavailable' && state.searchQuery) gList.appendChild(createEl('p', 'muted small', 'No matching Graphify nodes.'));
    } else {
      for (const n of nodes) {
        const row = createEl('div', 'list-item');
        row.appendChild(createEl('div', 'list-item-title', `${n.label || n.id} (${n.type || 'node'})`));
        if (n.source_path) row.appendChild(createEl('div', 'muted small', n.source_path));
        if (n.summary) row.appendChild(createEl('p', 'small', n.summary));
        gList.appendChild(row);
      }
    }
  }

  const res = state.searchResults || { tasks: [], plans: [], messages: [], events: [] };
  const fill = (id, countId, items, renderItem) => {
    const wrap = $(id);
    if ($(countId)) $(countId).textContent = String(items.length);
    if (!wrap) return;
    wrap.closest('section').hidden = !items.length;
    clearNode(wrap);
    for (const item of items) wrap.appendChild(renderItem(item));
  };

  fill('#search-context-list', '#search-context-count', res.context || [], record => {
    const row = createEl('article', 'list-item search-result-row');
    const detail = createEl('details', '');
    detail.append(
      createEl('summary', '', record.title + ' · v' + record.revision),
      createEl('p', 'small', record.excerpt)
    );
    detail.addEventListener('toggle', async () => {
      if (!detail.open || detail.children.length > 2) return;
      try {
        const [revision] = await request(projectApi('context', { id: record.id, revision: record.revision }));
        if (!revision || !detail.isConnected) return;
        const body = createEl('div', 'markdown-body');
        renderSafeMarkdown(body, revision.body);
        detail.appendChild(body);
        if (revision.verification) detail.appendChild(createEl('p', 'agent-dm-body', revision.verification));
      } catch (error) {
        if (detail.isConnected) showError(error.message);
      }
    });
    row.appendChild(detail);
    return row;
  });

  fill('#search-tasks-list', '#search-tasks-count', res.tasks || [], t => {
    const row = createEl('div', 'list-item search-result-row');
    row.append(
      createEl('div', 'list-item-title', t.title || t.id),
      createEl('div', 'muted small', `${t.id} · ${t.status}`),
      uiButton({
        label: 'Inspect task',
        icon: 'search',
        cls: 'btn btn-xs btn-secondary icon-only-control',
        onClick: () => openTaskDetail(t.id)
      })
    );
    return row;
  });

  fill('#search-plans-list', '#search-plans-count', res.plans || [], p => {
    const row = createEl('div', 'list-item search-result-row');
    const plan = state.planHistory.find(item => item.revision === p.revision);
    const title = p.plan_title || plan?.plan_title || (p.body || '').split('\n').map(line => line.trim().replace(/^#+\s*/, '')).find(Boolean) || 'Untitled plan';
    const version = plan ? planVersionLabel(plan) : 'Revision';
    row.append(
      createEl('div', 'list-item-title', `${title} · ${version}`),
      createEl('p', 'small search-result-excerpt', (p.body || '').slice(0, 180)),
      uiButton({
        label: 'Open plan',
        icon: 'plans',
        cls: 'btn btn-xs btn-secondary icon-only-control',
        onClick: () => {
          state.selectedPlanID = p.plan_id || plan?.plan_id || '';
          state.selectedPlanRev = p.revision;
          state.planTab = 'reader';
          state.planPane = 'reader';
          setView('plans');
        }
      })
    );
    return row;
  });

  fill('#search-messages-list', '#search-messages-count', res.messages || [], m => {
    const row = createEl('div', 'list-item search-result-row');
    row.append(
      createEl('div', 'list-item-title', `${principalLabel(m.sender)} ${m.recipient ? '(DM)' : '(# general)'}`),
      createEl('p', 'small chat-result-excerpt', m.subject || m.body || ''),
      uiButton({
        label: 'Open message',
        icon: 'chat',
        cls: 'btn btn-xs btn-secondary icon-only-control',
        onClick: () => openSourceMessage(m.id)
      })
    );
    return row;
  });

  fill('#search-events-list', '#search-events-count', res.events || [], ev => {
    const row = createEl('div', 'list-item search-result-row');
    row.append(
      createEl('div', 'list-item-title', `${ev.kind} · #${ev.sequence}`),
      createEl('div', 'muted small', ev.summary || formatTime(ev.created_at))
    );
    return row;
  });
}

function wireAgentEvents() {
  $$('#agents-view-tabs .subtab').forEach(btn => {
    btn.addEventListener('click', () => {
      state.agentsTab = btn.dataset.agentTab || 'all';
      renderAgentsView();
    });
  });

  for (const tab of ['results', 'graph']) {
    $('#search-' + tab + '-tab')?.addEventListener('click', () => {
      state.searchTab = tab;
      syncUrlState();
      renderSearchView();
    });
  }

  $('#role-preset')?.addEventListener('change', e => {
    const preset = state.rolePresets.find(p => p.id === e.target.value);
    if (!preset) return;
    if ($('#role-id')) $('#role-id').value = preset.id || '';
    if ($('#role-name')) $('#role-name').value = preset.name || '';
    if ($('#role-body')) $('#role-body').value = preset.body || '';
  });

  $('#role-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    try {
      await request(projectApi('roles'), {
        method: 'POST',
        body: {
          recipient: $('#role-agent')?.value || '',
          id: $('#role-id')?.value?.trim() || '',
          name: $('#role-name')?.value?.trim() || '',
          body: $('#role-body')?.value || ''
        }
      });
      closeDialog('work-roles-section');
      await refresh();
    } catch (err) {
      showError(err.message);
    }
  });

  $('#agent-parent-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const form = event.currentTarget;
    const submit = form.querySelector('[type=submit]');
    submit.disabled = true;
    $('#agent-parent-error').textContent = '';
    try {
      await request(projectApi('agents'), {
        method: 'POST',
        body: {
          operation: 'parent',
          id: form.elements.child_id.value,
          parent_id: form.elements.parent_id.value
        }
      });
      state.agents = await request(projectApi('agents'));
      closeDialog('agent-parent-dialog');
      renderAgentsView();
    } catch (error) {
      $('#agent-parent-error').textContent = error.message;
    } finally {
      submit.disabled = false;
    }
  });
}
