'use strict';

async function loadProjectRoleEditor(startNewRole = false) {
  const project = state.project;
  const wrap = $('#project-roles-content');
  const loadButton = $('#load-project-roles');
  const loadSpan = loadButton?.querySelector('span');
  if (loadButton) {
    if (loadSpan) loadSpan.textContent = 'Loading…';
    else loadButton.textContent = 'Loading…';
    loadButton.disabled = true;
  }
  wrap.textContent = 'Loading roles…';
  try {
    const directory = await request(projectApi('project-roles'));
    if (project !== state.project) return;
    if (loadButton) {
      if (loadSpan) loadSpan.textContent = 'Refresh';
      else loadButton.textContent = 'Refresh';
      loadButton.disabled = false;
    }
    clearNode(wrap);
    const roles = createEl('select', 'input');
    roles.setAttribute('aria-label', 'Project role');
    roles.replaceChildren(
      ...directory.roles.map(role => new Option(role.name, role.id)),
      new Option('New custom role', '')
    );
    const members = createEl('select', 'input');
    members.setAttribute('aria-label', 'Project member');
    populateSelect(members, state.agents.filter(agent => agent.role !== 'owner' && !agent.revoked), {
      getValue: agent => agent.id,
      getLabel: agent => agent.name
    });
    const rolePills = createEl('div', 'role-pills-bar');
    for (const r of directory.roles) {
      const pillBtn = createEl('button', 'chip');
      pillBtn.type = 'button';
      pillBtn.textContent = `${r.name} (${(r.permissions || []).length})`;
      pillBtn.addEventListener('click', () => {
        roles.value = r.id;
        update();
      });
      rolePills.appendChild(pillBtn);
    }
    const newRoleChip = createEl('button', 'chip');
    newRoleChip.type = 'button';
    newRoleChip.textContent = '+ New custom role';
    newRoleChip.addEventListener('click', () => {
      roles.value = '';
      update();
      name.focus();
    });
    rolePills.appendChild(newRoleChip);

    const form = createEl('div', 'role-editor-grid');
    const name = createEl('input', 'input');
    name.setAttribute('aria-label', 'Role name');
    name.placeholder = 'e.g. code_reviewer';
    const actions = createEl('fieldset', 'role-permissions');
    actions.appendChild(createEl('legend', '', 'Permissions granted by role'));
    const roleNote = createEl('p', 'muted small role-readonly-note');
    actions.appendChild(roleNote);
    const inputs = directory.actions.map(permission => {
      const label = createEl('label', '');
      const check = createEl('input', '');
      check.type = 'checkbox';
      label.append(check, ' ' + permission);
      actions.appendChild(label);
      return { permission, check };
    });
    const status = createEl('p', 'muted role-editor-status');
    const run = async body => {
      if (project !== state.project || !form.isConnected) return;
      try {
        await request(projectApi('project-roles'), { method: 'POST', body });
        await refresh();
        if (project === state.project) await loadProjectRoleEditor();
      } catch (error) {
        status.textContent = error.message;
      }
    };
    const save = uiButton({
      label: 'Save custom role',
      cls: 'btn btn-primary',
      onClick: () => run({
        operation: 'save',
        role: { id: roles.value, name: name.value, permissions: inputs.filter(x => x.check.checked).map(x => x.permission) }
      })
    });
    const assign = uiButton({
      label: 'Assign selected role',
      cls: 'btn btn-secondary',
      onClick: async () => {
        const role = directory.roles.find(entry => entry.id === roles.value);
        const member = members.value;
        if (!role || !member) return;
        const confirmed = await askConfirmation(
          `Assign ${role.name} to ${principalLabel(member)}?`,
          'Role grants ' + role.permissions.join(', ') + '. Individual overrides still apply.'
        );
        if (confirmed) await run({ operation: 'assign', principal_id: member, role: { id: role.id } });
      }
    });
    const resetPermission = createEl('select', 'input');
    resetPermission.setAttribute('aria-label', 'Permission to reset');
    resetPermission.replaceChildren(...directory.actions.map(permission => new Option(permission, permission)));
    const reset = uiButton({
      label: 'Reset permission to role default',
      cls: 'btn btn-secondary',
      onClick: () => run({ operation: 'reset', principal_id: members.value, permission: resetPermission.value })
    });
    const update = () => {
      const role = directory.roles.find(entry => entry.id === roles.value);
      const builtin = role && !role.id.startsWith('role_');
      const isNew = !roles.value;
      name.value = role?.name || '';
      name.disabled = Boolean(builtin);
      nameField.hidden = Boolean(builtin);
      save.hidden = Boolean(builtin);
      save.disabled = Boolean(builtin);
      roleNote.textContent = builtin
        ? 'Built-in role permissions are fixed. Choose + New custom role above to define custom permissions.'
        : 'Select which permissions this custom role grants.';
      assign.disabled = !role || !members.value;
      reset.disabled = !directory.assignments[members.value];
      for (const chip of rolePills.children) {
        const match = isNew
          ? chip === newRoleChip
          : chip.textContent.startsWith(`${role?.name || ''} `);
        chip.classList.toggle('chip-active', Boolean(match));
      }
      for (const { permission, check } of inputs) {
        check.checked = role ? role.permissions.includes(permission) : permission === 'project.read';
        check.disabled = Boolean(builtin) || permission === 'project.read';
      }
    };
    roles.onchange = update;
    members.onchange = () => { roles.value = directory.assignments[members.value] || 'viewer'; update(); };
    const memberField = createEl('label', 'field');
    memberField.append(createEl('span', '', 'Project member'), members);
    const roleField = createEl('label', 'field');
    roleField.append(createEl('span', '', 'Selected role'), roles);
    const nameField = createEl('label', 'field');
    nameField.append(createEl('span', '', 'Custom role name'), name);
    const actionRow = createEl('div', 'role-editor-actions');
    actionRow.append(save, assign);
    const resetField = createEl('div', 'role-reset-field');
    const resetLabel = createEl('label', 'field');
    resetLabel.append(createEl('span', '', 'Reset permission'), resetPermission);
    resetField.append(resetLabel, reset);
    form.append(memberField, roleField, nameField, actions, actionRow, resetField, status);
    if (startNewRole) {
      roles.value = '';
      update();
    } else {
      members.onchange();
    }
    wrap.append(rolePills, form);
    if (startNewRole) name.focus();
  } catch (error) {
    if (project === state.project) {
      wrap.textContent = error.message;
      if (loadButton) {
        if (loadSpan) loadSpan.textContent = 'Retry';
        else loadButton.textContent = 'Retry';
        loadButton.disabled = false;
      }
    }
  }
}

function renderSettingsAccessPanels(canManageMembers, canEditSettings) {
  populateSelect($('#permission-principal'), state.agents.filter(x => !x.revoked), {
    getValue: a => a.id,
    getLabel: a => `${a.name} (${a.role})`
  });

  const membersWrap = $('#members-list');
  if (membersWrap) {
    clearNode(membersWrap);
    for (const a of state.agents) {
      const row = createEl('div', 'list-item');
      const top = createEl('div', 'list-item-row');
      const main = createEl('div', 'member-row-main');
      main.append(
        createEl('div', 'list-item-title', `${a.id === state.project?.id ? `You (${a.name})` : a.name} · ${a.role}`),
        uiBadge(a.revoked ? 'Revoked' : 'Active', a.revoked ? 'err' : 'muted')
      );
      top.appendChild(main);
      row.appendChild(top);
      const identity = createEl('details', 'small member-identity');
      const idPill = createEl('div', 'scope-id-pill member-id-pill');
      const copyMemberBtn = uiButton({
        label: 'Copy',
        icon: 'copy',
        cls: 'btn btn-xs btn-secondary',
        onClick: async () => {
          try {
            await navigator.clipboard.writeText(a.id);
            copyMemberBtn.querySelector('span').textContent = 'Copied';
            setTimeout(() => { if (copyMemberBtn.isConnected) copyMemberBtn.querySelector('span').textContent = 'Copy'; }, 1500);
          } catch {}
        }
      });
      idPill.append(createEl('code', 'small', a.id), copyMemberBtn);
      identity.append(createEl('summary', '', 'Identity details'), idPill);
      row.appendChild(identity);
      if (!a.revoked) {
        const grants = state.permissions.filter(grant => grant.principal_id === a.id && !grant.revoked);
        if (a.role === 'owner' || grants.length) {
          const effective = createEl('details', 'small member-perms');
          effective.appendChild(createEl('summary', '', `Effective permissions${a.role === 'owner' ? ' (Full access)' : ` (${grants.length})`}`));
          const permsGrid = createEl('div', 'effective-perms-grid');
          if (a.role === 'owner') {
            const ownerBanner = createEl('div', 'perm-row-pill perm-row-owner');
            ownerBanner.append(
              createEl('strong', 'small', 'All project permissions'),
              uiBadge('Owner role', 'ok')
            );
            permsGrid.appendChild(ownerBanner);
          }
          for (const grant of grants) {
            const pill = createEl('div', 'perm-row-pill');
            pill.append(
              createEl('code', 'perm-code', grant.permission),
              uiBadge(grant.source || 'Individual grant', grant.source ? 'muted' : 'ok')
            );
            permsGrid.appendChild(pill);
          }
          effective.appendChild(permsGrid);
          row.appendChild(effective);
        }
      }
      if (canManageMembers && !a.revoked && a.id !== state.project?.id) {
        top.appendChild(uiButton({
          label: 'Revoke access',
          cls: 'btn btn-xs btn-danger',
          onClick: async () => {
            try {
              const project = state.project;
              const confirmed = await askConfirmation('Revoke project access?', `Remove ${a.name} from ${project.project_name || project.project_id}?`);
              if (!confirmed || project !== state.project) return;
              await request(projectApi('agents'), { method: 'POST', body: { operation: 'revoke', id: a.id } });
              await refresh();
            } catch (err) {
              showError(err.message);
            }
          }
        }));
      }
      membersWrap.appendChild(row);
    }
  }

  const activePerms = (state.permissions || []).filter(p => !p.revoked);
  renderList($('#permissions-list'), activePerms, 'No extra permission grants beyond role defaults.', perm => {
    const row = createEl('div', 'list-item');
    const top = createEl('div', 'list-item-row');
    const main = createEl('div', 'member-row-main');
    main.append(
      createEl('strong', '', principalLabel(perm.principal_id)),
      createEl('code', 'perm-code', perm.permission),
      uiBadge(perm.source || 'Individual grant', 'muted')
    );
    top.appendChild(main);
    if (canManageMembers) {
      top.appendChild(uiButton({
        label: 'Revoke',
        cls: 'btn btn-xs btn-danger',
        onClick: async () => {
          try {
            await request(projectApi('permissions'), {
              method: 'POST',
              body: { operation: 'revoke', principal_id: perm.principal_id, permission: perm.permission }
            });
            await refresh();
          } catch (err) {
            showError(err.message);
          }
        }
      }));
    }
    row.appendChild(top);
    return row;
  });

  renderList($('#certificates-list'), state.certificates, 'No client certificates enrolled for this project.', c => {
    const row = createEl('div', 'list-item');
    row.append(
      createEl('code', '', c.fingerprint),
      createEl('div', 'muted small', c.revoked ? 'Revoked' : (c.expires_at ? `Expires ${c.expires_at}` : 'Active'))
    );
    if (canManageMembers && !c.revoked) {
      row.appendChild(uiButton({
        label: 'Revoke certificate',
        cls: 'btn btn-xs btn-danger',
        onClick: async () => {
          try {
            await request(projectApi('certificates'), { method: 'POST', body: { operation: 'revoke', fingerprint: c.fingerprint } });
            await refresh();
          } catch (err) {
            showError(err.message);
          }
        }
      }));
    }
    return row;
  });

  const invWrap = $('#cert-invitations-list');
  if (invWrap) {
    clearNode(invWrap);
    for (const inv of state.certInvitations) {
      const row = createEl('div', 'list-item');
      row.append(
        createEl('div', 'list-item-title', `Invitation ${inv.id}`),
        createEl('div', 'muted small', `Status: ${inv.status || (inv.revoked ? 'revoked' : 'active')}`)
      );
      invWrap.appendChild(row);
    }
  }

  if (state.settings) {
    if ($('#setting-max-parallel')) $('#setting-max-parallel').value = String(state.settings.max_parallel || 4);
    if ($('#setting-max-lease')) $('#setting-max-lease').value = String(state.settings.max_lease_seconds || 300);
    if ($('#setting-summary-tokens')) $('#setting-summary-tokens').value = String(state.settings.summary_token_target || 1000);
    if ($('#setting-review-mode')) $('#setting-review-mode').value = state.settings.default_review_mode || 'report_first';
  }
  if ($('#settings-form')) {
    $$('#settings-form input, #settings-form select, #settings-form button').forEach(el => { el.disabled = !canEditSettings; });
  }
  if (state.budget) {
    if ($('#budget-total')) $('#budget-total').value = String(state.budget.total_units || 0);
    if ($('#budget-status-text')) $('#budget-status-text').textContent = formatBudgetStatus(state.budget);
  }
}

function renderSettingsOtherPanels() {
  if ($('#remote-access-status')) {
    $('#remote-access-status').textContent = state.remoteAccessEnabled
      ? 'Remote TLS access is ENABLED on this Gossip host.'
      : 'Remote LAN/internet access is currently DISABLED (loopback-only local authority mode).';
  }

  renderList($('#replicas-list'), state.replicas, 'No peer replicas registered.', rep => {
    const row = createEl('div', 'list-item');
    row.append(
      createEl('div', 'list-item-title', rep.replica_id || rep.home_id),
      createEl('div', 'muted small', `Last synced event seq: ${rep.last_sequence || 0}`)
    );
    return row;
  });

  renderList($('#connections-remote-list'), state.remoteProjects, 'No remote projects joined.', rp => {
    const row = createEl('div', 'list-item');
    row.append(
      createEl('div', 'list-item-title', `${rp.project_name || rp.project_id} (${rp.status})`),
      createEl('div', 'muted small', `${rp.endpoint} · Home: ${rp.home_id}`)
    );
    return row;
  });

  for (const sel of [$('#remote-hint-repo'), $('#ws-repo')]) {
    populateSelect(sel, state.repositories, {
      getValue: repo => repo.id,
      getLabel: repo => repo.name || repo.id
    });
  }

  renderList($('#repositories-list'), state.repositories, 'No repositories registered.', r => {
    const row = createEl('div', 'repository-settings-row');
    row.append(
      createEl('strong', '', r.name || r.id),
      createEl('span', 'muted small', `Remotes: ${(r.remote_hints || []).join(', ') || 'none'}`)
    );
    return row;
  });

  renderList($('#workspaces-list'), state.workspaces, 'No local workspaces registered.', w => {
    const row = createEl('div', 'workspace-checkout-row');
    const info = createEl('div', 'workspace-checkout-info');
    const title = createEl('div', 'workspace-checkout-title');
    title.append(
      createEl('div', 'list-item-title', w.name || w.id),
      uiBadge(w.dirty ? 'Changes' : 'Clean', w.dirty ? 'warn' : 'ok')
    );
    info.append(title, createEl('div', 'muted small workspace-checkout-branch', w.branch || 'Detached HEAD'));
    const details = createEl('details', 'workspace-checkout-details');
    details.appendChild(createEl('summary', '', 'Checkout details'));
    const detailBody = createEl('div', 'workspace-checkout-detail-body');
    detailBody.append(
      createEl('span', 'muted small', 'Path'),
      createEl('code', '', w.local_path || 'Path unavailable'),
      createEl('span', 'muted small', 'HEAD'),
      createEl('code', '', w.head || 'Unknown')
    );
    details.appendChild(detailBody);
    info.appendChild(details);
    row.append(info, uiButton({
      label: `Update checkout ${w.name || w.id}`,
      shortLabel: 'Update checkout',
      icon: 'refresh',
      cls: 'btn btn-xs btn-secondary icon-only-control',
      title: `Update checkout ${w.name || w.id}`,
      onClick: () => {
        if ($('#ws-update-id')) $('#ws-update-id').value = w.id;
        if ($('#ws-update-branch')) $('#ws-update-branch').value = w.branch || 'main';
        if ($('#ws-update-head')) $('#ws-update-head').value = w.head || '';
        if ($('#ws-update-dirty')) $('#ws-update-dirty').checked = Boolean(w.dirty);
        openDialog('workspace-update-dialog');
      }
    }));
    return row;
  });

  renderList($('#reservations-list'), state.fileReservations, 'No active file reservations.', fr => {
    const row = createEl('div', 'list-item');
    row.append(
      createEl('code', '', fr.path),
      createEl('div', 'muted small', `Holder: ${fr.holder} · Expires: ${formatTime(fr.expires_at)}`)
    );
    return row;
  });

  renderList($('#integrations-list'), state.integrations, 'No integrations recorded.', it => {
    const row = createEl('details', 'integration-settings-row');
    row.append(
      createEl('summary', '', `${it.task_id} · attempt ${it.attempt}${it.passed === true ? ' · passed' : it.passed === false ? ' · failed' : ''}`),
      createEl('div', 'muted small integration-settings-evidence', it.summary || it.combined_revision || 'No integration summary recorded.')
    );
    return row;
  });

  if ($('#retention-summary')) {
    $('#retention-summary').textContent = state.retention
      ? `Earliest event: #${state.retention.earliest_sequence || 0} · Latest event: #${state.retention.latest_sequence || 0} · Pruned through: #${state.retention.pruned_through_sequence || 0}`
      : '';
  }

  renderList($('#legacy-imports-list'), state.legacyImports, 'No legacy imports present.', imp => {
    const row = createEl('div', 'list-item');
    row.append(
      createEl('div', 'list-item-title', imp.id || imp.source || 'Archive'),
      createEl('div', 'muted small', `Imported at ${formatTime(imp.created_at)}`)
    );
    return row;
  });

  const recWrap = $('#legacy-records-list');
  if (recWrap) {
    clearNode(recWrap);
    for (const rec of state.legacyRecords) {
      const row = createEl('div', 'list-item');
      row.appendChild(createEl('div', 'list-item-title', `${rec.kind || 'record'} · ${rec.id}`));
      recWrap.appendChild(row);
    }
  }

  const eventsList = $('#history-events-list');
  if (eventsList) {
    renderList(eventsList, (state.events || []).slice(0, 25), 'No recent audit events recorded.', renderAuditEventRow);
  }
}

function renderSettingsView() {
  const canManageRoles = state.project?.role === 'owner' && state.project?.source !== 'remote';
  if (!canManageRoles && ['project-roles-card', 'access-requests-card'].includes(state.accessSection)) {
    state.accessSection = 'access-members';
  }
  if ($('#project-roles-card')) $('#project-roles-card').hidden = !canManageRoles;
  if ($('#access-requests-card')) $('#access-requests-card').hidden = !canManageRoles;
  for (const id of ['project-roles-card', 'access-requests-card']) {
    const navBtn = $(`[data-access-section="${id}"]`);
    if (navBtn) navBtn.hidden = !canManageRoles;
  }
  if ($('#open-access-requests')) $('#open-access-requests').onclick = async () => {
    await openShareProject('requests');
    $('#share-requests-heading')?.focus();
  };
  for (const button of $$('[data-access-section]')) {
    const active = button.dataset.accessSection === state.accessSection;
    const panel = document.getElementById(button.dataset.accessSection);
    if (panel) panel.hidden = !active;
    button.setAttribute('aria-pressed', String(active));
  }
  const accessSectionSelect = $('#access-section-select');
  if (accessSectionSelect) {
    accessSectionSelect.replaceChildren(...$$('[data-access-section]:not([hidden])').map(button =>
      new Option(button.textContent.trim(), button.dataset.accessSection)));
    accessSectionSelect.value = state.accessSection;
  }
  if ($('#load-project-roles')) $('#load-project-roles').onclick = () => loadProjectRoleEditor(false);
  if ($('#new-project-role-btn')) $('#new-project-role-btn').onclick = () => loadProjectRoleEditor(true);
  if (canManageRoles && state.settingsTab === 'access' && state.accessSection === 'project-roles-card') {
    const content = $('#project-roles-content');
    if (content && !content.childElementCount && !$('#load-project-roles')?.disabled) {
      void loadProjectRoleEditor(false);
    }
  }
  renderAccessScope();

  $$('#settings-tabs .subtab').forEach(btn => {
    const active = btn.dataset.settingsTab === state.settingsTab;
    btn.classList.toggle('subtab-active', active);
    btn.setAttribute('aria-selected', String(active));
    btn.tabIndex = active ? 0 : -1;
  });
  $$('.settings-panel').forEach(panel => {
    panel.hidden = panel.getAttribute('data-settings-panel') !== state.settingsTab;
  });

  renderSettingsAccessPanels(canManageMembers(), canEditSettings());
  renderSettingsOtherPanels();
}

async function copyJsonWithFeedback(button, text) {
  if (!text) return;
  try {
    await navigator.clipboard?.writeText(text);
    const original = button.textContent;
    button.textContent = 'Copied JSON';
    setTimeout(() => { if (button.isConnected) button.textContent = original; }, 1600);
  } catch (err) {
    showError(err?.message || 'Clipboard copy unavailable. Copy the JSON block manually.');
  }
}

function wireSettingsEvents() {
  $$('[data-access-section]').forEach(button => {
    button.addEventListener('click', () => {
      state.accessSection = button.dataset.accessSection;
      syncUrlState();
      renderSettingsView();
      void loadSettingsRouteData(state.generation);
      const section = document.getElementById(state.accessSection);
      section?.focus({ preventScroll: true });
      if (section && section.getBoundingClientRect().top < $('.topbar').getBoundingClientRect().bottom) {
        $('#settings-tabs')?.scrollIntoView({ block: 'start' });
      }
    });
  });

  $('#access-section-select')?.addEventListener('change', event => {
    state.accessSection = event.target.value;
    syncUrlState();
    renderSettingsView();
    void loadSettingsRouteData(state.generation);
  });

  $('#copy-project-id')?.addEventListener('click', async () => {
    const id = state.project?.project_id;
    if (!id) return;
    const status = $('#copy-project-status');
    try {
      await navigator.clipboard.writeText(id);
      if (state.project?.project_id === id && status) status.textContent = 'Copied';
    } catch {
      const selection = window.getSelection();
      selection.selectAllChildren($('#access-project-id'));
      if (status) status.textContent = 'ID selected. Use your copy shortcut.';
    }
  });

  $$('#settings-tabs .subtab').forEach(btn => {
    btn.addEventListener('click', () => {
      state.settingsTab = btn.dataset.settingsTab;
      syncUrlState();
      renderSettingsView();
      void loadSettingsRouteData(state.generation);
    });
  });

  bindProjectForm('#invite-form', {
    action: 'agents',
    getBody: () => {
      const fp = $('#invite-fingerprint')?.value?.trim() || '';
      return {
        operation: fp ? 'invite_remote' : 'invite',
        name: $('#invite-name')?.value?.trim() || '',
        role: $('#invite-role')?.value || 'worker',
        fingerprint: fp,
        expires_at: $('#invite-expires')?.value?.trim() || ''
      };
    },
    onSuccess: res => {
      if ($('#invite-name')) $('#invite-name').value = '';
      const box = $('#invite-result');
      if (box) {
        const jsonText = JSON.stringify(res, null, 2);
        box.hidden = false;
        clearNode(box);
        const head = createEl('div', 'list-item-row');
        head.append(
          createEl('strong', '', `Issued credential for ${res.name} (${res.id})`),
          uiButton({
            label: 'Copy JSON',
            cls: 'btn btn-xs btn-secondary',
            onClick: event => copyJsonWithFeedback(event.currentTarget, jsonText)
          })
        );
        const pre = createEl('pre', 'code-block', jsonText);
        box.append(head, pre);
      }
    }
  });

  bindProjectForm('#permission-form', {
    action: 'permissions',
    getBody: () => ({
      operation: 'grant',
      principal_id: $('#permission-principal')?.value || '',
      permission: $('#permission-name')?.value || 'project.read'
    })
  });

  bindProjectForm('#certificate-form', {
    action: 'certificates',
    getBody: () => ({
      operation: 'grant',
      fingerprint: $('#cert-fingerprint')?.value?.trim() || '',
      expires_at: $('#cert-expires')?.value?.trim() || ''
    }),
    onSuccess: () => {
      if ($('#cert-fingerprint')) $('#cert-fingerprint').value = '';
    }
  });

  bindProjectForm('#pair-form', {
    action: 'certificate-invitations',
    getBody: () => ({
      operation: 'create',
      fingerprint: $('#pair-fingerprint')?.value?.trim() || '',
      expires_at: $('#pair-expires')?.value?.trim() || ''
    }),
    onSuccess: res => {
      const box = $('#pair-result');
      if (box) {
        box.hidden = false;
        box.textContent = `Created invitation ${res.id} (token: ${res.token || 'saved'})`;
      }
    }
  });

  bindProjectForm('#settings-form', {
    action: 'settings',
    getBody: () => ({
      settings: {
        revision: state.settings?.revision || 0,
        max_parallel: Number($('#setting-max-parallel')?.value || 4),
        max_lease_seconds: Number($('#setting-max-lease')?.value || 300),
        summary_token_target: Number($('#setting-summary-tokens')?.value || 1000),
        default_review_mode: $('#setting-review-mode')?.value || 'report_first'
      }
    })
  });

  bindProjectForm('#budget-form', {
    action: 'budget',
    getBody: () => ({
      operation: 'set',
      revision: state.budget?.revision || 0,
      amount: Number($('#budget-total')?.value || 0)
    })
  });

  $('#export-recovery-btn')?.addEventListener('click', async () => {
    if (!state.project) return;
    try {
      const res = await request(`/api/projects/${encodeURIComponent(state.project.project_id)}/recovery-credential`, {
        method: 'POST',
        body: {}
      });
      if ($('#recovery-output')) {
        $('#recovery-output').hidden = false;
        $('#recovery-output').textContent = JSON.stringify(res, null, 2);
      }
      if ($('#copy-recovery-btn')) $('#copy-recovery-btn').hidden = false;
      setView('settings');
      state.settingsTab = 'access';
      renderSettingsView();
    } catch (err) {
      if (err.status === 404) {
        showError('No saved recovery credential exists for this project in the local registry. Issue a fresh scoped credential from Settings → Access → Project Members.');
        return;
      }
      showError(err.message);
    }
  });

  $('#copy-recovery-btn')?.addEventListener('click', event => {
    const jsonText = $('#recovery-output')?.textContent || '';
    void copyJsonWithFeedback(event.currentTarget, jsonText);
  });

  bindProjectForm('#repo-create-form', {
    action: 'repositories',
    getBody: () => ({ name: $('#repo-create-name')?.value?.trim() || '' }),
    onSuccess: () => {
      if ($('#repo-create-name')) $('#repo-create-name').value = '';
    }
  });

  bindProjectForm('#remote-hint-form', {
    action: 'repositories',
    getBody: () => ({
      operation: 'add_hint',
      repository_id: $('#remote-hint-repo')?.value || '',
      remote_url: $('#remote-hint-url')?.value?.trim() || ''
    }),
    onSuccess: () => {
      if ($('#remote-hint-url')) $('#remote-hint-url').value = '';
    }
  });

  $('#open-workspace-dialog-btn')?.addEventListener('click', () => openDialog('workspace-dialog'));

  bindProjectForm('#workspace-form', {
    action: 'workspaces',
    getBody: () => ({
      operation: 'register',
      repository_id: $('#ws-repo')?.value || '',
      name: $('#ws-name')?.value?.trim() || '',
      node_label: $('#ws-node')?.value?.trim() || '',
      local_path: $('#ws-path')?.value?.trim() || '',
      branch: $('#ws-branch')?.value?.trim() || 'main',
      head: $('#ws-head')?.value?.trim() || 'HEAD'
    }),
    onSuccess: () => closeDialog('workspace-dialog')
  });

  bindProjectForm('#workspace-update-form', {
    action: 'workspaces',
    getBody: () => ({
      operation: 'update',
      workspace_id: $('#ws-update-id')?.value || '',
      branch: $('#ws-update-branch')?.value?.trim() || '',
      head: $('#ws-update-head')?.value?.trim() || '',
      dirty: Boolean($('#ws-update-dirty')?.checked)
    }),
    onSuccess: () => closeDialog('workspace-update-dialog')
  });

  bindProjectForm('#event-retention-form', {
    action: 'events',
    getBody: () => ({
      operation: 'prune',
      through_sequence: Number($('#prune-sequence')?.value || 0)
    })
  });
}
