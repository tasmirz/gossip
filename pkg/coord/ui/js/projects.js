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

function updateRequestsBadge(invitations) {
  const badge = $('#share-requests-badge');
  if (!badge) return;
  const now = Date.now();
  const pendingCount = (Array.isArray(invitations) ? invitations : []).filter(
    inv => inv.state === 'pending' && (!(inv.expires_at > 0) || inv.expires_at > now)
  ).length;
  badge.hidden = pendingCount === 0;
  badge.textContent = String(pendingCount);
}

function renderInviteApprovalBox(project, invite, directory, onDone) {
  const box = createEl('div', 'verify-code-box');
  box.appendChild(createEl('strong', '', 'Compare this code with the joining device before accepting'));
  if (invite.verification_code) {
    box.appendChild(createEl('div', 'verify-code-value', invite.verification_code));
  }
  if (invite.ssh_fingerprint) {
    box.appendChild(createEl('div', 'muted small peer-endpoint-preview', 'Device key: ' + invite.ssh_fingerprint));
  }
  const role = directory?.roles?.find(entry => entry.id === invite.role);
  if (role) {
    box.appendChild(createEl('div', 'muted small', role.name + ' (' + role.permissions.join(', ') + ')'));
  }
  const actionsRow = createEl('div', 'peer-access-actions');
  if (role) {
    const acceptBtn = uiButton({
      label: 'Accept',
      cls: 'btn btn-primary btn-sm',
      onClick: async () => {
        if (project !== state.project) return;
        const details = [
          invite.verification_code ? 'Verification code: ' + invite.verification_code : '',
          invite.ssh_fingerprint ? 'Device key: ' + invite.ssh_fingerprint : '',
          'Accept only if this code matches the joining device.'
        ].filter(Boolean).join('\n');
        if (!await askConfirmation('Accept ' + invite.name + ' for ' + project.project_name + '?', details)) return;
        if (project !== state.project) return;
        await runGuardedAction(acceptBtn, async () => {
          await request(projectApi('remote-invitations'), { method: 'POST', body: { operation: 'approved', id: invite.id } });
          if (onDone) await onDone();
        });
      }
    });
    actionsRow.appendChild(acceptBtn);
  }
  const denyBtn = uiButton({
    label: 'Deny',
    cls: 'btn btn-secondary btn-sm',
    onClick: async () => {
      if (project !== state.project) return;
      await runGuardedAction(denyBtn, async () => {
        await request(projectApi('remote-invitations'), { method: 'POST', body: { operation: 'denied', id: invite.id } });
        if (onDone) await onDone();
      });
    }
  });
  actionsRow.appendChild(denyBtn);
  box.appendChild(actionsRow);
  return box;
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
    shareDirectorySnapshot = directory;
    updateRequestsBadge(invitations);
    renderList(wrap, invitations, 'No invitations yet.', invite => {
      const row = createEl('article', 'list-item');
      row.appendChild(createEl('strong', '', invite.name));
      const expired = invite.expires_at > 0 && invite.expires_at <= Date.now();
      const stage = {
        issued: 'Waiting for recipient',
        pending: 'Pending verification',
        approved: 'Accepted — finishing connection',
        enrolled: 'Connected',
        denied: 'Denied',
        cancelled: 'Cancelled'
      }[invite.state] || invite.state;
      row.appendChild(createEl('p', 'muted', invite.role + ' · ' + (expired && ['issued', 'pending', 'approved'].includes(invite.state) ? 'Expired' : stage)));
      if (!expired && invite.state === 'pending') {
        row.appendChild(renderInviteApprovalBox(project, invite, directory, loadDeviceRequests));
        return row;
      }
      if (invite.verification_code) {
        row.appendChild(createEl('p', 'small', 'Verification code: ' + invite.verification_code));
      }
      if (invite.ssh_fingerprint) {
        row.appendChild(createEl('code', 'agent-dm-body', invite.ssh_fingerprint));
      } else if (invite.fingerprint) {
        row.appendChild(createEl('code', 'agent-dm-body', invite.fingerprint));
      }
      if (!expired && ['issued', 'approved'].includes(invite.state)) {
        const cancelBtn = uiButton({
          label: 'Cancel invitation',
          cls: 'btn btn-secondary',
          onClick: async () => {
            if (project !== state.project) return;
            await runGuardedAction(cancelBtn, async () => {
              await request(projectApi('remote-invitations'), { method: 'POST', body: { operation: 'cancelled', id: invite.id } });
              await loadDeviceRequests();
            });
          }
        });
        row.appendChild(cancelBtn);
      }
      return row;
    }, 'muted');
  } catch (error) {
    if (project === state.project) wrap.textContent = error.message;
  }
}

function generateInvitePIN() {
  const limit = Math.floor(0x100000000 / 1000000) * 1000000;
  const buf = new Uint32Array(1);
  let val = 0;
  do {
    crypto.getRandomValues(buf);
    val = buf[0];
  } while (val >= limit);
  return String(val % 1000000).padStart(6, '0');
}

function normalizeInviteSecret(raw) {
  const trimmed = String(raw || '').trim();
  if (/^\d{3}[\s-]\d{3}$/.test(trimmed)) {
    return trimmed.replace(/[\s-]/g, '');
  }
  return trimmed;
}

let shareAccessSnapshot = null;
let shareDirectorySnapshot = null;
let shareWatcherTimer = null;

function stopShareInviteWatcher() {
  if (shareWatcherTimer) {
    clearInterval(shareWatcherTimer);
    shareWatcherTimer = null;
  }
}

function setShareMode(mode) {
  const normalized = mode === 'frp' ? 'frp' : 'lan';
  // OpenSSH relay share mode commented out: mode === 'ssh' ? 'ssh' : ...
  const form = $('#share-project-form');
  if (form) form.dataset.mode = normalized;
  $('#share-mode-lan')?.setAttribute('aria-pressed', String(normalized === 'lan'));
  $('#share-mode-frp')?.setAttribute('aria-pressed', String(normalized === 'frp'));
  $('#share-mode-ssh')?.setAttribute('aria-pressed', String(normalized === 'ssh'));
  if ($('#share-lan-box')) $('#share-lan-box').hidden = normalized !== 'lan';
  if ($('#share-frp-box')) $('#share-frp-box').hidden = normalized !== 'frp';
  if ($('#share-ssh-box')) $('#share-ssh-box').hidden = normalized !== 'ssh';
  updateShareSetupStatus();
}

function updateShareSetupStatus() {
  const statusEl = $('#share-setup-status');
  const form = $('#share-project-form');
  const mode = form?.dataset.mode || 'lan';
  const access = shareAccessSnapshot;
  const submit = form?.querySelector('[type=submit]');
  const inlineServer = ($('#share-frp-server')?.value || '').trim();
  const canAutoFRP = Boolean(access && (access.frp_enabled || access.frp_server || access.frp_relay_server || access.frp_local_relay_enabled || inlineServer));
  if (submit && access) {
    const blockedFRP = mode === 'frp' && !access.frp_binary_available;
    const blockedSSH = mode === 'ssh' && !access.ssh_enabled;
    submit.disabled = blockedFRP || blockedSSH;
  }
  if (!statusEl) return;
  if (!access) {
    statusEl.textContent = '';
    return;
  }
  if (mode === 'frp') {
    if (!access.frp_binary_available) {
      statusEl.textContent = 'Companion gossip-frp binary is not installed (running without FRP support).';
    } else if (access.frp_enabled) {
      statusEl.textContent = 'Using this project\'s isolated FRP tunnel (' + (access.frp_endpoint || access.frp_server || 'running') + ').';
    } else if (canAutoFRP) {
      statusEl.textContent = 'Ready — clicking Create will auto-start port ' + (access.frp_remote_port || 18443) + ' and download the .gossip file.';
    } else {
      statusEl.textContent = 'Enter your VPS Relay Host below, or leave it blank to auto-start the built-in local FRP relay.';
    }
    return;
  }
  /* Commented out: OpenSSH MCP relaying share mode
  if (mode === 'ssh') {
    if (access.ssh_enabled) {
      statusEl.textContent = 'Using this project\'s Settings-configured SSH access (' + (access.ssh_mode || 'direct') + ' · ' + (access.ssh_endpoint || access.ssh_local_addr || 'active') + ').';
    } else {
      statusEl.textContent = 'SSH access is off for this project. Enable it in Settings → Connections → Remote access.';
    }
    return;
  }
  */
  if (access.lan_enabled) {
    statusEl.textContent = 'LAN peer listener is active on port ' + (access.active_port || access.listen_port || 47282) + '.';
  } else {
    statusEl.textContent = 'LAN peer listener is currently off — Gossip will auto-start it on port ' + (access.listen_port || 47282) + ' when you create this invite.';
  }
}

function updateShareLANPreview(access) {
  const preview = $('#share-lan-endpoint-preview');
  const selectField = $('#share-lan-select-field');
  const select = $('#share-lan-select');
  if (!preview || !select) return;
  const port = Number(access?.listen_port) || 47282;
  const ips = Array.isArray(access?.detected_lan_ips) ? access.detected_lan_ips : [];
  if (!ips.length) {
    select.replaceChildren();
    if (selectField) selectField.hidden = true;
    preview.textContent = 'No non-loopback LAN IPv4 detected. Enter a custom HTTPS URL under Advanced if needed.';
    return;
  }
  select.replaceChildren(...ips.map(ip => {
    const opt = document.createElement('option');
    opt.value = ip;
    opt.textContent = ip + ' (https://' + ip + ':' + port + ')';
    return opt;
  }));
  select.value = ips[0];
  if (selectField) selectField.hidden = ips.length <= 1;
  preview.textContent = 'https://' + select.value + ':' + port;
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
  stopShareInviteWatcher();
  form.reset();
  clearNode($('#share-project-result'));
  $('#share-setup-status').textContent = '';
  clearNode($('#device-requests'));
  shareAccessSnapshot = null;
  shareDirectorySnapshot = null;
  updateRequestsBadge([]);
  if ($('#share-passphrase')) $('#share-passphrase').value = generateInvitePIN();
  if ($('#share-frp-server')) $('#share-frp-server').value = '';
  if ($('#share-frp-summary')) $('#share-frp-summary').textContent = 'Checking project FRP configuration...';
  if ($('#share-ssh-summary')) $('#share-ssh-summary').textContent = 'Checking project SSH configuration...';
  setShareMode('lan');
  openDialog('share-project-dialog');
  const selected = showShareTab(tab);
  const submit = form.querySelector('[type=submit]');
  submit.disabled = true;
  try {
    const [directory, access, invitations] = await Promise.all([
      request(projectApi('project-roles')),
      request('/api/remote-access?project=' + encodeURIComponent(project.project_id)),
      request(projectApi('remote-invitations')).catch(() => [])
    ]);
    if (project !== state.project || !$('#share-project-dialog').open) return;
    shareAccessSnapshot = access;
    shareDirectorySnapshot = directory;
    state.peerAccess = access;
    state.remoteAccessEnabled = Boolean(access?.enabled);
    updateRequestsBadge(invitations);
    updateShareLANPreview(access);

    if ($('#share-frp-server') && !$('#share-frp-server').value) {
      $('#share-frp-server').value = access?.frp_server || access?.frp_relay_server || '';
    }
    const projLabel = project.project_name || project.project_id;
    if ($('#share-frp-summary')) {
      if (!access?.frp_binary_available) {
        $('#share-frp-summary').textContent = 'Companion gossip-frp binary is not installed (running without FRP support).';
      } else if (access?.frp_enabled) {
        $('#share-frp-summary').textContent = 'Enabled for ' + projLabel + ': ' + (access.frp_endpoint || access.frp_server) + ' · ' + (access.frp_state || 'running');
      } else if (access?.frp_server || access?.frp_relay_server || access?.frp_local_relay_enabled) {
        const relayLabel = access.frp_server || access.frp_relay_server || 'built-in local relay';
        $('#share-frp-summary').textContent = 'Ready for 1-click auto-start via ' + relayLabel + ' (port ' + (access.frp_remote_port || 18443) + ').';
      } else {
        $('#share-frp-summary').textContent = '1-click ready: enter a relay host or leave blank for built-in local relay.';
      }
    }
    if ($('#share-ssh-summary')) {
      if (access?.ssh_enabled) {
        const sshDesc = (access.ssh_mode || 'direct') + ' · ' + (access.ssh_endpoint || access.ssh_local_addr || '');
        $('#share-ssh-summary').textContent = 'Enabled for ' + projLabel + ': ' + sshDesc + (access.ssh_target ? ' via ' + access.ssh_target : '');
      } else {
        $('#share-ssh-summary').textContent = 'SSH access is off for ' + projLabel + '. Enable direct OpenSSH or reverse relay in Settings → Connections → Remote access.';
      }
    }

    const defaultMode = access?.lan_enabled ? 'lan' : access?.frp_enabled ? 'frp' : 'lan';
    setShareMode(defaultMode);

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
    updateShareSetupStatus();
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
  let joinPollTimer = null;
  let sshProbeState = null;
  let sshSelectionSeq = 0;

  const stopJoinPoll = () => {
    if (joinPollTimer) {
      clearInterval(joinPollTimer);
      joinPollTimer = null;
    }
  };

  const renderVerificationCard = (box, data, note) => {
    if (!box) return;
    box.hidden = false;
    const kids = [
      createEl('strong', '', 'Verification code (match this with the host owner)')
    ];
    if (data?.verification_code) {
      kids.push(createEl('div', 'verify-code-value', data.verification_code));
    }
    if (data?.ssh_fingerprint) {
      kids.push(createEl('div', 'muted small peer-endpoint-preview', 'Device key: ' + data.ssh_fingerprint));
    }
    if (data?.host_fingerprint) {
      kids.push(createEl('div', 'muted small peer-endpoint-preview', 'Host CA: ' + data.host_fingerprint));
    }
    if (note) {
      kids.push(createEl('p', 'muted small', note));
    }
    box.replaceChildren(...kids);
  };

  const finishJoinedProject = async (accepted, fallbackHomeID) => {
    stopJoinPoll();
    await loadDashboardProjects();
    const targetHomeID = accepted.home_id || fallbackHomeID;
    const project = state.remoteProjects.find(row =>
      row.project_id === accepted.project_id && (!targetHomeID || row.home_id === targetHomeID)
    );
    closeDialog('join-remote-dialog');
    if (project) await openRemoteProject(project);
    else render();
  };

  const startJoinPoll = (seq, checkFn, isSameSeq) => {
    stopJoinPoll();
    joinPollTimer = setInterval(async () => {
      if (!isSameSeq(seq) || !$('#join-remote-dialog')?.open) {
        stopJoinPoll();
        return;
      }
      try {
        await checkFn();
      } catch {
        // Retry silently on the next interval while owner approval is pending.
      }
    }, 2000);
  };

  const setJoinMethod = method => {
    stopJoinPoll();
    const normalized = method === 'credential' ? 'credential' : method === 'ssh' ? 'ssh' : 'invitation';
    $('#accept-invitation-form').hidden = normalized !== 'invitation';
    if ($('#join-ssh-section')) $('#join-ssh-section').hidden = normalized !== 'ssh';
    $('#join-existing-credential').hidden = normalized !== 'credential';
    $('#join-invitation-tab')?.setAttribute('aria-pressed', String(normalized === 'invitation'));
    $('#join-ssh-tab')?.setAttribute('aria-pressed', String(normalized === 'ssh'));
    $('#join-credential-tab')?.setAttribute('aria-pressed', String(normalized === 'credential'));
  };
  if ($('#join-invitation-tab')) $('#join-invitation-tab').onclick = () => setJoinMethod('invitation');
  if ($('#join-ssh-tab')) $('#join-ssh-tab').onclick = () => setJoinMethod('ssh');
  if ($('#join-credential-tab')) $('#join-credential-tab').onclick = () => setJoinMethod('credential');

  const invitationForm = $('#accept-invitation-form');
  const invitationDropzone = $('#accept-invitation-dropzone');
  const invitationFile = $('#accept-invitation-file');
  const invitationPreview = $('#accept-invitation-preview');
  const invitationStatus = $('#accept-invitation-status');
  const invitationVerifyBox = $('#accept-verification-box');
  const invitationButton = invitationForm?.querySelector('[type=submit]');
  let invitationBundle = null;
  let encryptedInviteText = '';
  let inviteSelectionSeq = 0;

  const loadInvitationFile = async file => {
    const seq = ++inviteSelectionSeq;
    stopJoinPoll();
    invitationBundle = null;
    encryptedInviteText = '';
    invitationPreview.textContent = '';
    invitationStatus.textContent = '';
    if (invitationVerifyBox) {
      invitationVerifyBox.hidden = true;
      clearNode(invitationVerifyBox);
    }
    if (invitationButton) invitationButton.textContent = 'Join with PIN';
    if (!file) return;
    try {
      if (file.size > 65536) throw new Error('Invitation file exceeds 64 KiB.');
      const rawText = await file.text();
      if (seq !== inviteSelectionSeq) return;
      const parsed = JSON.parse(rawText);
      if (parsed?.format === 'gossip-encrypted-invite-v1' && parsed.ciphertext && parsed.salt && parsed.nonce) {
        encryptedInviteText = rawText;
        if ($('#accept-manual-details')) $('#accept-manual-details').open = false;
        invitationPreview.textContent = file.name + ' · Encrypted Gossip invite (' + Math.max(1, Math.round(file.size / 1024)) + ' KiB) — enter the 6-digit PIN.';
        $('#accept-invitation-code')?.focus();
        return;
      }
      if (!parsed.invitation?.id || !parsed.invitation.project_id || !parsed.home_id || !parsed.endpoint || !parsed.server_ca) {
        throw new Error('Choose a Gossip .gossip or invitation JSON file.');
      }
      if (!(Number(parsed.invitation.expires_at) > Date.now())) throw new Error('This invitation has expired.');
      invitationBundle = parsed;
      invitationPreview.textContent = parsed.endpoint + ' · ' + parsed.invitation.project_id + ' · Role: ' + parsed.invitation.role + ' · Expires ' + new Date(parsed.invitation.expires_at).toLocaleString();
    } catch (error) {
      invitationStatus.textContent = error.message;
    }
  };

  invitationFile?.addEventListener('change', () => {
    void loadInvitationFile(invitationFile.files[0]);
  });

  if (invitationDropzone) {
    invitationDropzone.addEventListener('dragover', event => {
      event.preventDefault();
      invitationDropzone.classList.add('is-dragover');
    });
    invitationDropzone.addEventListener('dragleave', () => {
      invitationDropzone.classList.remove('is-dragover');
    });
    invitationDropzone.addEventListener('drop', event => {
      event.preventDefault();
      invitationDropzone.classList.remove('is-dragover');
      const dropped = event.dataTransfer?.files?.[0];
      if (dropped) {
        try {
          const dt = new DataTransfer();
          dt.items.add(dropped);
          if (invitationFile) invitationFile.files = dt.files;
        } catch {
          // Ignore FileList assignment on browsers that disallow programmatic assignment.
        }
        void loadInvitationFile(dropped);
      }
    });
  }

  invitationForm?.querySelector('details')?.addEventListener('input', () => {
    inviteSelectionSeq++;
    stopJoinPoll();
    invitationBundle = null;
    encryptedInviteText = '';
    invitationFile.value = '';
    invitationPreview.textContent = '';
    invitationStatus.textContent = '';
    if (invitationVerifyBox) {
      invitationVerifyBox.hidden = true;
      clearNode(invitationVerifyBox);
    }
    if (invitationButton) invitationButton.textContent = 'Join with PIN';
  });

  invitationForm?.addEventListener('submit', async event => {
    event.preventDefault();
    const fields = invitationForm.elements;
    const seq = inviteSelectionSeq;
    const code = normalizeInviteSecret($('#accept-invitation-code').value);
    if (!code) {
      invitationStatus.textContent = 'Enter the 6-digit PIN from the project owner.';
      return;
    }

    let requestBody;
    let fallbackHomeID = '';
    if (encryptedInviteText) {
      requestBody = { encrypted_file: encryptedInviteText, passphrase: code };
    } else {
      const bundle = invitationBundle || {
        endpoint: fields.endpoint.value.trim(),
        home_id: fields.home.value.trim(),
        server_ca: fields.ca.value.trim(),
        invitation: { id: fields.invite.value.trim(), project_id: fields.project.value.trim() }
      };
      if (!bundle.endpoint || !bundle.home_id || !bundle.server_ca || !bundle.invitation.id || !bundle.invitation.project_id) {
        invitationStatus.textContent = 'Choose a .gossip / invitation file or enter all legacy invitation details.';
        return;
      }
      invitationBundle = bundle;
      fallbackHomeID = bundle.home_id;
      requestBody = { bundle, code };
    }

    invitationButton.disabled = true;
    invitationStatus.textContent = 'Decrypting and connecting to host...';
    try {
      const accepted = await request('/api/local/accept-invitation', {
        method: 'POST',
        body: requestBody
      });
      if (seq !== inviteSelectionSeq || !$('#join-remote-dialog').open) return;
      if (accepted.state === 'pending') {
        renderVerificationCard(
          invitationVerifyBox,
          accepted,
          'Waiting for the project owner to accept on the host (auto-checking every 2 seconds)...'
        );
        invitationStatus.textContent = '';
        invitationButton.textContent = 'Check approval now';
        startJoinPoll(seq, async () => {
          const next = await request('/api/local/accept-invitation', { method: 'POST', body: requestBody });
          if (seq !== inviteSelectionSeq || !$('#join-remote-dialog').open) return;
          if (next.state !== 'pending') {
            await finishJoinedProject(next, fallbackHomeID);
          }
        }, currentSeq => currentSeq === inviteSelectionSeq);
        return;
      }
      await finishJoinedProject(accepted, fallbackHomeID);
    } catch (error) {
      if (seq === inviteSelectionSeq) invitationStatus.textContent = error.message;
    } finally {
      invitationButton.disabled = false;
    }
  });

  const sshForm = $('#join-ssh-form');
  const sshProbeBtn = $('#join-ssh-probe');
  const sshConnectBtn = $('#join-ssh-connect');
  const sshProbeBox = $('#join-ssh-probe-box');
  const sshVerifyBox = $('#join-ssh-verification-box');
  const sshStatus = $('#join-ssh-status');

  const readSSHFormInputs = () => ({
    ssh_target: ($('#join-ssh-target')?.value || '').trim(),
    endpoint: ($('#join-ssh-endpoint')?.value || '').trim(),
    ssh_port: Number($('#join-ssh-port')?.value || 22),
    ssh_remote_port: Number($('#join-ssh-remote-port')?.value || 9443),
    ssh_identity_file: ($('#join-ssh-identity')?.value || '').trim(),
    project_id: ($('#join-ssh-project')?.value || '').trim(),
    invitation_id: ($('#join-ssh-invite')?.value || '').trim(),
    code: ($('#join-ssh-code')?.value || '').trim()
  });

  const renderSSHProbeResult = (probe, resetPrompt = false) => {
    if (!sshProbeBox) return;
    sshProbeBox.hidden = false;
    if (probe.status === 'changed') {
      sshProbeBox.classList.add('tofu-warning');
      const forgetBtn = createEl('button', 'btn btn-secondary btn-xs', 'Reset stored host key (only if expected)');
      forgetBtn.type = 'button';
      forgetBtn.addEventListener('click', async () => {
        const inputs = readSSHFormInputs();
        await request('/api/local/probe-host', {
          method: 'POST',
          body: { ...inputs, reset_known_host: true, trust_host_key: false }
        }).then(next => {
          sshProbeState = next;
          renderSSHProbeResult(next);
        }).catch(err => {
          if (sshStatus) sshStatus.textContent = err.message;
        });
      });
      sshProbeBox.replaceChildren(
        createEl('strong', '', 'WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED'),
        createEl('p', 'small', 'The host presented a different certificate than previously trusted in known_hosts.'),
        createEl('div', 'muted small peer-endpoint-preview', 'Expected: ' + (probe.expected_fingerprint || '')),
        createEl('div', 'muted small peer-endpoint-preview', 'Received: ' + (probe.host_fingerprint || '')),
        forgetBtn
      );
      return;
    }
    sshProbeBox.classList.remove('tofu-warning');
    if (probe.status === 'untrusted') {
      const trustBtn = createEl('button', 'btn btn-secondary btn-xs', 'Trust host key (add to known_hosts)');
      trustBtn.type = 'button';
      trustBtn.addEventListener('click', async () => {
        const inputs = readSSHFormInputs();
        trustBtn.disabled = true;
        try {
          const trusted = await request('/api/local/probe-host', {
            method: 'POST',
            body: { ...inputs, trust_host_key: true }
          });
          sshProbeState = trusted;
          renderSSHProbeResult(trusted);
        } catch (err) {
          if (sshStatus) sshStatus.textContent = err.message;
        } finally {
          trustBtn.disabled = false;
        }
      });
      sshProbeBox.replaceChildren(
        createEl('strong', '', 'First-time host authenticity check (SSH TOFU)'),
        createEl('p', 'small', 'Confirm this host fingerprint with the owner before connecting:'),
        createEl('div', 'muted small peer-endpoint-preview', (probe.host_key || '') + ' · ' + (probe.host_fingerprint || '')),
        trustBtn
      );
      return;
    }
    sshProbeBox.replaceChildren(
      createEl('strong', '', 'Host key verified in known_hosts'),
      createEl('div', 'muted small peer-endpoint-preview', (probe.host_key || '') + ' · ' + (probe.host_fingerprint || ''))
    );
  };

  for (const sel of ['#join-ssh-target', '#join-ssh-endpoint', '#join-ssh-port', '#join-ssh-remote-port', '#join-ssh-identity']) {
    $(sel)?.addEventListener('input', () => {
      sshSelectionSeq++;
      stopJoinPoll();
      sshProbeState = null;
      if (sshProbeBox) {
        sshProbeBox.hidden = true;
        clearNode(sshProbeBox);
      }
      if (sshVerifyBox) {
        sshVerifyBox.hidden = true;
        clearNode(sshVerifyBox);
      }
      if (sshStatus) sshStatus.textContent = '';
      if (sshConnectBtn) sshConnectBtn.textContent = 'Trust host & request access';
    });
  }

  sshProbeBtn?.addEventListener('click', async () => {
    const inputs = readSSHFormInputs();
    if (!inputs.endpoint && !inputs.ssh_target) {
      if (sshStatus) sshStatus.textContent = 'Enter a Peer HTTPS Endpoint first.';
      return;
    }
    sshProbeBtn.disabled = true;
    if (sshStatus) sshStatus.textContent = 'Probing host key (TOFU)...';
    try {
      const probe = await request('/api/local/probe-host', {
        method: 'POST',
        body: { ...inputs, trust_host_key: false }
      });
      sshProbeState = probe;
      renderSSHProbeResult(probe);
      if (sshStatus) sshStatus.textContent = '';
    } catch (error) {
      sshProbeState = null;
      if (error.data?.status === 'changed') {
        renderSSHProbeResult(error.data);
      }
      if (sshStatus) sshStatus.textContent = error.message;
    } finally {
      sshProbeBtn.disabled = false;
    }
  });

  sshForm?.addEventListener('submit', async event => {
    event.preventDefault();
    const seq = ++sshSelectionSeq;
    const inputs = readSSHFormInputs();
    if (!inputs.endpoint && !inputs.ssh_target) {
      if (sshStatus) sshStatus.textContent = 'Enter a Peer HTTPS Endpoint.';
      return;
    }
    if (!inputs.project_id || !inputs.invitation_id || !inputs.code) {
      if (sshStatus) sshStatus.textContent = 'Enter Project ID, Invitation ID, and pairing code.';
      return;
    }
    if (sshConnectBtn) sshConnectBtn.disabled = true;
    if (sshStatus) sshStatus.textContent = 'Verifying host key & requesting access...';
    try {
      let probe = sshProbeState;
      if (!probe) {
        probe = await request('/api/local/probe-host', {
          method: 'POST',
          body: { ...inputs, trust_host_key: true }
        });
        sshProbeState = probe;
        renderSSHProbeResult(probe);
      } else if (probe.status === 'changed') {
        throw new Error('WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED');
      } else if (probe.status === 'untrusted') {
        probe = await request('/api/local/probe-host', {
          method: 'POST',
          body: { ...inputs, trust_host_key: true }
        });
        sshProbeState = probe;
        renderSSHProbeResult(probe);
      }
      const reqBody = {
        endpoint: inputs.endpoint || ('https://127.0.0.1:' + (inputs.ssh_remote_port || 9443)),
        project_id: inputs.project_id,
        invitation_id: inputs.invitation_id,
        code: inputs.code,
        server_ca: probe.server_ca || '',
        ssh_target: inputs.ssh_target,
        ssh_port: inputs.ssh_port,
        ssh_remote_port: inputs.ssh_remote_port,
        ssh_identity_file: inputs.ssh_identity_file,
        trust_host_key: false
      };
      const accepted = await request('/api/local/accept-invitation', { method: 'POST', body: reqBody });
      if (seq !== sshSelectionSeq || !$('#join-remote-dialog').open) return;
      if (accepted.state === 'pending') {
        renderVerificationCard(
          sshVerifyBox,
          accepted,
          'Waiting for the project owner to accept on the host (auto-checking every 2 seconds)...'
        );
        if (sshStatus) sshStatus.textContent = '';
        if (sshConnectBtn) sshConnectBtn.textContent = 'Check approval now';
        startJoinPoll(seq, async () => {
          const next = await request('/api/local/accept-invitation', { method: 'POST', body: reqBody });
          if (seq !== sshSelectionSeq || !$('#join-remote-dialog').open) return;
          if (next.state !== 'pending') {
            await finishJoinedProject(next, next.home_id || '');
          }
        }, currentSeq => currentSeq === sshSelectionSeq);
        return;
      }
      await finishJoinedProject(accepted, accepted.home_id || '');
    } catch (error) {
      if (error.data?.status === 'changed') renderSSHProbeResult(error.data);
      if (seq === sshSelectionSeq && sshStatus) sshStatus.textContent = error.message;
    } finally {
      if (sshConnectBtn) sshConnectBtn.disabled = false;
    }
  });

  $('#join-remote-dialog')?.addEventListener('close', () => {
    inviteSelectionSeq++;
    sshSelectionSeq++;
    stopJoinPoll();
    invitationBundle = null;
    encryptedInviteText = '';
    sshProbeState = null;
    invitationForm?.reset();
    sshForm?.reset();
    $('#join-remote-form')?.reset();
    if ($('#accept-manual-details')) $('#accept-manual-details').open = false;
    if ($('#join-credential-details')) $('#join-credential-details').open = false;
    setJoinMethod('invitation');
    joinConnectStatus.textContent = '';
    $('#join-credential-status').textContent = 'Use a credential issued for this device, or enter its details below.';
    invitationPreview.textContent = '';
    invitationStatus.textContent = '';
    if (invitationVerifyBox) {
      invitationVerifyBox.hidden = true;
      clearNode(invitationVerifyBox);
    }
    if (sshProbeBox) {
      sshProbeBox.hidden = true;
      clearNode(sshProbeBox);
    }
    if (sshVerifyBox) {
      sshVerifyBox.hidden = true;
      clearNode(sshVerifyBox);
    }
    if (sshStatus) sshStatus.textContent = '';
    if (invitationButton) invitationButton.textContent = 'Request access';
    if (sshConnectBtn) sshConnectBtn.textContent = 'Trust host & request access';
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
  $('#share-mode-lan')?.addEventListener('click', () => setShareMode('lan'));
  $('#share-mode-frp')?.addEventListener('click', () => setShareMode('frp'));
  $('#share-mode-ssh')?.addEventListener('click', () => setShareMode('ssh'));
  $('#share-lan-select')?.addEventListener('change', () => {
    const port = Number(shareAccessSnapshot?.listen_port) || 47282;
    if ($('#share-lan-endpoint-preview') && $('#share-lan-select').value) {
      $('#share-lan-endpoint-preview').textContent = 'https://' + $('#share-lan-select').value + ':' + port;
    }
  });
  const openPeerSettings = () => {
    closeDialog('share-project-dialog');
    state.settingsTab = 'connections';
    state.connectionsSection = 'remote-access-card';
    setView('settings');
    if (typeof loadSettingsRouteData === 'function') void loadSettingsRouteData(state.generation);
  };
  $('#share-open-frp-settings')?.addEventListener('click', openPeerSettings);
  $('#share-open-ssh-settings')?.addEventListener('click', openPeerSettings);
  $('#share-gen-passphrase')?.addEventListener('click', () => {
    if ($('#share-passphrase')) {
      $('#share-passphrase').value = generateInvitePIN();
      $('#share-passphrase').focus();
    }
  });
  $('#share-frp-server')?.addEventListener('input', () => updateShareSetupStatus());
  $('#refresh-device-requests')?.addEventListener('click', loadDeviceRequests);
  $('#share-project-dialog')?.addEventListener('close', () => {
    stopShareInviteWatcher();
    clearNode($('#share-project-result'));
  });
  $('#share-project-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const project = state.project;
    const form = event.currentTarget;
    const result = $('#share-project-result');
    const submit = form.querySelector('[type=submit]');
    if (submit.disabled) return;
    const mode = form.dataset.mode || 'lan';
    const passphrase = normalizeInviteSecret(form.elements.passphrase?.value);
    if (passphrase.length < 6) {
      result.textContent = 'PIN must be at least 6 characters/digits.';
      return;
    }
    const body = {
      operation: 'create',
      mode,
      name: (form.elements.invitee?.value || '').trim(),
      role: form.elements.role.value,
      encrypt: true,
      passphrase,
      auto_approve: false
    };
    if (mode === 'lan') {
      const selectedIP = (form.elements.lanIP?.value || '').trim();
      if (selectedIP) body.lan_ip = selectedIP;
    }
    if (mode === 'frp') {
      body.auto_local_relay = true;
      const srv = ($('#share-frp-server')?.value || '').trim();
      if (srv) body.frp_server = srv;
    }
    submit.disabled = true;
    result.textContent = mode === 'frp'
      ? 'Starting isolated FRP tunnel & encrypting invite...'
      : mode === 'ssh'
        ? 'Creating encrypted SSH invite...'
        : 'Starting LAN listener & encrypting invite...';
    try {
      const data = await request(projectApi('remote-invitations'), {
        method: 'POST',
        body
      });
      if (project !== state.project || !$('#share-project-dialog').open) return;

      const updatedAccess = await request('/api/remote-access?project=' + encodeURIComponent(project.project_id)).catch(() => null);
      if (updatedAccess) {
        shareAccessSnapshot = updatedAccess;
        state.peerAccess = updatedAccess;
        state.remoteAccessEnabled = Boolean(updatedAccess.enabled);
        updateShareSetupStatus();
      }

      const fileContent = typeof data.encrypted_file === 'string'
        ? data.encrypted_file
        : JSON.stringify(data.encrypted_file, null, 2);
      const blob = URL.createObjectURL(new Blob([fileContent], { type: 'application/json' }));
      const download = createEl('a', 'btn btn-primary', 'Download .gossip file');
      download.href = blob;
      download.download = data.filename || 'invite.gossip';
      $('#share-project-dialog').addEventListener('close', () => URL.revokeObjectURL(blob), { once: true });

      const usedPass = data.pin || data.passphrase || passphrase;
      const copyBtn = createEl('button', 'btn btn-secondary', 'Copy PIN');
      copyBtn.type = 'button';
      copyBtn.addEventListener('click', async () => {
        try {
          await navigator.clipboard.writeText(usedPass);
          copyBtn.textContent = 'Copied PIN!';
        } catch {
          copyBtn.textContent = 'Copy manually';
        }
      });

      const pinBox = createEl('div', 'verify-code-box');
      const pinRow = createEl('div', 'verify-code-value', usedPass);
      const actionsRow = createEl('div', 'btn-row');
      actionsRow.append(copyBtn, download);
      pinBox.append(
        createEl('strong', '', 'Step 1 · Send ' + (data.filename || 'invite.gossip') + ' + this PIN'),
        pinRow,
        actionsRow
      );

      const summary = createEl(
        'p',
        'muted small',
        (data.mode || mode).toUpperCase() + ' · ' + (data.endpoint || '') +
        (data.host_fingerprint ? ' · Host CA: ' + data.host_fingerprint : '')
      );
      const liveWatch = createEl('div', 'stack-list', 'Step 2 · Waiting for peer to open the file and enter this PIN...');
      result.replaceChildren(pinBox, summary, liveWatch);
      download.click();

      const createdID = data.invitation?.id || '';
      const pollShareWatcher = async () => {
        if (project !== state.project || !$('#share-project-dialog').open) {
          stopShareInviteWatcher();
          return;
        }
        try {
          const list = await request(projectApi('remote-invitations'));
          if (project !== state.project || !$('#share-project-dialog').open) return;
          updateRequestsBadge(list);
          const match = list.find(inv => inv.id === createdID);
          if (!match) return;
          if (match.state === 'pending') {
            liveWatch.replaceChildren(renderInviteApprovalBox(project, match, shareDirectorySnapshot, async () => {
              await loadDeviceRequests();
              await pollShareWatcher();
            }));
          } else if (match.state === 'approved') {
            liveWatch.textContent = 'Accepted — waiting for ' + match.name + ' to finish connecting...';
          } else if (match.state === 'enrolled') {
            liveWatch.textContent = match.name + ' is now connected.';
            stopShareInviteWatcher();
          } else if (match.state === 'denied' || match.state === 'cancelled') {
            liveWatch.textContent = 'Invitation ' + match.state + '.';
            stopShareInviteWatcher();
          }
        } catch {
          // Retry on next interval while Share modal stays open.
        }
      };
      stopShareInviteWatcher();
      shareWatcherTimer = setInterval(pollShareWatcher, 1000);
    } catch (error) {
      if (project === state.project) result.textContent = error.message;
    } finally {
      if (project === state.project) submit.disabled = false;
    }
  });
}
