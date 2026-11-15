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
    renderList(membersWrap, state.agents, '', a => {
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
      return row;
    }, { pageSize: 25 });
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
    renderList(invWrap, state.certInvitations, '', inv => {
      const row = createEl('div', 'list-item');
      row.append(
        createEl('div', 'list-item-title', `Invitation ${inv.id}`),
        createEl('div', 'muted small', `Status: ${inv.status || (inv.revoked ? 'revoked' : 'active')}`)
      );
      return row;
    }, { pageSize: 25 });
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

function lookupProjectDisplayName(projectID) {
  if (!projectID) return 'this project';
  const match = (state.localProjects || []).find(p => p.project_id === projectID)
    || (state.projects || []).find(p => p.project_id === projectID);
  return match?.project_name || projectID;
}

function syncPeerAccessPanel(force = false) {
  const pa = state.peerAccess;
  const form = $('#peer-access-form');
  const activeProjID = state.project?.project_id || '';
  const activeProjName = state.project?.project_name || activeProjID || 'this project';

  if ($('#remote-access-status')) {
    if (!pa) {
      $('#remote-access-status').textContent = state.remoteAccessEnabled
        ? 'Remote TLS access is ENABLED on this Gossip host.'
        : 'Remote LAN/internet access is currently DISABLED (loopback-only local authority mode).';
    } else if (pa.enabled) {
      const parts = [];
      if (pa.lan_enabled) {
        parts.push('LAN (' + activeProjName + ') active on ' + ((pa.lan_endpoints && pa.lan_endpoints.length) ? pa.lan_endpoints.join(', ') : (pa.active_listen_addr || ('port ' + (pa.active_port || pa.listen_port || 47282)))));
      } else if (Array.isArray(pa.lan_listeners) && pa.lan_listeners.some(l => l.enabled)) {
        const count = pa.lan_listeners.filter(l => l.enabled).length;
        parts.push(count + ' other project LAN port(s) active');
      }
      if (pa.frp_enabled) {
        parts.push('Project FRP ' + (pa.frp_state || 'active') + (pa.frp_endpoint ? ' (' + pa.frp_endpoint + ')' : ''));
      } else if (Array.isArray(pa.frp_tunnels) && pa.frp_tunnels.some(t => t.enabled)) {
        const count = pa.frp_tunnels.filter(t => t.enabled).length;
        parts.push(count + ' other project FRP tunnel(s) active');
      }
      /* Commented out: OpenSSH MCP relaying disabled; using SSH-like mTLS TOFU + separate per-project ports instead
      if (pa.ssh_enabled) {
        parts.push('Project SSH ' + (pa.ssh_state || 'active') + (pa.ssh_endpoint ? ' (' + pa.ssh_endpoint + ')' : ''));
      } else if (Array.isArray(pa.ssh_tunnels) && pa.ssh_tunnels.some(t => t.enabled)) {
        const count = pa.ssh_tunnels.filter(t => t.enabled).length;
        parts.push(count + ' other project SSH listener(s) active');
      }
      */
      if (pa.listener_error) parts.push('Listener error: ' + pa.listener_error);
      $('#remote-access-status').textContent = parts.join(' · ') || 'Peer TLS access is ENABLED.';
    } else {
      $('#remote-access-status').textContent = 'Remote LAN/internet access is currently DISABLED (loopback-only local authority mode).';
    }
  }

  const hostFpEl = $('#peer-host-fingerprint');
  if (hostFpEl) {
    if (pa?.host_fingerprint) {
      hostFpEl.hidden = false;
      hostFpEl.textContent = 'Host CA fingerprint (TOFU): ' + pa.host_fingerprint;
    } else {
      hostFpEl.hidden = true;
      hostFpEl.textContent = '';
    }
  }

  const binEl = $('#peer-binary-status');
  if (binEl) {
    if (!pa) {
      binEl.textContent = '';
    } else {
      const frpMsg = pa.frp_binary_available
        ? ('FRP: ' + (pa.frp_binary_name || 'gossip-frp') + ' ready')
        : 'FRP: companion gossip-frp not installed (LAN only)';
      /* Commented out: OpenSSH relay binary status
      const sshMsg = pa.ssh_binary_available
        ? 'OpenSSH: ready'
        : 'OpenSSH: ssh binary not found (direct loopback listener works; relay requires ssh)';
      binEl.textContent = frpMsg + ' · ' + sshMsg;
      */
      binEl.textContent = frpMsg + ' · Auth: SSH-style TOFU host keys + manual verification code';
    }
  }

  if (!form) return;
  form.hidden = !pa;
  if (!pa) return;

  const lanTitleEl = $('#peer-lan-project-title');
  if (lanTitleEl) {
    const strong = createEl('strong', '', 'LAN Peer Access for ' + activeProjName);
    const note = createEl('span', 'muted small', ' (auto-picks free mTLS port & real LAN IP)');
    lanTitleEl.replaceChildren(strong, note);
  }

  const frpTitleEl = $('#peer-frp-project-title');
  if (frpTitleEl) {
    const strong = createEl('strong', '', 'FRP Tunnel for ' + activeProjName);
    const note = createEl('span', 'muted small', ' (auto-assigns free port & CSPRNG token; isolated per project)');
    frpTitleEl.replaceChildren(strong, note);
  }

  /* Commented out: OpenSSH MCP relaying UI
  const sshTitleEl = $('#peer-ssh-project-title');
  if (sshTitleEl) {
    const strong = createEl('strong', '', 'SSH Access / Tunnel for ' + activeProjName);
    const note = createEl('span', 'muted small', ' (isolated per-project loopback listener; peer connects via OpenSSH)');
    sshTitleEl.replaceChildren(strong, note);
  }
  */

  const endpointsEl = $('#peer-lan-endpoints');
  if (endpointsEl) {
    if (pa.lan_enabled && pa.lan_endpoints && pa.lan_endpoints.length) {
      endpointsEl.textContent = pa.lan_endpoints.join(', ');
    } else if (pa.detected_lan_ips && pa.detected_lan_ips.length) {
      const nextPort = pa.listen_port || 47282;
      const prefix = pa.lan_enabled ? 'Listening; detected LAN IP(s): ' : 'Off — ready on port ' + nextPort + ' (';
      endpointsEl.textContent = pa.lan_enabled
        ? (prefix + pa.detected_lan_ips.join(', '))
        : (prefix + pa.detected_lan_ips.join(', ') + ')');
    } else {
      endpointsEl.textContent = pa.lan_enabled ? 'Listening on all interfaces (no non-loopback LAN IPv4 detected)' : 'Off (no non-loopback LAN IPv4 detected)';
    }
  }

  const frpStatusEl = $('#peer-frp-status');
  if (frpStatusEl) {
    const bits = [];
    if (!pa.frp_binary_available) {
      frpStatusEl.textContent = 'Unavailable — running Gossip without companion gossip-frp binary.';
    } else {
      if (pa.frp_local_relay_enabled) {
        bits.push('Local relay ' + (pa.frp_local_relay_state || 'running') + ' (:' + (pa.frp_relay_server_port || 7000) + ')');
      }
      if (pa.frp_enabled) {
        bits.push('Tunnel ' + (pa.frp_state || 'running'));
        if (pa.frp_endpoint) bits.push(pa.frp_endpoint);
        if (pa.frp_local_addr) bits.push('isolated loopback ' + pa.frp_local_addr);
        if (pa.frp_error) bits.push('Error: ' + pa.frp_error);
      } else if (pa.frp_relay_server) {
        bits.push('Off for ' + activeProjName + ' · Relay ready (' + pa.frp_relay_server + '; auto port ' + (pa.frp_remote_port || 18443) + ')');
      } else {
        bits.push('Off for ' + activeProjName + ' · Companion ready (' + (pa.frp_binary_name || 'gossip-frp') + ')');
      }
      if (pa.frp_local_relay_error) {
        bits.push('Relay error: ' + pa.frp_local_relay_error);
      }
      frpStatusEl.textContent = bits.join(' · ');
    }
  }

  if ($('#peer-frp-enabled')) {
    $('#peer-frp-enabled').disabled = !pa.frp_binary_available && !pa.frp_enabled;
  }
  if ($('#peer-frp-local-relay')) {
    $('#peer-frp-local-relay').disabled = !pa.frp_binary_available && !pa.frp_local_relay_enabled;
  }

  const tokenEl = $('#peer-frp-token');
  if (tokenEl) {
    const hasTok = pa.frp_token_configured || pa.frp_relay_token_configured;
    const maskedTok = pa.frp_token_masked || pa.frp_relay_token_masked || 'saved';
    tokenEl.placeholder = hasTok
      ? ('Auto-managed CSPRNG (' + maskedTok + ') — leave blank to keep')
      : 'Leave blank to auto-generate CSPRNG token';
  }

  const dashPort = Number(pa.dashboard_port) || Number(window.location.port) || 47281;
  const dashPreviewEl = $('#peer-dashboard-url-preview');
  if (dashPreviewEl) {
    dashPreviewEl.textContent = 'http://' + (window.location.hostname || '127.0.0.1') + ':' + dashPort;
  }

  if (force || !form.contains(document.activeElement)) {
    if ($('#peer-dashboard-port')) $('#peer-dashboard-port').value = String(dashPort);
    if ($('#peer-lan-enabled')) $('#peer-lan-enabled').checked = Boolean(pa.lan_enabled);
    if ($('#peer-lan-port')) $('#peer-lan-port').value = String(pa.listen_port || 47282);
    if ($('#peer-frp-enabled')) $('#peer-frp-enabled').checked = Boolean(pa.frp_enabled);
    if ($('#peer-frp-local-relay')) $('#peer-frp-local-relay').checked = Boolean(pa.frp_local_relay_enabled);
    if ($('#peer-frp-server')) $('#peer-frp-server').value = pa.frp_server || pa.frp_relay_server || '';
    if ($('#peer-frp-server-port')) $('#peer-frp-server-port').value = String(pa.frp_server_port || pa.frp_relay_server_port || 7000);
    if ($('#peer-frp-remote-port')) $('#peer-frp-remote-port').value = String(pa.frp_remote_port || 18443);
    if (tokenEl) tokenEl.value = '';
    /* Commented out: OpenSSH MCP relaying fields
    if ($('#peer-ssh-enabled')) $('#peer-ssh-enabled').checked = Boolean(pa.ssh_enabled);
    if ($('#peer-ssh-mode')) $('#peer-ssh-mode').value = pa.ssh_mode === 'relay' ? 'relay' : 'direct';
    if ($('#peer-ssh-local-port')) $('#peer-ssh-local-port').value = String(pa.ssh_local_port || 9443);
    if ($('#peer-ssh-target')) $('#peer-ssh-target').value = pa.ssh_target || '';
    if ($('#peer-ssh-port')) $('#peer-ssh-port').value = String(pa.ssh_port || 22);
    if ($('#peer-ssh-remote-port')) $('#peer-ssh-remote-port').value = String(pa.ssh_remote_port || 19443);
    if ($('#peer-ssh-identity')) $('#peer-ssh-identity').value = pa.ssh_identity_file || '';
    */
  }

  /* Commented out: OpenSSH MCP relaying status
  const sshRelayMode = ($('#peer-ssh-mode')?.value || pa.ssh_mode) === 'relay';
  if ($('#peer-ssh-remote-port-wrap')) $('#peer-ssh-remote-port-wrap').hidden = !sshRelayMode;
  if ($('#peer-ssh-identity-wrap')) $('#peer-ssh-identity-wrap').hidden = !sshRelayMode;

  const sshStatusEl = $('#peer-ssh-status');
  if (sshStatusEl) {
    if (pa.ssh_enabled) {
      const bits = ['SSH (' + (pa.ssh_mode || 'direct') + ') ' + (pa.ssh_state || 'running')];
      if (pa.ssh_endpoint) bits.push('endpoint ' + pa.ssh_endpoint);
      if (pa.ssh_local_addr) bits.push('isolated loopback ' + pa.ssh_local_addr);
      if (pa.ssh_target) bits.push('target ' + pa.ssh_target);
      if (pa.ssh_error) bits.push('Error: ' + pa.ssh_error);
      sshStatusEl.textContent = bits.join(' · ');
    } else {
      sshStatusEl.textContent = 'Disabled for ' + activeProjName + ' · Isolated loopback listener only (peers tunnel via OpenSSH).';
    }
  }
  */

  const lanWrap = $('#peer-lan-tunnels-wrap');
  const lanList = $('#peer-lan-tunnels-list');
  const lanListeners = Array.isArray(pa.lan_listeners) ? pa.lan_listeners : [];
  if (lanWrap && lanList) {
    lanWrap.hidden = lanListeners.length === 0;
    lanList.replaceChildren(...lanListeners.map(l => {
      const row = createEl('div', 'peer-tunnel-row');
      const info = createEl('div', '');
      const label = lookupProjectDisplayName(l.project_id) + (l.project_id === activeProjID ? ' (current)' : '');
      const epText = (l.endpoints && l.endpoints.length) ? l.endpoints.join(', ') : (l.listen_addr || ('port ' + (l.listen_port || '')));
      const statusText = l.enabled
        ? (epText + ' · ' + (l.state || 'listening') + (l.error ? ' (' + l.error + ')' : ''))
        : ((l.listen_port ? ('Port ' + l.listen_port) : 'Not set') + ' · Disabled');
      info.append(
        createEl('div', 'list-item-title', label),
        createEl('div', 'muted small', statusText)
      );
      row.appendChild(info);
      if (l.enabled) {
        const offBtn = createEl('button', 'btn btn-secondary btn-xs', 'Turn off');
        offBtn.type = 'button';
        offBtn.dataset.disableLanProject = l.project_id;
        row.appendChild(offBtn);
      }
      return row;
    }));
  }

  const tunnelsWrap = $('#peer-frp-tunnels-wrap');
  const tunnelsList = $('#peer-frp-tunnels-list');
  const tunnels = Array.isArray(pa.frp_tunnels) ? pa.frp_tunnels : [];
  if (tunnelsWrap && tunnelsList) {
    tunnelsWrap.hidden = tunnels.length === 0;
    tunnelsList.replaceChildren(...tunnels.map(t => {
      const row = createEl('div', 'peer-tunnel-row');
      const info = createEl('div', '');
      const label = lookupProjectDisplayName(t.project_id) + (t.project_id === activeProjID ? ' (current)' : '');
      const statusText = t.enabled
        ? ((t.endpoint || t.server || '') + ' · ' + (t.state || 'running') + (t.error ? ' (' + t.error + ')' : ''))
        : ((t.endpoint || t.server || 'Not set') + ' · Disabled');
      info.append(
        createEl('div', 'list-item-title', label),
        createEl('div', 'muted small', statusText)
      );
      row.appendChild(info);
      if (t.enabled) {
        const offBtn = createEl('button', 'btn btn-secondary btn-xs', 'Turn off');
        offBtn.type = 'button';
        offBtn.dataset.disableTunnelProject = t.project_id;
        row.appendChild(offBtn);
      }
      return row;
    }));
  }

  /* Commented out: OpenSSH MCP relaying tunnel list
  const sshWrap = $('#peer-ssh-tunnels-wrap');
  const sshList = $('#peer-ssh-tunnels-list');
  const sshTunnels = Array.isArray(pa.ssh_tunnels) ? pa.ssh_tunnels : [];
  if (sshWrap && sshList) {
    sshWrap.hidden = sshTunnels.length === 0;
    sshList.replaceChildren(...sshTunnels.map(t => {
      const row = createEl('div', 'peer-tunnel-row');
      const info = createEl('div', '');
      const label = lookupProjectDisplayName(t.project_id) + (t.project_id === activeProjID ? ' (current)' : '');
      const statusText = t.enabled
        ? ((t.mode || 'direct') + ' · ' + (t.endpoint || t.local_addr || '') + ' · ' + (t.state || 'running') + (t.error ? ' (' + t.error + ')' : ''))
        : ((t.mode || 'direct') + ' · Disabled');
      info.append(
        createEl('div', 'list-item-title', label),
        createEl('div', 'muted small', statusText)
      );
      row.appendChild(info);
      if (t.enabled) {
        const offBtn = createEl('button', 'btn btn-secondary btn-xs', 'Turn off');
        offBtn.type = 'button';
        offBtn.dataset.disableSshProject = t.project_id;
        row.appendChild(offBtn);
      }
      return row;
    }));
  }
  */

  if ($('#peer-access-disable-all')) {
    $('#peer-access-disable-all').disabled = !pa.enabled;
  }
}

function renderSettingsOtherPanels() {
  syncPeerAccessPanel();

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

  renderList($('#known-hosts-list'), state.knownHosts || [], 'No trusted peer host keys pinned yet.', kh => {
    const row = createEl('div', 'peer-tunnel-row');
    const info = createEl('div', '');
    info.append(
      createEl('div', 'list-item-title', kh.host_key || ''),
      createEl('div', 'muted small', kh.fingerprint || '')
    );
    const removeBtn = createEl('button', 'btn btn-secondary btn-xs', 'Forget');
    removeBtn.type = 'button';
    removeBtn.dataset.forgetKnownHost = kh.host_key || '';
    row.append(info, removeBtn);
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
    renderList(recWrap, state.legacyRecords, '', rec => {
      const row = createEl('div', 'list-item');
      row.appendChild(createEl('div', 'list-item-title', `${rec.kind || 'record'} · ${rec.id}`));
      return row;
    }, {
      pageSize: 25,
      hasMore: () => state.legacyRecordsHasMore,
      loadMore: loadMoreLegacyRecords
    });
  }

  const eventsList = $('#history-events-list');
  if (eventsList) {
    renderList(eventsList, state.events || [], 'No recent audit events recorded.', renderAuditEventRow, {
      pageSize: 25,
      hasMore: () => state.eventsHasMore,
      loadMore: loadMoreProjectEvents
    });
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
  for (const [selector, stateKey, datasetKey] of [
    ['#access-section-select', 'accessSection', 'accessSection'],
    ['#connections-section-select', 'connectionsSection', 'connectionsSection'],
    ['#workspaces-section-select', 'workspacesSection', 'workspacesSection'],
    ['#history-section-select', 'historySection', 'historySection']
  ]) {
    const buttons = $$(`[data-${datasetKey.replace(/[A-Z]/g, m => '-' + m.toLowerCase())}]`);
    syncSettingsSectionNav(buttons, $(selector), stateKey, datasetKey);
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
  if ($('#access-project-scope')) $('#access-project-scope').hidden = state.settingsTab !== 'access';

  renderSettingsAccessPanels(canManageMembers(), canEditSettings());
  renderSettingsOtherPanels();
}

function syncSettingsSectionNav(buttons, selectEl, stateKey, datasetKey) {
  if (!buttons.length) return;
  const visible = buttons.filter(btn => !btn.hidden);
  if (!visible.some(btn => btn.dataset[datasetKey] === state[stateKey]) && visible[0]) {
    state[stateKey] = visible[0].dataset[datasetKey];
  }
  for (const button of buttons) {
    const sectionId = button.dataset[datasetKey];
    const active = sectionId === state[stateKey];
    const panel = document.getElementById(sectionId);
    if (panel) panel.hidden = !active;
    button.setAttribute('aria-pressed', String(active));
  }
  if (selectEl) {
    selectEl.replaceChildren(...visible.map(button =>
      new Option(button.textContent.trim(), button.dataset[datasetKey])));
    selectEl.value = state[stateKey];
    if (typeof syncCustomSelect === 'function') syncCustomSelect(selectEl);
  }
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

function bindSettingsSectionNav({ attr, stateKey, datasetKey, selectSelector }) {
  $$(`[${attr}]`).forEach(button => {
    button.addEventListener('click', () => {
      state[stateKey] = button.dataset[datasetKey];
      syncUrlState();
      renderSettingsView();
      void loadSettingsRouteData(state.generation);
      const section = document.getElementById(state[stateKey]);
      section?.focus({ preventScroll: true });
      if (section && section.getBoundingClientRect().top < $('.topbar').getBoundingClientRect().bottom) {
        $('#settings-tabs')?.scrollIntoView({ block: 'start' });
      }
    });
  });

  $(selectSelector)?.addEventListener('change', event => {
    state[stateKey] = event.target.value;
    syncUrlState();
    renderSettingsView();
    void loadSettingsRouteData(state.generation);
  });
}

function wireSettingsEvents() {
  bindSettingsSectionNav({ attr: 'data-access-section', stateKey: 'accessSection', datasetKey: 'accessSection', selectSelector: '#access-section-select' });
  bindSettingsSectionNav({ attr: 'data-connections-section', stateKey: 'connectionsSection', datasetKey: 'connectionsSection', selectSelector: '#connections-section-select' });
  bindSettingsSectionNav({ attr: 'data-workspaces-section', stateKey: 'workspacesSection', datasetKey: 'workspacesSection', selectSelector: '#workspaces-section-select' });
  bindSettingsSectionNav({ attr: 'data-history-section', stateKey: 'historySection', datasetKey: 'historySection', selectSelector: '#history-section-select' });

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

  const peerForm = $('#peer-access-form');
  if (peerForm) {
    const feedbackEl = $('#peer-access-feedback');
    const saveBtn = $('#peer-access-save');
    const disableAllBtn = $('#peer-access-disable-all');

    const applyPeerUpdate = async (body, successText) => {
      if (feedbackEl) feedbackEl.textContent = 'Updating peer access...';
      if (saveBtn) saveBtn.disabled = true;
      if (disableAllBtn) disableAllBtn.disabled = true;
      try {
        const updated = await request('/api/local/peer-access', { method: 'POST', body });
        state.peerAccess = updated;
        state.remoteAccessEnabled = Boolean(updated?.enabled);
        syncPeerAccessPanel(true);
        if (updated?.dashboard_redirect_url && updated.dashboard_redirect_url !== window.location.origin) {
          if (feedbackEl) feedbackEl.textContent = 'Dashboard port updated — opening ' + updated.dashboard_redirect_url + '...';
          render();
          setTimeout(() => { window.location.href = updated.dashboard_redirect_url; }, 60);
          return;
        }
        if (feedbackEl) feedbackEl.textContent = successText || 'Peer access settings applied.';
        render();
      } catch (error) {
        syncPeerAccessPanel(true);
        if (feedbackEl) feedbackEl.textContent = error.message;
      } finally {
        if (saveBtn) saveBtn.disabled = false;
        if (disableAllBtn) disableAllBtn.disabled = !state.peerAccess?.enabled;
      }
    };

    const readPeerBody = (opts = {}) => {
      const dashPortVal = Number($('#peer-dashboard-port')?.value || 0);
      const body = {
        project_id: state.project?.project_id || '',
        auto_setup: opts.auto_setup !== false,
        lan_enabled: Boolean($('#peer-lan-enabled')?.checked),
        listen_port: Number($('#peer-lan-port')?.value || 47282),
        frp_local_relay: Boolean($('#peer-frp-local-relay')?.checked),
        frp_enabled: Boolean($('#peer-frp-enabled')?.checked),
        frp_server: ($('#peer-frp-server')?.value || '').trim(),
        frp_server_port: Number($('#peer-frp-server-port')?.value || 7000),
        frp_remote_port: Number($('#peer-frp-remote-port')?.value || 18443)
        /* Commented out: OpenSSH MCP relaying fields
        ssh_enabled: Boolean($('#peer-ssh-enabled')?.checked),
        ssh_mode: ($('#peer-ssh-mode')?.value || 'direct'),
        ssh_local_port: Number($('#peer-ssh-local-port')?.value || 9443),
        ssh_target: ($('#peer-ssh-target')?.value || '').trim(),
        ssh_port: Number($('#peer-ssh-port')?.value || 22),
        ssh_remote_port: Number($('#peer-ssh-remote-port')?.value || 19443),
        ssh_identity_file: ($('#peer-ssh-identity')?.value || '').trim()
        */
      };
      if (dashPortVal > 0) body.dashboard_port = dashPortVal;
      const tok = ($('#peer-frp-token')?.value || '').trim();
      if (tok) body.frp_token = tok;
      if (opts.frp_auto_generate) body.frp_auto_generate = true;
      return body;
    };

    $('#peer-dashboard-port')?.addEventListener('input', () => {
      const p = Number($('#peer-dashboard-port')?.value || 0);
      const previewEl = $('#peer-dashboard-url-preview');
      if (previewEl) {
        previewEl.textContent = p >= 1 && p <= 65535
          ? ('http://' + (window.location.hostname || '127.0.0.1') + ':' + p)
          : 'Enter port 1–65535';
      }
    });

    /* Commented out: OpenSSH MCP relaying UI handlers
    $('#peer-ssh-mode')?.addEventListener('change', () => {
      const relay = $('#peer-ssh-mode')?.value === 'relay';
      if ($('#peer-ssh-remote-port-wrap')) $('#peer-ssh-remote-port-wrap').hidden = !relay;
      if ($('#peer-ssh-identity-wrap')) $('#peer-ssh-identity-wrap').hidden = !relay;
    });
    */

    peerForm.addEventListener('submit', async event => {
      event.preventDefault();
      await applyPeerUpdate(readPeerBody(), 'Peer access settings updated.');
    });

    $('#peer-lan-enabled')?.addEventListener('change', async () => {
      const enabling = Boolean($('#peer-lan-enabled')?.checked);
      await applyPeerUpdate(readPeerBody(), enabling ? 'LAN peer access enabled.' : 'LAN peer access disabled.');
    });

    $('#peer-frp-enabled')?.addEventListener('change', async () => {
      const enabling = Boolean($('#peer-frp-enabled')?.checked);
      if (!enabling) {
        await applyPeerUpdate(readPeerBody({ auto_setup: false }), 'Project FRP tunnel disabled.');
        return;
      }
      const hasServer = Boolean(($('#peer-frp-server')?.value || '').trim() || state.peerAccess?.frp_relay_server);
      const localRelay = Boolean($('#peer-frp-local-relay')?.checked || state.peerAccess?.frp_local_relay_enabled);
      if (hasServer || localRelay) {
        await applyPeerUpdate(readPeerBody({ auto_setup: true }), 'Project FRP tunnel started (port & CSPRNG token auto-managed).');
      } else {
        $('#peer-frp-server')?.focus();
        if (feedbackEl) feedbackEl.textContent = 'Enter your FRP Relay Host once, or turn on "Run built-in local FRP relay" / "Auto-configure & Start".';
      }
    });

    $('#peer-frp-local-relay')?.addEventListener('change', async () => {
      const enabling = Boolean($('#peer-frp-local-relay')?.checked);
      await applyPeerUpdate(
        readPeerBody({ auto_setup: true }),
        enabling
          ? 'Built-in local FRP relay started with auto-generated CSPRNG token.'
          : 'Built-in local FRP relay stopped.'
      );
    });

    $('#peer-frp-auto-btn')?.addEventListener('click', async () => {
      const serverInput = ($('#peer-frp-server')?.value || '').trim();
      const hasServer = Boolean(serverInput || state.peerAccess?.frp_relay_server);
      const body = readPeerBody({ auto_setup: true });
      body.frp_enabled = true;
      if (!hasServer && !body.frp_local_relay) {
        body.frp_local_relay = true;
      }
      await applyPeerUpdate(body, 'FRP auto-configured with CSPRNG token and dedicated project port.');
    });

    $('#peer-frp-regen-token')?.addEventListener('click', async () => {
      await applyPeerUpdate(
        readPeerBody({ auto_setup: true, frp_auto_generate: true }),
        'Generated new CSPRNG FRP token and applied it.'
      );
    });

    $('#peer-frp-copy-cmd')?.addEventListener('click', async () => {
      if (feedbackEl) feedbackEl.textContent = 'Preparing VPS relay command with CSPRNG token...';
      try {
        const res = await request('/api/local/frp-relay-command', { method: 'POST', body: {} });
        let copied = false;
        if (navigator.clipboard && navigator.clipboard.writeText) {
          try {
            await navigator.clipboard.writeText(res.command || '');
            copied = true;
          } catch {
            copied = false;
          }
        }
        const refreshed = await request('/api/local/peer-access?project=' + encodeURIComponent(state.project?.project_id || '')).catch(() => null);
        if (refreshed) {
          state.peerAccess = refreshed;
          syncPeerAccessPanel(true);
        }
        if (feedbackEl) {
          feedbackEl.textContent = copied
            ? ('Copied `gossip-frp server` command (port ' + (res.server_port || 7000) + ') with your saved CSPRNG token to clipboard — run it on your VPS, then enter your VPS IP above.')
            : ('Generated CSPRNG token for port ' + (res.server_port || 7000) + ', but clipboard access was blocked here. Check browser clipboard permissions to copy the command.');
        }
      } catch (error) {
        if (feedbackEl) feedbackEl.textContent = error.message;
      }
    });

    /* Commented out: OpenSSH MCP relaying toggle handler
    $('#peer-ssh-enabled')?.addEventListener('change', async () => {
      const enabling = Boolean($('#peer-ssh-enabled')?.checked);
      if (!enabling) {
        await applyPeerUpdate(readPeerBody(), 'Project SSH listener disabled.');
        return;
      }
      const mode = $('#peer-ssh-mode')?.value || 'direct';
      const target = ($('#peer-ssh-target')?.value || '').trim();
      if (mode === 'relay' && !target) {
        if (feedbackEl) feedbackEl.textContent = 'Enter SSH Host / Login (user@host) for reverse relay, then click Apply peer settings.';
        return;
      }
      await applyPeerUpdate(readPeerBody(), 'Project SSH access enabled.');
    });
    */

    $('#peer-lan-tunnels-list')?.addEventListener('click', async event => {
      const btn = event.target.closest('[data-disable-lan-project]');
      if (!btn) return;
      const targetProj = btn.dataset.disableLanProject;
      if (!targetProj) return;
      const activeProj = state.project?.project_id || '';
      if (!activeProj || targetProj === activeProj) {
        await applyPeerUpdate(
          { project_id: targetProj, lan_enabled: false },
          'Project LAN listener disabled.'
        );
        return;
      }
      if (feedbackEl) feedbackEl.textContent = 'Disabling LAN listener...';
      try {
        await request('/api/local/peer-access', {
          method: 'POST',
          body: { project_id: targetProj, lan_enabled: false }
        });
        const refreshed = await request('/api/local/peer-access?project=' + encodeURIComponent(activeProj));
        state.peerAccess = refreshed;
        state.remoteAccessEnabled = Boolean(refreshed?.enabled);
        syncPeerAccessPanel(true);
        if (feedbackEl) feedbackEl.textContent = 'Disabled LAN listener for ' + lookupProjectDisplayName(targetProj) + '.';
        render();
      } catch (error) {
        if (feedbackEl) feedbackEl.textContent = error.message;
      }
    });

    $('#peer-frp-tunnels-list')?.addEventListener('click', async event => {
      const btn = event.target.closest('[data-disable-tunnel-project]');
      if (!btn) return;
      const targetProj = btn.dataset.disableTunnelProject;
      if (!targetProj) return;
      const activeProj = state.project?.project_id || '';
      if (!activeProj || targetProj === activeProj) {
        await applyPeerUpdate(
          { project_id: targetProj, frp_enabled: false },
          'Project FRP tunnel disabled.'
        );
        return;
      }
      if (feedbackEl) feedbackEl.textContent = 'Disabling FRP tunnel...';
      try {
        await request('/api/local/peer-access', {
          method: 'POST',
          body: { project_id: targetProj, frp_enabled: false }
        });
        const refreshed = await request('/api/local/peer-access?project=' + encodeURIComponent(activeProj));
        state.peerAccess = refreshed;
        state.remoteAccessEnabled = Boolean(refreshed?.enabled);
        syncPeerAccessPanel(true);
        if (feedbackEl) feedbackEl.textContent = 'Disabled FRP tunnel for ' + lookupProjectDisplayName(targetProj) + '.';
        render();
      } catch (error) {
        if (feedbackEl) feedbackEl.textContent = error.message;
      }
    });

    /* Commented out: OpenSSH MCP relaying list handler
    $('#peer-ssh-tunnels-list')?.addEventListener('click', async event => {
      const btn = event.target.closest('[data-disable-ssh-project]');
      if (!btn) return;
      const targetProj = btn.dataset.disableSshProject;
      if (!targetProj) return;
      const activeProj = state.project?.project_id || '';
      if (!activeProj || targetProj === activeProj) {
        await applyPeerUpdate(
          { project_id: targetProj, ssh_enabled: false },
          'Project SSH listener disabled.'
        );
        return;
      }
      if (feedbackEl) feedbackEl.textContent = 'Disabling SSH listener...';
      try {
        await request('/api/local/peer-access', {
          method: 'POST',
          body: { project_id: targetProj, ssh_enabled: false }
        });
        const refreshed = await request('/api/local/peer-access?project=' + encodeURIComponent(activeProj));
        state.peerAccess = refreshed;
        state.remoteAccessEnabled = Boolean(refreshed?.enabled);
        syncPeerAccessPanel(true);
        if (feedbackEl) feedbackEl.textContent = 'Disabled SSH listener for ' + lookupProjectDisplayName(targetProj) + '.';
        render();
      } catch (error) {
        if (feedbackEl) feedbackEl.textContent = error.message;
      }
    });
    */

    $('#known-hosts-list')?.addEventListener('click', async event => {
      const btn = event.target.closest('[data-forget-known-host]');
      if (!btn) return;
      const hostKey = btn.dataset.forgetKnownHost;
      if (!hostKey) return;
      try {
        await request('/api/local/known-hosts?host_key=' + encodeURIComponent(hostKey), { method: 'DELETE' });
        state.knownHosts = (state.knownHosts || []).filter(kh => kh.host_key !== hostKey);
        renderSettingsOtherPanels();
      } catch (error) {
        if (feedbackEl) feedbackEl.textContent = error.message;
      }
    });

    disableAllBtn?.addEventListener('click', async () => {
      await applyPeerUpdate(
        { disable_all: true, project_id: state.project?.project_id || '' },
        'All LAN and FRP peer access disabled.'
      );
    });
  }
}
