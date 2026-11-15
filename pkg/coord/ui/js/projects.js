'use strict';

function principalFromLocalProject(lp) {
  return {
    id: lp.owner?.id || '',
    project_id: lp.project_id,
    project_name: lp.project_name,
    home_id: lp.home_id || state.dashboardAccess.home_id,
    name: lp.owner?.name || 'Owner',
    role: lp.owner?.role || 'owner',
    permissions: lp.owner?.permissions || []
  };
}

async function openLocalProject(lp, targetView = 'chat') {
  selectProject(principalFromLocalProject(lp), { view: targetView });
  await refresh();
}

function principalFromRemoteProject(rp) {
  return {
    source: 'remote',
    id: rp.principal_id,
    project_id: rp.project_id,
    project_name: rp.project_name || rp.project_id,
    home_id: rp.home_id,
    name: rp.principal_name || rp.principal_id,
    role: rp.role || 'worker',
    permissions: []
  };
}

async function openRemoteProject(rp, targetView = 'chat') {
  selectProject(principalFromRemoteProject(rp), { view: targetView });
  await refresh();
}

function projectFromLocation({ projectID, homeID }) {
  if (!projectID) return null;
  if (homeID && homeID !== state.dashboardAccess.home_id) {
    const remote = state.remoteProjects.find(project => project.project_id === projectID && project.home_id === homeID);
    return remote ? principalFromRemoteProject(remote) : null;
  }
  const local = state.localProjects.find(project => project.project_id === projectID && project.status === 'ready');
  return local ? principalFromLocalProject(local) : state.projects.find(project => project.project_id === projectID) || null;
}

function createProjectCard({ title, subtitle, meta = '', icon = 'projects', isActive = false, badgeText, badgeVariant = '', errorText = '', actions = null, extra = null }) {
  const card = createEl('div', `list-item project-row${isActive ? ' is-active-project' : ''}`);
  const iconBox = createEl('div', 'project-card-icon');
  iconBox.appendChild(svgIcon(icon));
  const main = createEl('div', 'project-row-main');
  const heading = createEl('div', 'project-row-heading');
  heading.append(
    createEl('div', 'list-item-title', title),
    ...(isActive ? [uiBadge('Active', 'ok')] : []),
    uiBadge(badgeText, badgeVariant)
  );
  main.append(heading, createEl('div', 'muted small', subtitle));
  if (meta) main.appendChild(createEl('div', 'muted small project-card-meta', meta));
  if (errorText) main.appendChild(createEl('div', 'muted small', errorText));
  const body = createEl('div', 'project-card-lead');
  body.append(iconBox, main);
  card.appendChild(body);
  if (actions) card.appendChild(actions);
  if (extra) card.appendChild(extra);
  return card;
}

function renderLocalProjectCard(lp) {
  const statusLabel = lp.status === 'ready'
    ? 'Ready'
    : lp.status === 'ambiguous_owner'
      ? 'Choose local owner'
      : 'Missing active owner';
  const isActive = Boolean(state.project && state.project.project_id === lp.project_id && (!state.project.home_id || state.project.home_id === lp.home_id));

  let actions = null;
  let extra = null;
  if (lp.status === 'ready') {
    actions = createEl('div', 'btn-row project-card-actions');
    actions.append(
      uiButton({
        label: 'Open ' + (lp.project_name || 'local project') + ' in Chat',
        shortLabel: 'Chat',
        icon: 'chat',
        cls: `btn btn-sm ${isActive ? 'btn-primary' : 'btn-secondary'}`,
        title: 'Open in Chat',
        onClick: () => openLocalProject(lp, 'chat')
      }),
      uiButton({
        label: 'Open ' + (lp.project_name || 'local project') + ' Plans',
        shortLabel: 'Plans',
        icon: 'plans',
        cls: 'btn btn-sm btn-secondary',
        title: 'Open Plans',
        onClick: () => openLocalProject(lp, 'plans')
      }),
      uiButton({
        label: 'Open ' + (lp.project_name || 'local project') + ' To-dos',
        shortLabel: 'To-dos',
        icon: 'todos',
        cls: 'btn btn-sm btn-secondary',
        title: 'Open To-dos',
        onClick: () => openLocalProject(lp, 'todos')
      })
    );
  } else if (lp.status === 'ambiguous_owner') {
    extra = createEl('div', 'repair-row');
    extra.appendChild(createEl('p', 'muted small', 'Multiple active owners exist for this legacy project. Explicitly select which owner identity represents You locally:'));
    const sel = createEl('select', 'input select-compact');
    populateSelect(sel, lp.candidates || [], {
      getValue: c => c.id,
      getLabel: c => `${c.name} (${c.id})`
    });
    const saveBtn = uiButton({
      label: 'Confirm owner & open',
      cls: 'btn btn-sm btn-primary',
      onClick: async () => {
        try {
          const owner = await request(`/api/projects/${encodeURIComponent(lp.project_id)}/local-owner`, {
            method: 'POST',
            body: { principal_id: sel.value }
          });
          await loadDashboardProjects();
          selectProject(owner, { view: 'chat' });
          await refresh();
        } catch (err) {
          showError(err.message);
        }
      }
    });
    extra.append(sel, saveBtn);
  } else {
    extra = createEl('p', 'muted small repair-row', 'This project has no active local owner. Restore its owner through project recovery.');
  }

  return createProjectCard({
    title: lp.project_name || lp.project_id,
    subtitle: lp.plan_revision ? 'Plan available' : 'No plans yet',
    meta: lp.workspace_path || lp.repo_root || lp.project_id,
    icon: 'projects',
    isActive,
    badgeText: statusLabel,
    badgeVariant: lp.status === 'ready' ? 'muted' : 'warn',
    actions,
    extra
  });
}

function renderRemoteProjectCard(rp) {
  const isActive = Boolean(state.project && state.project.project_id === rp.project_id && state.project.home_id === rp.home_id);
  const actions = createEl('div', 'btn-row project-card-actions');
  actions.append(
    uiButton({
      label: 'Open remote project ' + (rp.project_name || 'project') + ' in Chat',
      shortLabel: 'Chat',
      icon: 'chat',
      cls: `btn btn-sm ${isActive ? 'btn-primary' : 'btn-secondary'}`,
      title: 'Open in Chat',
      onClick: () => openRemoteProject(rp, 'chat')
    }),
    uiButton({
      label: 'Open remote project ' + (rp.project_name || 'project') + ' Plans',
      shortLabel: 'Plans',
      icon: 'plans',
      cls: 'btn btn-sm btn-secondary',
      title: 'Open Plans',
      onClick: () => openRemoteProject(rp, 'plans')
    }),
    uiButton({
      label: 'Open remote project ' + (rp.project_name || 'project') + ' To-dos',
      shortLabel: 'To-dos',
      icon: 'todos',
      cls: 'btn btn-sm btn-secondary',
      title: 'Open To-dos',
      onClick: () => openRemoteProject(rp, 'todos')
    })
  );
  return createProjectCard({
    title: rp.project_name || rp.project_id,
    subtitle: `${rp.endpoint} · Principal: ${rp.principal_name || rp.principal_id} (${rp.role || 'member'})`,
    meta: rp.project_id,
    icon: 'globe',
    isActive,
    badgeText: rp.status || 'unknown',
    badgeVariant: rp.status === 'connected' ? 'muted' : 'warn',
    errorText: rp.error || '',
    actions
  });
}

function renderProjectsView() {
  const activeTab = state.projectsTab === 'joined' ? 'joined' : 'local';
  $$('#projects-tabs .subtab').forEach(btn => {
    const selected = btn.dataset.projectsTab === activeTab;
    btn.classList.toggle('subtab-active', selected);
    btn.setAttribute('aria-selected', String(selected));
  });
  if ($('#local-projects-panel')) $('#local-projects-panel').hidden = activeTab !== 'local';
  if ($('#joined-projects-panel')) $('#joined-projects-panel').hidden = activeTab !== 'joined';

  const query = state.projectQuery.toLowerCase();
  const matches = project => [project.project_name, project.project_id, project.endpoint]
    .some(value => value?.toLowerCase().includes(query));
  const localProjects = state.localProjects.filter(matches);
  const remoteProjects = state.remoteProjects.filter(matches);
  if ($('#local-count-pill')) $('#local-count-pill').textContent = String(localProjects.length);
  if ($('#remote-count-pill')) $('#remote-count-pill').textContent = String(remoteProjects.length);

  renderList(
    $('#local-projects-list'),
    localProjects,
    query ? 'No local projects match this search.' : 'Create a project from a Git checkout on this device.',
    renderLocalProjectCard,
    'muted'
  );

  renderList(
    $('#remote-projects-list'),
    remoteProjects,
    query ? 'No joined projects match this search.' : 'Accept an invitation or import a credential with Join project.',
    renderRemoteProjectCard,
    'muted'
  );
}

async function loadDeviceRequests() {
  const project = state.project;
  const wrap = $('#device-requests');
  try {
    const [invitations, directory] = await Promise.all([
      request(projectApi('remote-invitations')),
      request(projectApi('project-roles'))
    ]);
    if (project !== state.project || !$('#share-project-dialog').open) return;
    renderList(wrap, invitations, 'No invitations yet.', invite => {
      const row = createEl('article', 'list-item');
      row.appendChild(createEl('strong', '', invite.name));
      const expired = invite.expires_at <= Date.now();
      const stage = {
        issued: 'Waiting for recipient',
        pending: 'Pending approval',
        approved: 'Approved',
        enrolled: 'Connected',
        denied: 'Denied',
        cancelled: 'Cancelled'
      }[invite.state] || invite.state;
      row.appendChild(createEl('p', 'muted', invite.role + ' · ' + (expired && ['issued', 'pending', 'approved'].includes(invite.state) ? 'Expired' : stage)));
      if (invite.fingerprint) row.appendChild(createEl('code', 'agent-dm-body', invite.fingerprint));
      const role = directory.roles.find(entry => entry.id === invite.role);
      if (invite.state === 'pending') {
        row.appendChild(createEl('p', 'small', role ? role.name + ' grants ' + role.permissions.join(', ') : 'The selected role is unavailable.'));
      }
      const actions = expired
        ? []
        : invite.state === 'pending'
          ? [...(role ? [['approved', 'Approve device']] : []), ['denied', 'Deny request']]
          : ['issued', 'approved'].includes(invite.state)
            ? [['cancelled', 'Cancel invitation']]
            : [];
      for (const [operation, label] of actions) {
        const button = uiButton({
          label,
          cls: 'btn btn-secondary',
          onClick: async () => {
            if (project !== state.project) return;
            if (operation === 'approved' && !await askConfirmation(
              'Approve ' + invite.name + ' for ' + project.project_name + '?',
              'Compare the fingerprint shown here with the one on the recipient’s device.'
            )) return;
            if (project !== state.project) return;
            await runGuardedAction(button, async () => {
              await request(projectApi('remote-invitations'), { method: 'POST', body: { operation, id: invite.id } });
              await loadDeviceRequests();
            });
          }
        });
        row.appendChild(button);
      }
      return row;
    }, 'muted');
  } catch (error) {
    if (project === state.project) wrap.textContent = error.message;
  }
}

function showShareTab(tab) {
  for (const name of ['invite', 'requests']) {
    const active = name === tab;
    $('#share-' + name + '-tab').setAttribute('aria-pressed', String(active));
    $('#share-' + name + '-panel').hidden = !active;
  }
  if (tab === 'requests') return loadDeviceRequests();
}

async function openShareProject(tab = 'invite') {
  const project = state.project;
  const form = $('#share-project-form');
  form.reset();
  clearNode($('#share-project-result'));
  $('#share-setup-status').textContent = '';
  clearNode($('#device-requests'));
  openDialog('share-project-dialog');
  const selected = showShareTab(tab);
  const submit = form.querySelector('[type=submit]');
  submit.disabled = true;
  try {
    const [directory, access] = await Promise.all([
      request(projectApi('project-roles')),
      request('/api/remote-access')
    ]);
    if (project !== state.project || !$('#share-project-dialog').open) return;
    $('#share-setup-status').textContent = access.enabled ? '' : 'Peer access is off on this device. Enable it to create remote invitations.';
    $('#share-setup-help').hidden = access.enabled;
    populateSelect(form.elements.role, directory.roles, {
      getValue: role => role.id,
      getLabel: role => role.name,
      selectedValue: 'worker'
    });
    form.elements.role.onchange = () => {
      const role = directory.roles.find(entry => entry.id === form.elements.role.value);
      $('#share-role-permissions').textContent = role ? role.permissions.join(', ') : '';
    };
    form.elements.role.onchange();
    submit.disabled = !access.enabled;
  } catch (error) {
    if (project === state.project) showError(error.message);
  }
  await selected;
}

async function handleSignInPayload(rawText, explicitProjectID) {
  const txt = String(rawText || '').trim();
  if (!txt) throw new Error('Enter a credential JSON payload or token.');
  let payload = { project_id: String(explicitProjectID || '').trim(), token: txt };
  if (txt.startsWith('{')) {
    const parsed = JSON.parse(txt);
    payload = {
      project_id: parsed.project_id || payload.project_id,
      token: parsed.token || parsed.secret || ''
    };
  }
  const session = await request('/api/login', { method: 'POST', body: payload });
  await loadDashboardProjects();
  selectProject(session, { view: 'chat' });
  await refresh();
}

function wireProjectDialogEvents() {
  $$('#projects-tabs .subtab').forEach(btn => {
    btn.addEventListener('click', () => {
      state.projectsTab = btn.dataset.projectsTab === 'joined' ? 'joined' : 'local';
      renderProjectsView();
    });
  });
  $('#open-local-project-btn')?.addEventListener('click', () => openDialog('local-project-dialog'));
  $('#rail-add-project-btn')?.addEventListener('click', () => {
    if ($('#project-switcher-menu')) $('#project-switcher-menu').hidden = true;
    $('#project-switcher-btn')?.setAttribute('aria-expanded', 'false');
    setMobileRailOpen(false);
    openDialog('add-project-dialog');
  });
  for (const [choice, target] of [['local', 'local-project-dialog'], ['remote', 'join-remote-dialog']]) {
    $('#choose-' + choice + '-project')?.addEventListener('click', () => {
      closeDialog('add-project-dialog');
      openDialog(target);
    });
  }
  $('#open-join-remote-btn')?.addEventListener('click', () => openDialog('join-remote-dialog'));
  $('#open-credential-import-btn')?.addEventListener('click', () => openDialog('credential-import-dialog'));

  const checkoutForm = $('#local-project-form');
  const checkoutPath = $('#local-path-input');
  const checkoutName = $('#local-name-input');
  const checkoutPreview = $('#checkout-preview');
  const checkoutStatus = $('#checkout-status');
  const checkoutInitialFoot = $('#checkout-initial-foot');
  const checkoutDetails = $('#checkout-details');
  const inspectCheckoutButton = $('#inspect-checkout-btn');
  const browseCheckoutButton = $('#browse-checkout-btn');
  const createLocalButton = $('#create-local-submit');
  let checkoutRequest = null;
  let suggestedCheckoutName = '';

  const resetLocalCheckoutDialog = ({ clearPath = true } = {}) => {
    if (clearPath) checkoutForm?.reset();
    checkoutRequest = null;
    if (checkoutPreview) {
      clearNode(checkoutPreview);
      checkoutPreview.hidden = true;
    }
    if (checkoutDetails) checkoutDetails.hidden = true;
    if (checkoutInitialFoot) checkoutInitialFoot.hidden = false;
    if (checkoutStatus) checkoutStatus.textContent = '';
    if (clearPath || checkoutName.value === suggestedCheckoutName) checkoutName.value = '';
    suggestedCheckoutName = '';
  };

  $('#local-project-dialog')?.addEventListener('close', () => resetLocalCheckoutDialog({ clearPath: true }));

  checkoutPath?.addEventListener('input', () => {
    resetLocalCheckoutDialog({ clearPath: false });
  });

  browseCheckoutButton?.addEventListener('click', async () => {
    browseCheckoutButton.disabled = true;
    checkoutStatus.textContent = 'Opening folder picker…';
    try {
      const result = await request('/api/local/pick-checkout', { method: 'POST', body: {} });
      if (!$('#local-project-dialog').open) return;
      if (!result.selected) {
        checkoutStatus.textContent = 'No folder selected. You can enter the checkout path instead.';
        return;
      }
      checkoutPath.value = result.local_path || '';
      checkoutPath.dispatchEvent(new Event('input', { bubbles: true }));
      checkoutPath.value = result.local_path || '';
      inspectCheckoutButton.click();
    } catch (error) {
      if ($('#local-project-dialog').open) checkoutStatus.textContent = error.message + ' You can enter the checkout path instead.';
    } finally {
      browseCheckoutButton.disabled = false;
    }
  });

  inspectCheckoutButton?.addEventListener('click', async () => {
    const path = checkoutPath.value.trim();
    if (!path) {
      checkoutPath.reportValidity();
      return;
    }
    inspectCheckoutButton.disabled = true;
    checkoutRequest = null;
    checkoutDetails.hidden = true;
    if (checkoutInitialFoot) checkoutInitialFoot.hidden = false;
    checkoutPreview.hidden = true;
    checkoutStatus.textContent = 'Checking checkout…';
    try {
      const result = await request('/api/local/inspect-checkout', { method: 'POST', body: { local_path: path } });
      if (checkoutPath.value.trim() !== path || !$('#local-project-dialog').open) return;
      clearNode(checkoutPreview);
      checkoutPreview.hidden = false;
      const observation = result.observation || {};
      const entries = [
        ['Repository', result.repository_name || result.suggested_name],
        ['Folder', observation.root || path],
        ['Branch', observation.branch || 'Detached HEAD'],
        ['Remote', observation.remote_hints?.[0]]
      ];
      const facts = createEl('dl', 'checkout-facts');
      for (const [label, value] of entries) {
        if (value) facts.append(createEl('dt', '', label), createEl('dd', '', value));
      }
      checkoutPreview.appendChild(facts);
      const matches = result.existing_matches || [];
      for (const match of matches) {
        const project = state.localProjects.find(item => item.project_id === match.project_id);
        if (project?.status !== 'ready') continue;
        checkoutPreview.appendChild(uiButton({
          label: 'Open ' + match.project_name,
          cls: 'btn btn-primary',
          onClick: async () => {
            closeDialog('local-project-dialog');
            await openLocalProject(project, 'chat');
          }
        }));
      }
      if (matches.length) {
        checkoutStatus.textContent = 'This checkout already matches a project.';
        if (!checkoutPreview.querySelector('button')) {
          checkoutPreview.appendChild(createEl('p', 'muted small', 'Open Projects to resolve access to the existing project.'));
        }
        return;
      }
      checkoutRequest = { path, name: '', id: '' };
      suggestedCheckoutName = result.suggested_name || result.repository_name || '';
      if (!checkoutName.value.trim()) checkoutName.value = suggestedCheckoutName;
      checkoutDetails.hidden = false;
      if (checkoutInitialFoot) checkoutInitialFoot.hidden = true;
      checkoutStatus.textContent = '';
      checkoutName.focus();
    } catch (error) {
      if (checkoutPath.value.trim() === path) checkoutStatus.textContent = error.message;
    } finally {
      inspectCheckoutButton.disabled = false;
    }
  });

  checkoutForm?.addEventListener('submit', async event => {
    event.preventDefault();
    const path = checkoutPath.value.trim();
    if (checkoutRequest?.path !== path) {
      inspectCheckoutButton.click();
      return;
    }
    const name = checkoutName.value.trim();
    if (!name || createLocalButton.disabled) return;
    if (!checkoutRequest.id || checkoutRequest.name !== name) {
      checkoutRequest.name = name;
      checkoutRequest.id = 'web-' + crypto.randomUUID();
    }
    createLocalButton.disabled = true;
    inspectCheckoutButton.disabled = true;
    checkoutPath.readOnly = true;
    checkoutName.readOnly = true;
    checkoutStatus.textContent = 'Creating project…';
    try {
      const result = await request('/api/projects/create', {
        method: 'POST',
        body: { name, local_path: path, request_id: checkoutRequest.id }
      });
      await loadDashboardProjects();
      closeDialog('local-project-dialog');
      checkoutForm.reset();
      checkoutPath.dispatchEvent(new Event('input'));
      selectProject(result.project, { view: 'chat' });
      await refresh();
    } catch (error) {
      checkoutStatus.textContent = error.message;
    } finally {
      createLocalButton.disabled = false;
      inspectCheckoutButton.disabled = false;
      checkoutPath.readOnly = false;
      checkoutName.readOnly = false;
    }
  });

  const joinConnectStatus = $('#join-connect-status');
  const setJoinMethod = method => {
    const importing = method === 'credential';
    $('#accept-invitation-form').hidden = importing;
    $('#join-existing-credential').hidden = !importing;
    $('#join-credential-tab').setAttribute('aria-pressed', String(importing));
    $('#join-invitation-tab').setAttribute('aria-pressed', String(!importing));
  };
  if ($('#join-invitation-tab')) $('#join-invitation-tab').onclick = () => setJoinMethod('invitation');
  if ($('#join-credential-tab')) $('#join-credential-tab').onclick = () => setJoinMethod('credential');

  const invitationForm = $('#accept-invitation-form');
  const invitationFile = $('#accept-invitation-file');
  const invitationPreview = $('#accept-invitation-preview');
  const invitationStatus = $('#accept-invitation-status');
  const invitationButton = invitationForm?.querySelector('[type=submit]');
  let invitationBundle;

  invitationFile?.addEventListener('change', async () => {
    const file = invitationFile.files[0];
    invitationBundle = null;
    invitationPreview.textContent = '';
    invitationStatus.textContent = '';
    invitationButton.textContent = 'Accept invitation';
    if (!file) return;
    try {
      if (file.size > 65536) throw new Error('Invitation file exceeds 64 KiB.');
      const bundle = JSON.parse(await file.text());
      if (invitationFile.files[0] !== file) return;
      if (!bundle.invitation?.id || !bundle.invitation.project_id || !bundle.home_id || !bundle.endpoint || !bundle.server_ca) {
        throw new Error('Choose a Gossip invitation JSON file.');
      }
      if (!(Number(bundle.invitation.expires_at) > Date.now())) throw new Error('This invitation has expired.');
      invitationBundle = bundle;
      invitationPreview.textContent = bundle.endpoint + ' · ' + bundle.invitation.project_id + ' · Role: ' + bundle.invitation.role + ' · Expires ' + new Date(bundle.invitation.expires_at).toLocaleString();
    } catch (error) {
      invitationStatus.textContent = error.message;
    }
  });

  invitationForm?.querySelector('details')?.addEventListener('input', () => {
    invitationBundle = null;
    invitationFile.value = '';
    invitationPreview.textContent = '';
    invitationStatus.textContent = '';
    invitationButton.textContent = 'Accept invitation';
  });

  invitationForm?.addEventListener('submit', async event => {
    event.preventDefault();
    const fields = invitationForm.elements;
    const bundle = invitationBundle || {
      endpoint: fields.endpoint.value.trim(),
      home_id: fields.home.value.trim(),
      server_ca: fields.ca.value.trim(),
      invitation: { id: fields.invite.value.trim(), project_id: fields.project.value.trim() }
    };
    if (!bundle.endpoint || !bundle.home_id || !bundle.server_ca || !bundle.invitation.id || !bundle.invitation.project_id) {
      invitationStatus.textContent = 'Choose an invitation file or enter all invitation details.';
      return;
    }
    invitationBundle = bundle;
    invitationButton.disabled = true;
    try {
      const accepted = await request('/api/local/accept-invitation', {
        method: 'POST',
        body: { bundle, code: $('#accept-invitation-code').value.trim() }
      });
      if (invitationBundle !== bundle || !$('#join-remote-dialog').open) return;
      if (accepted.state === 'pending') {
        invitationStatus.textContent = 'Pending owner approval. Compare this device fingerprint with the owner.\n' + accepted.fingerprint;
        invitationButton.textContent = 'Check approval and connect';
        return;
      }
      await loadDashboardProjects();
      const project = state.remoteProjects.find(row => row.project_id === accepted.project_id && row.home_id === bundle.home_id);
      closeDialog('join-remote-dialog');
      if (project) await openRemoteProject(project);
      else render();
    } catch (error) {
      if (invitationBundle === bundle) invitationStatus.textContent = error.message;
    } finally {
      invitationButton.disabled = false;
    }
  });

  $('#join-remote-dialog')?.addEventListener('close', () => {
    invitationBundle = null;
    invitationForm?.reset();
    $('#join-remote-form')?.reset();
    if ($('#join-credential-details')) $('#join-credential-details').open = false;
    setJoinMethod('invitation');
    joinConnectStatus.textContent = '';
    $('#join-credential-status').textContent = 'Use a credential issued for this device, or enter its details below.';
    invitationPreview.textContent = '';
    invitationStatus.textContent = '';
    if (invitationButton) invitationButton.textContent = 'Accept invitation';
  });

  $('#join-credential-file')?.addEventListener('change', async e => {
    const file = e.target.files[0];
    if (!file) return;
    const status = $('#join-credential-status');
    try {
      if (file.size > 65536) throw new Error('Credential file exceeds 64 KiB.');
      const data = JSON.parse(await file.text());
      const fields = { project_id: 'project-id', principal_id: 'principal-id', token: 'token' };
      for (const key of Object.keys(fields)) {
        if (typeof data?.[key] !== 'string' || !data[key].trim()) throw new Error('Choose a Gossip project credential JSON file.');
      }
      for (const [key, id] of Object.entries(fields)) $('#join-' + id).value = data[key];
      if (typeof data.home_id === 'string') $('#join-home-id').value = data.home_id;
      if (typeof data.endpoint === 'string') $('#join-endpoint').value = data.endpoint;
      const certFields = [['ca_file', 'ca-file'], ['cert_file', 'cert-file'], ['key_file', 'key-file'], ['tls_ca', 'ca-file'], ['tls_cert', 'cert-file'], ['tls_key', 'key-file']];
      for (const [key, id] of certFields) {
        if (typeof data[key] === 'string' && data[key].trim() && !$('#join-' + id).value) $('#join-' + id).value = data[key].trim();
      }
      status.textContent = 'Credential loaded. Confirm the endpoint; add this device’s certificate paths under “Credential fields and client certificates” if needed.';
    } catch (error) {
      for (const field of ['project-id', 'principal-id', 'token']) $('#join-' + field).value = '';
      status.textContent = error.message;
    } finally {
      e.target.value = '';
    }
  });

  $('#join-remote-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    const submit = e.currentTarget.querySelector('[type=submit]');
    if (submit.disabled) return;
    const requiredConnectionFields = [
      ['join-endpoint', 'Remote Gossip endpoint'], ['join-project-id', 'Project ID'],
      ['join-principal-id', 'Principal ID'], ['join-token', 'Scoped bearer token'],
      ['join-ca-file', 'Home CA certificate path'], ['join-cert-file', 'Client certificate path'],
      ['join-key-file', 'Client private key path']
    ];
    const missing = requiredConnectionFields.find(([id]) => !$('#' + id).value.trim());
    if (missing) {
      if (missing[0] !== 'join-endpoint') $('#join-credential-details').open = true;
      joinConnectStatus.textContent = `Enter ${missing[1]} to connect. The credential file supplies the project identity and token; mTLS certificate paths may need to be added separately.`;
      $('#' + missing[0]).focus();
      return;
    }
    submit.disabled = true;
    joinConnectStatus.textContent = 'Connecting to the project…';
    try {
      const res = await request('/api/local/join-remote', {
        method: 'POST',
        body: {
          name: $('#join-name')?.value?.trim() || '',
          endpoint: $('#join-endpoint')?.value?.trim() || '',
          home_id: $('#join-home-id')?.value?.trim() || '',
          project_id: $('#join-project-id')?.value?.trim() || '',
          principal_id: $('#join-principal-id')?.value?.trim() || '',
          token: $('#join-token')?.value || '',
          ca_file: $('#join-ca-file')?.value?.trim() || '',
          cert_file: $('#join-cert-file')?.value?.trim() || '',
          key_file: $('#join-key-file')?.value?.trim() || ''
        }
      });
      $('#join-remote-form').reset();
      $('#join-credential-status').textContent = 'Use a credential issued for this device, or enter its details below.';
      closeDialog('join-remote-dialog');
      await loadDashboardProjects();
      const rp = state.remoteProjects.find(x => x.project_id === res.project_id && x.home_id === res.home_id);
      if (rp) await openRemoteProject(rp);
      else render();
    } catch (err) {
      joinConnectStatus.textContent = err.message;
    } finally {
      submit.disabled = false;
    }
  });

  $('#login-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    try {
      await handleSignInPayload($('#credential-welcome')?.value, $('#login-project-id')?.value);
    } catch (err) {
      showError(err.message);
    }
  });

  $('#credential-import-form')?.addEventListener('submit', async e => {
    e.preventDefault();
    try {
      await handleSignInPayload($('#credential')?.value, '');
      closeDialog('credential-import-dialog');
    } catch (err) {
      showError(err.message);
    }
  });

  for (const tab of ['invite', 'requests']) {
    $('#share-' + tab + '-tab')?.addEventListener('click', () => showShareTab(tab));
  }
  $('#refresh-device-requests')?.addEventListener('click', loadDeviceRequests);
  $('#share-project-dialog')?.addEventListener('close', () => clearNode($('#share-project-result')));
  $('#share-project-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const project = state.project;
    const form = event.currentTarget;
    const result = $('#share-project-result');
    const submit = form.querySelector('[type=submit]');
    if (submit.disabled) return;
    submit.disabled = true;
    try {
      const data = await request(projectApi('remote-invitations'), {
        method: 'POST',
        body: {
          operation: 'create',
          name: form.elements.invitee.value,
          role: form.elements.role.value,
          endpoint: form.elements.endpoint.value,
          server_ca: form.elements.serverCA.value
        }
      });
      if (project !== state.project || !$('#share-project-dialog').open) return;
      const { code, ...bundle } = data;
      const blob = URL.createObjectURL(new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json' }));
      const download = createEl('a', 'btn btn-secondary', 'Download invitation');
      download.href = blob;
      download.download = 'gossip-invitation.json';
      $('#share-project-dialog').addEventListener('close', () => URL.revokeObjectURL(blob), { once: true });
      result.replaceChildren(download, createEl('p', '', 'Pairing code'), createEl('code', 'agent-dm-body', code));
      download.focus();
    } catch (error) {
      if (project === state.project) result.textContent = error.message;
    } finally {
      if (project === state.project) submit.disabled = false;
    }
  });
}
