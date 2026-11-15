'use strict';

function buildTaskCard(t) {
  const contract = t.report_contract || t.contract || {};
  const card = createEl('div', 'list-item shared-task');
  const kanban = state.todoLayout === 'kanban';
  const taskPlan = state.planHistory.find(item => item.revision === Number(t.plan_revision));
  const taskPlanThread = taskPlan && state.planThreads.find(item => item.id === taskPlan.plan_id);

  const top = createEl('div', 'list-item-row shared-task-head');
  const title = createEl('div', 'task-checklist-title');
  if (!kanban) title.appendChild(completionMark(t.status === 'accepted'));
  const pills = createEl('div', 'btn-row task-flairs');
  pills.appendChild(uiBadge(taskStatusLabels[t.status] || t.status));
  if (t.assigned_to) pills.appendChild(uiBadge(principalLabel(t.assigned_to), 'you'));
  title.appendChild(uiButton({
    label: t.title || t.id,
    cls: 'task-title-button',
    onClick: () => openTaskDetail(t.id)
  }));
  top.appendChild(title);

  const actions = createEl('div', 'btn-row task-side-actions');
  const addTaskAction = (label, operation, fields = {}, icon = '', shortLabel = label) => {
    const button = uiButton({
      label,
      shortLabel,
      icon,
      cls: `btn btn-xs btn-secondary${icon ? ' task-icon-action icon-only-control' : ''}`,
      title: label,
      onClick: async () => {
        const generation = state.generation;
        await runGuardedAction(button, async () => {
          await request(projectApi('tasks'), { method: 'POST', body: { id: t.id, operation, ...fields } });
          if (generation === state.generation) {
            await loadTasksRouteData(generation);
            if (generation === state.generation) render();
          }
        }, { onError: error => { if (generation === state.generation) showError(error.message); } });
      }
    });
    actions.appendChild(button);
  };

  if (t.status === 'ready' && canClaimTask() && (!t.assigned_to || t.assigned_to === state.project?.id)) {
    addTaskAction('Claim task', 'claim', {}, 'user-check');
  }
  if (t.status === 'needs_replan' && canDelegateTask()) {
    addTaskAction('Reconcile with current plan', 'reconcile', { revision: state.plan?.revision || 0 }, 'refresh', 'Reconcile');
  }

  if (kanban) {
    top.classList.add('is-kanban-task-head');
    title.classList.add('is-kanban-task-title');
    top.appendChild(title);
    if (taskPlan) {
      const planLabel = taskPlanThread?.title || taskPlan.plan_title || 'Plan';
      top.appendChild(createEl('div', 'task-kanban-plan muted small', `${planLabel} · ${planVersionLabel(taskPlan)}`));
    }
    const footer = createEl('div', 'task-kanban-footer');
    footer.appendChild(pills);
    if (actions.childElementCount) footer.appendChild(actions);
    top.appendChild(footer);
  } else {
    top.append(pills, actions);
  }
  card.appendChild(top);

  if (t.blocker_reason) card.appendChild(createEl('p', 'muted small', `Blocker: ${t.blocker_reason}`));

  const reqParts = [];
  if (contract.required_role) reqParts.push(`Role: ${contract.required_role}`);
  if (Array.isArray(contract.required_capabilities) && contract.required_capabilities.length) {
    reqParts.push(`Capabilities: ${contract.required_capabilities.join(', ')}`);
  }
  if (reqParts.length) {
    card.appendChild(createEl('div', 'muted small', `${reqParts.join(' · ')} — Host reported (unverified)`));
  }

  return card;
}

function renderPersonalChecklists(current, scope) {
  const checklist = $('#personal-checklists-list');
  const picker = $('#personal-agent');
  if (!checklist || !picker) return;
  const agents = new Map([[current, 'You']]);
  for (const agent of state.agents) if (agent.id !== current) agents.set(agent.id, principalLabel(agent.id));
  for (const item of state.checklists.items || []) if (!agents.has(item.agent_id)) agents.set(item.agent_id, principalLabel(item.agent_id));
  const selected = scope === 'agent' && agents.has(state.todoAgent) ? state.todoAgent : current;
  picker.replaceChildren(...Array.from(agents, ([id, label]) => new Option(label, id)));
  picker.value = selected;
  if ($('#personal-item-form')) $('#personal-item-form').hidden = !state.checklists.available || selected !== current;
  const items = (state.checklists.items || []).filter(item => item.agent_id === selected);
  if ($('#personal-checklists-count')) $('#personal-checklists-count').textContent = String(items.length);
  if ($('#personal-checklists-note')) $('#personal-checklists-note').textContent = state.checklists.available ? '' : (state.checklists.reason || '');
  clearNode(checklist);
  const board = state.todoLayout === 'kanban';
  checklist.classList.toggle('todo-board', board);
  const lanes = new Map();
  if (board) {
    for (const [key, name] of [['todo', 'To do'], ['doing', 'Doing'], ['done', 'Done']]) {
      const lane = createEl('section', 'todo-lane');
      lane.appendChild(createEl('h4', '', name));
      checklist.appendChild(lane);
      lanes.set(key, lane);
    }
  }
  if (!items.length && !board) checklist.appendChild(createEl('p', 'muted', 'No personal to-dos yet.'));
  for (const item of items) {
    const row = createEl('div', 'list-item');
    const title = createEl('div', 'task-checklist-title');
    const editable = item.agent_id === current && item.source === 'coordinator';
    const saveStatus = async (status, control, revert) => {
      const generation = state.generation;
      control.disabled = true;
      try {
        await request(projectApi('personal-items'), { method: 'POST', body: { id: item.id, title: item.title, notes: item.notes, status } });
        if (generation !== state.generation) return;
        await loadTasksRouteData(generation);
        if (generation === state.generation) renderTodosView();
      } catch (error) {
        revert();
        if (generation === state.generation) showError(error.message);
      } finally {
        control.disabled = false;
      }
    };
    if (!board && editable) {
      const label = createEl('label', 'task-checklist-title');
      const check = createEl('input', 'task-checkbox');
      check.type = 'checkbox';
      check.checked = item.status === 'done';
      check.addEventListener('change', () => saveStatus(check.checked ? 'done' : 'todo', check, () => { check.checked = item.status === 'done'; }));
      label.append(check, createEl('strong', '', item.title || item.id));
      title.appendChild(label);
    } else {
      if (!board) title.appendChild(completionMark(item.status === 'done'));
      title.appendChild(createEl('strong', '', item.title || item.id));
    }
    const head = createEl('div', 'list-item-row');
    head.appendChild(title);
    if (editable) {
      const controls = createEl('div', 'btn-row');
      controls.appendChild(uiButton({
        label: 'Edit',
        cls: 'btn btn-xs btn-secondary',
        onClick: () => {
          const form = $('#personal-edit-form');
          form.elements.item_id.value = item.id;
          form.elements.title.value = item.title;
          form.elements.notes.value = item.notes || '';
          form.elements.status.value = item.status;
          form.dataset.project = projectApi('personal-items');
          openDialog('personal-edit-dialog');
        }
      }));
      const select = createEl('select', 'input select-compact');
      select.setAttribute('aria-label', 'Status for ' + item.title);
      select.replaceChildren(new Option('To do', 'todo'), new Option('Doing', 'doing'), new Option('Done', 'done'));
      select.value = item.status;
      select.addEventListener('change', () => saveStatus(select.value, select, () => { select.value = item.status; }));
      controls.appendChild(select);
      head.appendChild(controls);
    } else {
      head.appendChild(uiBadge(
        item.source === 'coordinator' ? item.status : 'Imported · ' + item.status,
        item.status === 'done' ? 'ok' : 'muted'
      ));
    }
    row.appendChild(head);
    if (item.notes) row.appendChild(createEl('p', 'muted small', item.notes));
    (lanes.get(item.status) || lanes.get('todo') || checklist).appendChild(row);
  }
  if (board) {
    for (const lane of lanes.values()) {
      if (!lane.querySelector('.list-item')) lane.appendChild(createEl('p', 'kanban-empty muted small', 'No items'));
    }
  }
}

function renderTodosView() {
  const filterCount = Object.values(state.taskFilters).filter(Boolean).length + Number(Boolean(state.todoRevision));
  if ($('#todo-filter-count')) {
    $('#todo-filter-count').hidden = !filterCount;
    $('#todo-filter-count').textContent = String(filterCount);
  }
  if ($('#filter-status')) $('#filter-status').value = state.taskFilters.status || '';
  if ($('#filter-kind')) $('#filter-kind').value = state.taskFilters.kind || '';
  if ($('#filter-query')) $('#filter-query').value = state.taskFilters.query || '';

  populateSelect($('#filter-assignee'), state.agents, {
    placeholder: 'All agents',
    getValue: a => a.id,
    getLabel: a => `${principalLabel(a.id, a.name)} (${a.role})`,
    selectedValue: state.taskFilters.assignee || ''
  });

  const current = state.project?.id || '';
  const scope = state.todoScope;
  const goalTab = scope === 'project' ? (state.todoGoalTab || 'all') : 'all';
  if ($('#todo-scope')) $('#todo-scope').value = scope;
  $$('#todo-scope-tabs .subtab').forEach(btn => {
    const btnScope = btn.dataset.todoScope || 'project';
    const btnGoal = btn.dataset.todoGoal || 'all';
    const selected = btnScope === scope && (scope !== 'project' || btnGoal === goalTab);
    btn.classList.toggle('subtab-active', selected);
    btn.setAttribute('aria-selected', String(selected));
  });
  const visibleTasks = state.tasks.filter(task => {
    const rev = Number(task.plan_revision || 0);
    if (goalTab === 'plan') return rev > 0;
    if (goalTab === 'global') return rev === 0;
    return true;
  });
  const revision = $('#filter-revision');
  if (revision) {
    revision.replaceChildren(
      new Option('All plan revisions', ''),
      ...state.planHistory.map(plan => new Option(`${plan.plan_title || 'Plan'} · ${planVersionLabel(plan)}`, plan.revision))
    );
    if (state.todoRevision && !state.planHistory.some(plan => plan.revision === state.todoRevision)) {
      revision.append(new Option('Selected plan revision', state.todoRevision));
    }
    revision.value = state.todoRevision || '';
  }
  if ($('#open-task-dialog-btn')) $('#open-task-dialog-btn').hidden = !canDelegateTask();
  if ($('#todo-agent-field')) $('#todo-agent-field').hidden = scope !== 'agent';
  if ($('#filter-assignee')) $('#filter-assignee').hidden = scope !== 'project';
  if ($('#personal-todos-section')) $('#personal-todos-section').hidden = scope === 'project';
  if ($('#shared-todos-title')) {
    const scopeTitle = scope === 'mine' ? 'My work' : scope === 'agent' ? 'Assigned tasks' : 'Project tasks';
    $('#shared-todos-title').textContent = goalTab === 'global'
      ? 'Global goals'
      : goalTab === 'plan'
        ? 'Plan goals'
        : scopeTitle;
  }
  if ($('#quick-task-disclosure')) $('#quick-task-disclosure').hidden = scope !== 'project' || !canDelegateTask();

  const projWrap = $('#project-todos-list');
  if ($('#project-todos-count')) {
    $('#project-todos-count').textContent = goalTab === 'all' ? String(state.taskTotal || visibleTasks.length) : String(visibleTasks.length);
  }
  if (projWrap) {
    clearNode(projWrap);
    const board = state.todoLayout === 'kanban';
    projWrap.classList.toggle('todo-board', board);
    $('#todo-checklist')?.classList.toggle('is-active', !board);
    $('#todo-checklist')?.setAttribute('aria-pressed', String(!board));
    $('#todo-kanban')?.classList.toggle('is-active', board);
    $('#todo-kanban')?.setAttribute('aria-pressed', String(board));
    if (board) {
      const columns = [
        ['Ready', ['ready', 'open'], true],
        ['In progress', ['running'], true],
        ['In review', ['submitted', 'review_requested'], true],
        ['Done', ['accepted'], true],
        ['Blocked', ['blocked'], false],
        ['Needs replan', ['needs_replan'], false]
      ];
      const known = new Set(columns.flatMap(([, statuses]) => statuses));
      const other = visibleTasks.filter(task => !known.has(task.status));
      if (other.length) columns.push(['Other', other.map(task => task.status), false]);
      for (const [label, statuses, alwaysShow] of columns) {
        const tasks = visibleTasks.filter(task => statuses.includes(task.status));
        if (!alwaysShow && !tasks.length) continue;
        const lane = createEl('section', 'todo-lane');
        lane.appendChild(createEl('h4', '', label + ' · ' + tasks.length));
        if (!tasks.length) lane.appendChild(createEl('p', 'kanban-empty muted small', 'No tasks'));
        for (const task of tasks) lane.appendChild(buildTaskCard(task));
        projWrap.appendChild(lane);
      }
    } else {
      const planGroups = new Map();
      for (const task of visibleTasks) {
        const rev = Number(task.plan_revision) || 0;
        const plan = rev > 0 ? state.planHistory.find(item => item.revision === rev) : null;
        const planID = plan?.plan_id || (rev === 0 ? 'global-goals' : `unlinked-${rev}`);
        if (!planGroups.has(planID)) planGroups.set(planID, { plan, rev, tasks: [] });
        planGroups.get(planID).tasks.push(task);
      }
      if (scope === 'project' && (goalTab === 'all' || goalTab === 'global') && !planGroups.has('global-goals')) {
        planGroups.set('global-goals', { plan: null, rev: 0, tasks: [] });
      }
      if (!planGroups.size) {
        const emptyMsg = goalTab === 'plan'
          ? 'No plan-based tasks match the current filter.'
          : 'No project to-dos match the current filter.';
        projWrap.appendChild(createEl('p', 'muted small', emptyMsg));
      }
      const planIDs = [...planGroups.keys()].sort((a, b) => (planGroups.get(b).plan?.revision || 0) - (planGroups.get(a).plan?.revision || 0));
      for (const planID of planIDs) {
        const { plan, rev, tasks } = planGroups.get(planID);
        const group = createEl('details', 'task-plan-group');
        group.open = true;
        const summary = createEl('summary', 'task-plan-heading');
        const thread = plan && state.planThreads.find(item => item.id === plan.plan_id);
        const heading = plan
          ? (thread?.title || plan.plan_title || plan.body?.split('\n').map(line => line.trim().replace(/^#+\s*/, '')).find(Boolean) || 'Plan')
          : (rev === 0 ? 'Global goals' : 'Unlinked tasks');
        const label = createEl('span', 'task-plan-label');
        label.appendChild(createEl('span', '', heading));
        if (thread) label.appendChild(uiBadge(thread.status === 'done' ? 'Done' : 'Active', '', thread.status === 'done' ? 'plan-status-done' : 'plan-status-active'));
        else if (rev === 0) label.appendChild(uiBadge('Project-wide', 'muted'));
        summary.append(label, uiBadge(String(tasks.length), 'muted'));
        group.appendChild(summary);
        const list = createEl('div', 'task-plan-items');
        if (!tasks.length) {
          list.appendChild(createEl('p', 'muted small todo-group-empty', 'No global project goals yet. Create one with New task → Global goal.'));
        } else {
          for (const task of tasks) list.appendChild(buildTaskCard(task));
        }
        group.appendChild(list);
        projWrap.appendChild(group);
      }
    }
  }

  const loadMore = $('#todos-load-more-btn');
  if (loadMore) loadMore.hidden = !state.taskCursor;
  renderPersonalChecklists(current, scope);
}

async function appendReportForm(parent, task, generation) {
  if (task.status !== 'running' || task.owner !== state.project?.id || !canSubmitReport()) return;
  if (task.report_contract?.review_type === 'full_code') {
    parent.appendChild(createEl('p', 'muted small', 'Submit full-code work through Gossip MCP from the claimed checkout so the report can include the captured source diff.'));
    return;
  }
  const attempt = await request(projectApi('attempt', { id: task.id, number: task.attempt }));
  if (generation !== state.generation || !parent.isConnected) return;

  const form = createEl('form', 'report-form stack-list');
  const baseline = createEl('input', 'input');
  const report = createEl('textarea', 'input');
  const error = createEl('p', 'muted small');
  const submit = uiButton({ label: 'Submit report', cls: 'btn btn-primary', type: 'submit' });
  baseline.required = true;
  baseline.maxLength = 4096;
  baseline.value = attempt.workspace_head ? 'commit:' + attempt.workspace_head : '';
  baseline.readOnly = Boolean(attempt.workspace_head);
  report.required = true;
  report.rows = 8;
  error.setAttribute('role', 'alert');
  const baselineField = createEl('label', 'field');
  baselineField.append(createEl('span', '', 'Baseline'), baseline);
  const reportField = createEl('label', 'field');
  reportField.append(createEl('span', '', 'Report in Markdown'), report);
  form.append(createEl('h4', '', 'Submit work'), baselineField, reportField);
  if (task.report_contract?.review_type === 'diagram_report') {
    const headings = ['Review summary', 'Scope and evidence', 'Findings', 'Coverage and limitations', 'UML class diagram', 'Sequence diagram'];
    const template = uiButton({
      label: 'Insert report outline',
      cls: 'btn btn-secondary',
      onClick: () => {
        const outline = headings.map(title => '## ' + title + '\n\n').join('');
        report.value += (report.value.trim() ? '\n\n' : '') + outline;
        report.focus();
      }
    });
    form.append(createEl('p', 'muted small', 'Fill out each section in the outline. Use Mermaid code blocks for the class and sequence diagrams.'), template);
  }
  form.append(error, submit);
  const endpoint = projectApi('reports');
  let previous;
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (generation !== state.generation || submit.disabled) return;
    const payload = { id: task.id, attempt: task.attempt, body: report.value, baseline: baseline.value.trim() };
    const serialized = JSON.stringify(payload);
    if (serialized !== previous?.serialized) previous = { serialized, requestID: crypto.randomUUID() };
    submit.disabled = true;
    error.textContent = '';
    try {
      await request(endpoint, { method: 'POST', body: { ...payload, request_id: previous.requestID } });
      if (generation !== state.generation) return;
      closeDialog('detail-dialog');
      await refresh();
    } catch (failure) {
      error.textContent = failure.message;
    } finally {
      submit.disabled = false;
    }
  });
  parent.appendChild(form);
}

function appendTaskAssignment(parent, t, generation) {
  const eligible = t.status === 'ready' && canDelegateTask() ? eligibleAssignees(t) : [];
  if (!eligible.length) return;
  const assignSel = createEl('select', 'input select-compact');
  assignSel.setAttribute('aria-label', 'Select an online agent reporting all required capabilities');
  populateSelect(assignSel, eligible, {
    placeholder: 'Assign to…',
    getValue: a => a.id,
    getLabel: a => a.name
  });
  const assignBtn = uiButton({
    label: 'Assign',
    cls: 'btn btn-xs btn-primary',
    disabled: eligible.length === 0,
    onClick: async () => {
      if (!assignSel.value || generation !== state.generation) return;
      try {
        await request(projectApi('tasks'), { method: 'POST', body: { operation: 'assign', id: t.id, recipient: assignSel.value } });
        if (generation === state.generation) {
          closeDialog('detail-dialog');
          await refresh();
        }
      } catch (err) {
        showError(err.message);
      }
    }
  });
  const row = createEl('div', 'task-assignment');
  row.append(assignSel, assignBtn);
  parent.appendChild(row);
}

async function openTaskDetail(taskID) {
  const generation = state.generation;
  try {
    const t = await request(projectApi('task', { id: taskID }));
    if (generation !== state.generation) return;
    $('#detail-title').textContent = t.title || t.id;
    const body = $('#detail-body');
    clearNode(body);
    const taskPlan = state.planHistory.find(item => item.revision === Number(t.plan_revision));
    const taskThread = taskPlan && state.planThreads.find(item => item.id === taskPlan.plan_id);
    const planLabel = taskPlan
      ? `${taskThread?.title || taskPlan.plan_title || 'Plan'} · ${planVersionLabel(taskPlan)}`
      : (Number(t.plan_revision || 0) === 0 ? 'Global goal' : 'Unlinked plan');
    const statusText = taskStatusLabels[t.status] || (t.status || 'ready').replaceAll('_', ' ');
    const statusVariant = t.status === 'accepted' ? 'ok' : ['blocked', 'needs_replan', 'cancelled'].includes(t.status) ? 'warn' : 'muted';
    const metaRow = createEl('div', 'detail-meta-row');
    metaRow.append(
      uiBadge(statusText, statusVariant),
      uiBadge(t.kind || 'implementation', 'muted'),
      uiBadge(planLabel, 'muted'),
      createEl('span', 'muted small', `Attempt ${t.attempt || 0}`),
      createEl('code', 'detail-id-chip', t.id)
    );
    body.appendChild(metaRow);
    body.appendChild(createEl('p', 'small', 'Review · ' + taskContractReviewLabel(t)));
    appendTaskAssignment(body, t, generation);
    if (typeof renderContextTarget === 'function') void renderContextTarget(body, 'task', t.id, t.context_links || []);
    if (t.description) {
      const md = createEl('div', 'markdown-body');
      renderSafeMarkdown(md, t.description);
      body.appendChild(md);
    }
    try {
      await appendReportForm(body, t, generation);
    } catch (error) {
      if (generation === state.generation) body.appendChild(createEl('p', 'muted small', error.message));
    }
    try {
      const rep = t.attempt > 0 && ['submitted', 'accepted'].includes(t.status)
        ? await request(projectApi('report', { task: t.id, attempt: t.attempt }))
        : null;
      if (generation !== state.generation) return;
      if (rep && rep.body) {
        body.appendChild(createEl('h4', '', 'Report for attempt ' + rep.attempt));
        const repBox = createEl('div', 'markdown-body preview-box');
        renderSafeMarkdown(repBox, rep.body);
        body.appendChild(repBox);
        if (rep.id && typeof renderContextTarget === 'function') void renderContextTarget(body, 'review', rep.id);
        if (t.status === 'submitted' && rep.author !== state.project?.id && canDecideReview()) {
          const revRow = createEl('div', 'btn-row');
          revRow.append(
            uiButton({
              label: 'Accept report',
              cls: 'btn btn-sm btn-primary',
              onClick: async () => {
                try {
                  await request(projectApi('review'), { method: 'POST', body: { operation: 'accept', id: rep.id, digest: rep.digest } });
                  closeDialog('detail-dialog');
                  await refresh();
                } catch (err) {
                  showError(err.message);
                }
              }
            }),
            uiButton({
              label: 'Request rework',
              cls: 'btn btn-sm btn-danger',
              onClick: async () => {
                const feedback = window.prompt('Describe required rework:', '')?.trim();
                if (!feedback) return;
                try {
                  await request(projectApi('review'), { method: 'POST', body: { operation: 'rework', id: rep.id, digest: rep.digest, body: feedback } });
                  closeDialog('detail-dialog');
                  await refresh();
                } catch (err) {
                  showError(err.message);
                }
              }
            })
          );
          body.appendChild(revRow);
        }
      }
    } catch (error) {
      if (generation === state.generation) body.appendChild(createEl('p', 'muted small', 'Could not load the report. ' + error.message));
    }
    if (generation === state.generation) openDialog('detail-dialog');
  } catch (err) {
    showError(err.message);
  }
}

function wireTodoEvents() {
  $('#task-dialog')?.addEventListener('close', () => {
    $('#task-create-error').textContent = '';
    $('#task-advanced').open = false;
  });
  $('#open-task-dialog-btn')?.addEventListener('click', () => {
    $('#task-form')?.reset();
    if ($('#task-plan-scope')) {
      $('#task-plan-scope').value = state.todoGoalTab === 'global' || !state.plan?.revision ? 'global' : 'plan';
    }
    openDialog('task-dialog');
  });
  $('#quick-task-form')?.addEventListener('submit', event => {
    event.preventDefault();
    const title = $('#quick-task-title').value.trim();
    if (!title) return;
    $('#task-form').reset();
    $('#task-title').value = title;
    $('#task-kind').value = $('#quick-task-kind').value;
    if ($('#task-plan-scope')) {
      $('#task-plan-scope').value = state.todoGoalTab === 'global' || !state.plan?.revision ? 'global' : 'plan';
    }
    openDialog('task-dialog');
    $('#task-description').focus();
  });
  $('#task-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    const submit = e.currentTarget.querySelector('[type=submit]');
    if (submit.disabled) return;
    submit.disabled = true;
    const feedback = $('#task-create-error');
    feedback.textContent = '';
    const deps = ($('#task-deps')?.value || '').split(',').map(s => s.trim()).filter(Boolean);
    const reviewType = $('#task-review-type').value;
    const caps = ($('#task-required-caps')?.value || '').split(',').map(s => s.trim()).filter(Boolean);
    const goalScope = $('#task-plan-scope')?.value || 'plan';
    try {
      await request(projectApi('tasks'), {
        method: 'POST',
        body: {
          operation: 'create',
          title: $('#task-title')?.value?.trim() || '',
          kind: $('#task-kind').value,
          description: $('#task-description')?.value || '',
          revision: goalScope === 'global' ? 0 : (state.plan?.revision || 0),
          dependencies: deps,
          contract: {
            review_type: reviewType,
            formats: reviewType === 'diagram_report' ? ['markdown', 'mermaid_sequence', 'mermaid_class'] : [],
            required_role: $('#task-required-role')?.value?.trim() || '',
            required_capabilities: caps
          },
          request_id: `task-${Date.now()}`
        }
      });
      closeDialog('task-dialog');
      $('#task-form').reset();
      if ($('#quick-task-title')) $('#quick-task-title').value = '';
      await refresh();
    } catch (err) {
      feedback.textContent = err.message;
    } finally {
      submit.disabled = false;
    }
  });

  const applyTodoFilterChange = async () => {
    state.taskFilters = {
      status: $('#filter-status')?.value || '',
      kind: $('#filter-kind')?.value || '',
      assignee: $('#filter-assignee')?.value || '',
      query: $('#filter-query')?.value?.trim() || ''
    };
    syncUrlState();
    state.generation += 1;
    try {
      await loadTaskSearchOnly(state.generation, false);
      if (state.view === 'todos') renderTodosView();
    } catch (error) {
      showError(error.message);
    }
  };
  $('#filter-revision')?.addEventListener('change', event => {
    state.todoRevision = Number(event.target.value) || 0;
    applyTodoFilterChange();
  });
  $('#filter-status')?.addEventListener('change', applyTodoFilterChange);
  $('#filter-kind')?.addEventListener('change', applyTodoFilterChange);
  $('#filter-assignee')?.addEventListener('change', applyTodoFilterChange);
  $('#todo-filters')?.addEventListener('submit', e => {
    e.preventDefault();
    applyTodoFilterChange();
  });
  $('#filter-reset-btn')?.addEventListener('click', () => {
    state.todoRevision = 0;
    if ($('#filter-status')) $('#filter-status').value = '';
    if ($('#filter-kind')) $('#filter-kind').value = '';
    if ($('#filter-assignee')) $('#filter-assignee').value = '';
    if ($('#filter-query')) $('#filter-query').value = '';
    applyTodoFilterChange();
  });

  const loadTodoScope = async () => {
    const generation = ++state.generation;
    try {
      await loadTasksRouteData(generation);
      if (generation === state.generation) render();
    } catch (error) {
      if (generation === state.generation) showError(error.message);
    }
  };
  const applyTodoScope = (nextScope, nextGoal = 'all') => {
    const scopeChanged = state.todoScope !== nextScope;
    state.todoScope = nextScope;
    state.todoGoalTab = nextScope === 'project' ? nextGoal : 'all';
    if (state.todoScope === 'agent' && !state.agents.some(agent => agent.id === state.todoAgent)) {
      state.todoAgent = state.agents.find(agent => agent.id !== state.project?.id)?.id || '';
    }
    syncUrlState();
    if (scopeChanged) loadTodoScope();
    else renderTodosView();
  };
  $$('#todo-scope-tabs .subtab').forEach(btn => {
    btn.addEventListener('click', () => applyTodoScope(btn.dataset.todoScope || 'project', btn.dataset.todoGoal || 'all'));
  });
  $('#todo-scope')?.addEventListener('change', event => applyTodoScope(event.target.value));
  for (const layout of ['checklist', 'kanban']) {
    $('#todo-' + layout)?.addEventListener('click', () => {
      state.todoLayout = layout;
      syncUrlState();
      renderTodosView();
    });
  }
  $('#personal-agent')?.addEventListener('change', event => {
    state.todoAgent = event.target.value;
    syncUrlState();
    loadTodoScope();
  });
  $('#personal-item-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const input = $('#personal-item-title');
    try {
      await request(projectApi('personal-items'), { method: 'POST', body: { title: input.value, status: 'todo' } });
      input.value = '';
      await loadTasksRouteData(state.generation);
      renderTodosView();
    } catch (error) {
      showError(error.message);
    }
  });
  $('#personal-edit-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const form = event.currentTarget;
    const endpoint = projectApi('personal-items');
    const error = $('#personal-edit-error');
    if (endpoint !== form.dataset.project) {
      error.textContent = 'The active project changed. Reopen this item to edit it.';
      return;
    }
    const button = form.querySelector('[type=submit]');
    button.disabled = true;
    error.textContent = '';
    try {
      await request(endpoint, {
        method: 'POST',
        body: {
          id: form.elements.item_id.value,
          title: form.elements.title.value,
          notes: form.elements.notes.value,
          status: form.elements.status.value
        }
      });
      closeDialog('personal-edit-dialog');
      if (endpoint === projectApi('personal-items')) await loadTodoScope();
    } catch (failure) {
      error.textContent = failure.message;
    } finally {
      button.disabled = false;
    }
  });
  $('#todos-load-more-btn')?.addEventListener('click', async () => {
    if (!state.taskCursor) return;
    state.generation += 1;
    await loadTasksRouteData(state.generation, true);
    render();
  });
}
