'use strict';

let contextDraft = null;
let variantLoad = 0;

function resetPlanUiState() {
  contextDraft = null;
  variantLoad += 1;
  if ($('#plan-variant-form')) $('#plan-variant-form').dataset.revision = '';
  if ($('#plan-variants > details')) $('#plan-variants > details').open = false;
}

function computeSimpleLineDiff(oldText, newText) {
  const a = String(oldText || '').split('\n');
  const b = String(newText || '').split('\n');
  const lines = [];
  const max = Math.max(a.length, b.length);
  for (let i = 0; i < max; i++) {
    const left = a[i];
    const right = b[i];
    if (left === right) {
      if (left !== undefined) lines.push({ type: 'ctx', text: `  ${left}` });
    } else {
      if (left !== undefined) lines.push({ type: 'del', text: `- ${left}` });
      if (right !== undefined) lines.push({ type: 'add', text: `+ ${right}` });
    }
  }
  return lines;
}

function openContextEditor(record = null) {
  const form = $('#context-form');
  contextDraft = { id: record?.id || '', revision: record?.revision || 0, project: state.project };
  form.reset();
  form.elements.kind.disabled = Boolean(record);
  if (record) {
    for (const name of ['kind', 'title', 'body', 'verification', 'source', 'status']) {
      form.elements[name].value = record[name];
    }
  }
  $('#context-error').textContent = '';
  openDialog('context-dialog');
}

function renderProjectContext() {
  const list = $('#context-list');
  const edit = canEditPlan();
  if ($('#context-add')) {
    $('#context-add').hidden = !edit;
    $('#context-add').onclick = () => openContextEditor();
  }
  const filter = state.contextFilter || 'all';
  $$('[data-context-filter]').forEach(btn => {
    btn.setAttribute('aria-pressed', String((btn.dataset.contextFilter || 'all') === filter));
  });
  if ($('#context-filter-select')) $('#context-filter-select').value = filter;
  const records = filter === 'all'
    ? (state.contextRecords || [])
    : (state.contextRecords || []).filter(r => r.kind === filter);
  const emptyLabels = {
    story: 'No user stories yet.',
    invariant: 'No invariants yet.',
    decision: 'No decisions yet.'
  };
  renderList(
    list,
    records,
    state.contextError || emptyLabels[filter] || 'No user stories, invariants, or decisions yet.',
    record => {
      const row = createEl('article', 'list-item');
      const head = createEl('div', 'list-item-row');
      const headLeft = createEl('div', 'stack-list');
      const badges = createEl('div', 'btn-row');
      badges.append(
        uiBadge(({ story: 'User story', invariant: 'Invariant', decision: 'Decision' })[record.kind] || record.kind, 'muted'),
        uiBadge('v' + record.revision, 'muted'),
        uiBadge(record.status || 'active', record.status === 'active' ? 'ok' : 'muted')
      );
      headLeft.append(createEl('h4', '', record.title), badges);
      head.appendChild(headLeft);
      if (edit) {
        head.appendChild(uiButton({
          label: 'Edit record',
          cls: 'btn btn-xs btn-secondary',
          onClick: () => openContextEditor(record)
        }));
      }
      row.appendChild(head);
      const body = createEl('div', 'markdown-body');
      renderSafeMarkdown(body, record.body);
      row.appendChild(body);
      if (record.verification) row.appendChild(createEl('p', 'agent-dm-body', record.verification));
      if (record.source) row.appendChild(createEl('p', 'muted small', 'Source: ' + record.source));
      const history = createEl('details', 'stack-list');
      history.appendChild(createEl('summary', '', 'Revision history'));
      history.addEventListener('toggle', async () => {
        if (!history.open || history.children.length > 1) return;
        try {
          const versions = await request(projectApi('context', { id: record.id }));
          for (const version of versions) {
            const section = createEl('details', 'list-item');
            section.appendChild(createEl('summary', '', 'v' + version.revision + ' · ' + principalLabel(version.author)));
            section.appendChild(createEl('strong', '', version.title));
            const vBody = createEl('div', 'markdown-body');
            renderSafeMarkdown(vBody, version.body || '');
            section.appendChild(vBody);
            if (version.verification) section.appendChild(createEl('p', 'agent-dm-body', version.verification));
            history.appendChild(section);
          }
        } catch (error) {
          showError(error.message);
        }
      });
      row.appendChild(history);
      return row;
    },
    'muted'
  );
}

function appendPinnedContext(parent, links) {
  if (!links?.length) return;
  const section = createEl('section', 'stack-list');
  section.appendChild(createEl('h4', '', 'Pinned requirements'));
  for (const link of links) {
    const detail = createEl('details', 'list-item');
    const title = createEl('summary', '', link.context_id + ' · v' + link.revision);
    detail.appendChild(title);
    detail.addEventListener('toggle', async () => {
      if (!detail.open || detail.children.length !== 1) return;
      try {
        const [record] = await request(projectApi('context', { id: link.context_id, revision: link.revision }));
        if (!record || !detail.isConnected) return;
        title.textContent = record.title + ' · v' + record.revision;
        const content = createEl('div', 'markdown-body');
        renderSafeMarkdown(content, record.body);
        detail.appendChild(content);
        if (record.verification) detail.appendChild(createEl('p', 'agent-dm-body', record.verification));
      } catch (error) {
        if (detail.isConnected) showError(error.message);
      }
    });
    section.appendChild(detail);
  }
  parent.appendChild(section);
}

async function renderContextTarget(parent, kind, id, existing = null, readOnly = false) {
  const project = state.project;
  const section = createEl('section', 'stack-list context-target');
  parent.appendChild(section);
  const load = async () => {
    const links = existing || await request(projectApi('context-links', { target_kind: kind, target_id: id }));
    if (state.project !== project) return;
    clearNode(section);
    appendPinnedContext(section, links);
    if (readOnly || !canEditPlan()) return;
    const button = uiButton({
      label: 'Pin a requirement',
      cls: 'btn btn-sm btn-secondary context-pin-btn',
      onClick: async () => {
        button.disabled = true;
        try {
          const records = await request(projectApi('context'));
          if (state.project !== project || !section.isConnected) return;
          const available = records.filter(record =>
            record.status === 'active'
            && !links.some(link => link.context_id === record.id && link.revision === record.revision)
          );
          if (!available.length) {
            section.appendChild(createEl('p', 'muted', 'No unpinned active records. Create a record in Plans.'));
            return;
          }
          const form = createEl('form', 'btn-row');
          const select = createEl('select', 'input select-compact');
          select.setAttribute('aria-label', 'Requirement revision');
          select.replaceChildren(...available.map((record, index) => new Option(record.title + ' · v' + record.revision, String(index))));
          const save = uiButton({ label: 'Pin revision', cls: 'btn btn-sm btn-primary', type: 'submit' });
          const cancel = uiButton({ label: 'Cancel', cls: 'btn btn-sm btn-ghost', onClick: () => form.replaceWith(button) });
          form.append(select, save, cancel);
          button.replaceWith(form);
          form.onsubmit = async event => {
            event.preventDefault();
            if (state.project !== project || !section.isConnected) return;
            const record = available[Number(select.value)];
            if (!record) return;
            await runGuardedAction(save, async () => {
              await request(projectApi('context-links'), {
                method: 'POST',
                body: { target_kind: kind, target_id: id, context_id: record.id, revision: record.revision }
              });
              existing = null;
              await load();
            }, { onError: error => { if (section.isConnected) showError(error.message); } });
          };
        } catch (error) {
          if (section.isConnected) showError(error.message);
        } finally {
          button.disabled = false;
        }
      }
    });
    section.appendChild(button);
  };
  try {
    await load();
  } catch (error) {
    if (state.project === project) section.textContent = error.message;
  }
}

function selectPlanEditorPane(pane) {
  $('.plan-editor-split').dataset.pane = pane;
  $('#plan-write-tab').setAttribute('aria-pressed', String(pane === 'write'));
  $('#plan-preview-tab').setAttribute('aria-pressed', String(pane === 'preview'));
}

function openPlanEditor(body = state.plan?.body || '', newPlan = false) {
  const form = $('#plan-form');
  form.dataset.endpoint = projectApi(newPlan ? 'plan-threads' : 'plan');
  form.dataset.revision = String(state.plan?.revision || 0);
  form.dataset.operation = newPlan ? 'create' : 'publish';
  $('#new-plan-title-wrap').hidden = !newPlan;
  $('#new-plan-title').value = '';
  $('#plan-editor-title').textContent = newPlan ? 'Create independent plan' : 'Publish plan revision';
  $('#plan-conflict-banner').hidden = true;
  selectPlanEditorPane('write');
  $('#plan-editor-body').value = body;
  renderSafeMarkdown($('#plan-editor-preview'), body);
  openDialog('plan-dialog');
}

function planStatus(plan) {
  if (plan.status === 'done') return 'done';
  if (plan.revision === state.plan?.revision) return 'active';
  const thread = state.planThreads.find(item => item.id === plan.plan_id);
  // A plan's latest revision stays active within its own independent thread.
  // It is not superseded merely because another plan was created later.
  if (thread && thread.latest_revision === plan.revision) return 'active';
  return 'superseded';
}

function planStatusBadge(plan) {
  const status = planStatus(plan);
  const label = { active: 'Active', done: 'Done', archived: 'Archived', superseded: 'Older revision' }[status];
  return uiBadge(label, '', 'plan-status plan-status-' + status);
}

async function openPlanWalkthrough(plan) {
  const body = $('#plan-walkthrough-body');
  const project = state.project;
  const generation = state.generation;
  $('#plan-walkthrough-title').textContent = `${state.planThreads.find(item => item.id === plan.plan_id)?.title || 'Plan'} · ${planVersionLabel(plan)} walkthrough`;
  $('#walkthrough-progress-summary').textContent = 'Loading tasks for this plan…';
  $('#walkthrough-progress-bar').value = 0;
  clearNode($('#walkthrough-task-list'));
  $('#walkthrough-open-tasks').onclick = () => {
    if (project !== state.project) return;
    setView('todos', { revision: plan.revision });
    closeDialog('plan-walkthrough-dialog');
  };
  clearNode(body);
  renderSafeMarkdown(body, plan.body || 'This plan has no content yet.');
  openDialog('plan-walkthrough-dialog');
  try {
    const tasks = [];
    let cursor = '';
    for (let page = 0; page < 20; page++) {
      const payload = await request(projectApi('task-search', { revision: plan.revision, cursor }));
      if (generation !== state.generation || project !== state.project) return;
      tasks.push(...(Array.isArray(payload?.tasks) ? payload.tasks : []));
      cursor = payload?.next_cursor || '';
      if (!cursor) break;
    }
    const done = tasks.filter(task => task.status === 'accepted').length;
    const percent = tasks.length ? Math.round(done / tasks.length * 100) : 0;
    $('#walkthrough-progress-bar').value = percent;
    $('#walkthrough-progress-summary').textContent = tasks.length
      ? `${done} of ${tasks.length} tasks complete · ${percent}%`
      : 'No tasks are linked to this plan revision yet.';
    const list = $('#walkthrough-task-list');
    renderList(list, tasks, '', task => {
      const row = createEl('div', 'walkthrough-task');
      const open = uiButton({
        label: task.title || task.id,
        cls: 'task-title-button',
        onClick: () => openTaskDetail(task.id)
      });
      row.append(open, uiBadge(taskStatusLabels[task.status] || task.status));
      return row;
    }, { pageSize: 12, root: list?.closest('.modal-card') || null });
  } catch (error) {
    if (project !== state.project || generation !== state.generation) return;
    $('#walkthrough-progress-summary').textContent = 'Task progress could not be loaded.';
    $('#walkthrough-task-list').appendChild(createEl('p', 'muted small', error.message));
  }
}

async function renderPlanVariants(plan) {
  const load = ++variantLoad;
  const generation = state.generation;
  const panel = $('#plan-variants');
  const list = $('#plan-variant-list');
  const countBadge = $('#plan-variants-count');
  const form = $('#plan-variant-form');
  panel.hidden = !plan;
  if (countBadge) countBadge.textContent = '0';
  if (!plan) return;
  $('#plan-add-variant').hidden = !canEditPlan();
  if (form.dataset.revision !== String(plan.revision)) {
    form.reset();
    form.dataset.revision = plan.revision;
    $('#plan-add-variant').open = false;
    $('#plan-variant-error').textContent = '';
  }
  list.textContent = 'Loading variants…';
  const endpoint = projectApi('plan-variants', { revision: plan.revision });
  form.onsubmit = async event => {
    event.preventDefault();
    const submit = form.querySelector('[type=submit]');
    if (submit.disabled || generation !== state.generation || load !== variantLoad) return;
    submit.disabled = true;
    $('#plan-variant-error').textContent = '';
    try {
      const saved = await request(endpoint, {
        method: 'POST',
        body: {
          revision: plan.revision,
          name: form.elements.variantName.value,
          body: form.elements.variantBody.value
        }
      });
      if (generation === state.generation && load === variantLoad) {
        form.reset();
        $('#plan-add-variant').open = false;
        state.selectedPlanRev = plan.revision;
        state.planVariant = saved.id;
        syncUrlState();
        await renderPlanVariants(plan);
      }
    } catch (error) {
      if (generation === state.generation && load === variantLoad) $('#plan-variant-error').textContent = error.message;
    } finally {
      submit.disabled = false;
    }
  };
  try {
    const variants = await request(endpoint);
    if (generation !== state.generation || load !== variantLoad) return;
    if (countBadge) countBadge.textContent = String(variants.length);
    renderList(list, variants, 'No variants for this revision.', variant => {
      const details = createEl('details', 'list-item');
      const content = createEl('div', 'markdown-body');
      details.open = state.planVariant === variant.id;
      if (details.open) $('#plan-variants > details').open = true;
      details.addEventListener('toggle', () => {
        if (!details.isConnected || load !== variantLoad || state.view !== 'plans') return;
        if (details.open) {
          state.selectedPlanRev = plan.revision;
          state.planVariant = variant.id;
          for (const sibling of list.querySelectorAll('details')) {
            if (sibling !== details) sibling.open = false;
          }
        } else if (state.planVariant === variant.id) state.planVariant = '';
        else return;
        syncUrlState(true);
      });
      details.append(
        createEl('summary', '', variant.name),
        createEl('p', 'muted small', principalLabel(variant.author) + ' · Read only'),
        content
      );
      renderSafeMarkdown(content, variant.body);
      return details;
    });
  } catch (error) {
    if (generation === state.generation && load === variantLoad) list.textContent = error.message;
  }
}

function renderPlansView() {
  const contextVisible = state.planTab === 'context';
  const layout = $('#plan-reader-layout');
  const history = state.planHistory.filter(item => item.plan_id === state.selectedPlanID);
  const hasPlans = state.planHistory.length > 0;
  layout.hidden = contextVisible || !hasPlans;
  $('#plan-empty-state').hidden = contextVisible || hasPlans;
  $('#plan-project-context').hidden = !contextVisible;
  const tabReader = $('#plan-tab-reader');
  const tabContext = $('#plan-tab-context');
  if (tabReader) {
    tabReader.classList.toggle('subtab-active', !contextVisible);
    tabReader.setAttribute('aria-selected', String(!contextVisible));
  }
  if (tabContext) {
    tabContext.classList.toggle('subtab-active', contextVisible);
    tabContext.setAttribute('aria-selected', String(contextVisible));
  }
  const edit = $('#open-plan-dialog-btn');
  edit.hidden = contextVisible || !canEditPlan() || !hasPlans;
  const threads = state.planThreads;
  const thread = threads.find(item => item.id === state.selectedPlanID);
  const threadList = $('#plan-thread-list');
  const threadSelect = $('#plan-thread-select');
  clearNode(threadList);
  threadList.hidden = contextVisible || !hasPlans;
  appendProgressiveItems(threadList, threads, item => {
    const revisions = state.planHistory.filter(plan => plan.plan_id === item.id);
    const selected = item.id === state.selectedPlanID;
    const button = createEl('button', 'btn btn-secondary plan-thread-item');
    button.type = 'button';
    button.setAttribute('aria-pressed', String(selected));
    const statusText = item.status === 'done' ? 'Done' : item.status === 'active' ? 'Active' : 'Archived';
    button.setAttribute('aria-label', `${item.title}, ${statusText}, ${revisions.length} ${revisions.length === 1 ? 'revision' : 'revisions'}`);
    button.appendChild(createEl('span', 'plan-thread-label', item.title));
    if (item.status === 'done') button.appendChild(uiBadge('Done', '', 'plan-thread-status plan-status-done'));
    button.addEventListener('click', () => {
      state.selectedPlanID = item.id;
      state.selectedPlanRev = item.latest_revision || revisions[0]?.revision || 0;
      state.comparePlanRev = 0;
      syncUrlState();
      renderPlansView();
    });
    return button;
  }, { pageSize: 35, root: threadList });
  if (threadSelect) {
    threadSelect.replaceChildren(...threads.map(item => new Option(item.title, item.id)));
    threadSelect.value = state.selectedPlanID;
    threadSelect.hidden = contextVisible || !hasPlans;
  }
  $('#new-plan-btn').hidden = !canEditPlan() || !hasPlans;
  $('#plan-empty-create').hidden = !canEditPlan();
  if (contextVisible) {
    renderProjectContext();
    return;
  }
  if (!hasPlans) return;

  const plan = history.find(item => item.revision === state.selectedPlanRev) || history[0] || null;
  const current = plan && plan.revision === state.plan?.revision;
  const latestInThread = plan && history[0]?.revision === plan.revision;
  edit.hidden = contextVisible || !canEditPlan() || !current;
  $('span', edit).textContent = 'Edit revision';
  void renderPlanVariants(plan);
  $('#plan-related-actions').hidden = !plan;
  $('#plan-open-tasks').onclick = () => setView('todos', { revision: plan.revision });
  $('#plan-open-walkthrough').onclick = () => openPlanWalkthrough(plan);
  const comparisons = history.filter(item => item.revision !== plan?.revision);
  const comparison = comparisons.find(item => item.revision === state.comparePlanRev);
  const compare = $('#plan-compare-select');
  compare.replaceChildren(
    new Option('Compare revisions…', '0'),
    ...comparisons.map(item => new Option('Compare with ' + planVersionLabel(item), item.revision))
  );
  compare.value = String(comparison?.revision || 0);
  compare.hidden = !comparisons.length;
  const ack = $('#plan-ack-btn');
  ack.hidden = !current || plan.acknowledged;
  const done = $('#plan-complete-btn');
  done.hidden = !latestInThread || planStatus(plan) === 'done' || !canEditPlan();
  const remove = $('#plan-delete-btn');
  remove.hidden = !plan || !canEditPlan();
  $('#plan-reader-actions').hidden = done.hidden && remove.hidden;
  if ($('#plan-action-icons')) $('#plan-action-icons').hidden = $('#plan-related-actions').hidden && $('#plan-reader-actions').hidden;

  $('#plan-active-heading').textContent = plan ? (thread?.title || plan.plan_title || 'Plan') : 'No plan published';
  $('#plan-meta-line').textContent = plan
    ? principalLabel(plan.author) + ' · ' + formatTime(plan.created_at) + (current ? '' : ' · Read only')
    : 'Create a plan to share the work and its requirements.';
  const badge = $('#plan-status-badge');
  badge.hidden = !plan;
  if (plan) {
    const status = planStatusBadge(plan);
    badge.className = status.className;
    badge.textContent = status.textContent;
  }
  const links = $('#plan-context-links');
  clearNode(links);
  if (plan) void renderContextTarget(links, 'plan', String(plan.revision), null, !current);
  const content = $('#plan-view-content');
  if (plan) renderSafeMarkdown(content, plan.body);
  else clearNode(content);

  const diff = $('#plan-diff-panel');
  clearNode(diff);
  diff.hidden = !comparison;
  if (comparison) {
    for (const item of computeSimpleLineDiff(comparison.body, plan.body)) {
      diff.appendChild(createEl('div', 'diff-line-' + item.type, item.text));
    }
  }

  const version = $('#plan-version-select');
  version.replaceChildren(...history.map(item => new Option(planVersionLabel(item), String(item.revision))));
  $('#plan-version-control').hidden = !history.length;
  version.value = String(plan?.revision || '');
  layout.classList.toggle('is-empty', !history.length);
}

function wirePlanEvents() {
  for (const tab of ['reader', 'context']) {
    $('#plan-tab-' + tab)?.addEventListener('click', () => {
      state.planTab = tab;
      syncUrlState();
      renderPlansView();
    });
  }
  const setContextFilter = next => {
    state.contextFilter = next || 'all';
    renderProjectContext();
  };
  $$('[data-context-filter]').forEach(btn => {
    btn.addEventListener('click', () => setContextFilter(btn.dataset.contextFilter));
  });
  $('#context-filter-select')?.addEventListener('change', e => setContextFilter(e.target.value));
  $('#plan-version-select')?.addEventListener('change', e => {
    state.selectedPlanRev = Number(e.target.value) || 0;
    state.planVariant = '';
    syncUrlState();
    renderPlansView();
  });
  $('#new-plan-btn')?.addEventListener('click', () => openPlanEditor('', true));
  $('#plan-empty-create')?.addEventListener('click', () => openPlanEditor('', true));
  $('#plan-delete-btn')?.addEventListener('click', async () => {
    const project = state.project;
    const planID = state.selectedPlanID;
    const expectedRevision = state.plan?.revision ?? 0;
    const thread = state.planThreads.find(item => item.id === planID);
    if (!planID || !thread || !await askConfirmation(
      `Delete “${thread.title}” and all its revisions?`,
      'Tasks used only by this plan and their reports, reviews, assignments, and related records will be deleted. Tasks also linked to another plan will remain. This cannot be undone.'
    )) return;
    if (project !== state.project || planID !== state.selectedPlanID || expectedRevision !== (state.plan?.revision ?? 0)) return;
    try {
      await request(projectApi('plan'), { method: 'POST', body: { operation: 'delete_thread', id: planID, expected_revision: expectedRevision } });
      state.selectedPlanRev = 0;
      await refresh();
    } catch (error) {
      showError(error.message);
    }
  });
  $('#plan-complete-btn')?.addEventListener('click', async () => {
    const project = state.project;
    const planID = state.selectedPlanID;
    const revision = state.selectedPlanRev;
    const thread = state.planThreads.find(item => item.id === planID);
    if (!revision || !await askConfirmation('Mark “' + (thread?.title || 'this plan') + '” done?', 'The completion tag will be visible to all project members.')) return;
    if (project !== state.project || planID !== state.selectedPlanID || revision !== state.selectedPlanRev) return;
    try {
      await request(projectApi('plan'), { method: 'POST', body: { operation: 'complete', revision } });
      if (project === state.project) await refresh();
    } catch (error) {
      if (project === state.project) showError(error.message);
    }
  });
  $('#plan-compare-select')?.addEventListener('change', e => {
    state.comparePlanRev = Number(e.target.value) || 0;
    renderPlansView();
  });
  $('#plan-thread-select')?.addEventListener('change', e => {
    const item = state.planThreads.find(t => t.id === e.target.value);
    if (!item) return;
    state.selectedPlanID = item.id;
    state.selectedPlanRev = item.latest_revision || state.planHistory.find(p => p.plan_id === item.id)?.revision || 0;
    state.comparePlanRev = 0;
    syncUrlState();
    renderPlansView();
  });
  $('#plan-ack-btn')?.addEventListener('click', async () => {
    if (!state.plan?.revision) return;
    try {
      await request(projectApi('plan'), { method: 'POST', body: { operation: 'acknowledge', revision: state.plan.revision } });
      await refresh();
    } catch (err) {
      showError(err.message);
    }
  });
  $('#open-plan-dialog-btn')?.addEventListener('click', () => openPlanEditor());
  $('#context-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const draft = contextDraft;
    const form = event.currentTarget;
    const submit = form.querySelector('[type=submit]');
    const error = $('#context-error');
    if (!draft || draft.project !== state.project) {
      error.textContent = 'The active project changed. Open the editor again.';
      return;
    }
    const input = { id: draft.id, revision: draft.revision };
    for (const name of ['kind', 'title', 'body', 'verification', 'source', 'status']) {
      input[name] = form.elements[name].value;
    }
    submit.disabled = true;
    error.textContent = '';
    try {
      await request(projectApi('context'), { method: 'POST', body: input });
      if (contextDraft !== draft || draft.project !== state.project) return;
      closeDialog('context-dialog');
      await loadPlansRouteData(state.generation);
      render();
    } catch (failure) {
      if (contextDraft === draft) error.textContent = failure.message + ' Your draft is still open.';
    } finally {
      submit.disabled = false;
    }
  });
  if ($('#plan-write-tab')) $('#plan-write-tab').onclick = () => selectPlanEditorPane('write');
  if ($('#plan-preview-tab')) $('#plan-preview-tab').onclick = () => selectPlanEditorPane('preview');
  $('#plan-editor-body')?.addEventListener('input', e => {
    renderSafeMarkdown($('#plan-editor-preview'), e.target.value);
  });
  $('#plan-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const form = event.currentTarget;
    const endpoint = form.dataset.endpoint;
    const revision = Number(form.dataset.revision);
    const submit = $('#plan-submit-btn');
    if (submit.disabled || !state.project || projectApi(form.dataset.operation === 'create' ? 'plan-threads' : 'plan') !== endpoint) return;
    submit.disabled = true;
    try {
      const creating = form.dataset.operation === 'create';
      const result = await request(endpoint, {
        method: 'POST',
        body: creating
          ? { operation: 'create', name: $('#new-plan-title').value, body: $('#plan-editor-body').value, expected_revision: revision }
          : { operation: 'publish', body: $('#plan-editor-body').value, revision }
      });
      if (!state.project || projectApi(creating ? 'plan-threads' : 'plan') !== endpoint) return;
      closeDialog('plan-dialog');
      if (creating && result?.plan_id) {
        state.selectedPlanID = result.plan_id;
        state.planPane = 'reader';
      }
      state.selectedPlanRev = creating ? result?.revision || 0 : 0;
      await refresh();
    } catch (error) {
      if (!state.project || projectApi(form.dataset.operation === 'create' ? 'plan-threads' : 'plan') !== endpoint) return;
      const banner = $('#plan-conflict-banner');
      if (error.status === 409) {
        banner.textContent = 'A newer revision is available. Copy or save your draft before closing this editor to review the latest plan.';
        banner.hidden = false;
        await refresh();
      } else {
        banner.textContent = error.message;
        banner.hidden = false;
      }
    } finally {
      submit.disabled = false;
    }
  });
}
