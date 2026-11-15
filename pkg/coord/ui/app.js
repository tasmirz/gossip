'use strict';

if (location?.protocol === 'file:') {
  document.addEventListener('DOMContentLoaded', () => {
    const warn = document.querySelector('#file-warning');
    if (warn) warn.hidden = false;
  });
}

const $ = (sel, root = document) => root?.querySelector?.(sel) || null;
const $$ = (sel, root = document) => Array.from(root?.querySelectorAll?.(sel) || []);
const createEl = (tag, cls, text) => {
  const node = typeof document.createElement === 'function' ? document.createElement(tag) : document.querySelector(tag);
  if (cls && 'className' in node) node.className = cls;
  if (text !== undefined && 'textContent' in node) node.textContent = text;
  return node;
};

function clearNode(node) {
  if (!node) return;
  if (typeof node.replaceChildren === 'function') node.replaceChildren();
  else node.innerHTML = '';
}

function svgIcon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  const use = document.createElementNS(ns, 'use');
  svg.setAttribute('class', 'icon');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  use.setAttribute('href', '#icon-' + name);
  svg.appendChild(use);
  return svg;
}

const THEME_STORAGE_KEY = 'gossip.theme';

function resolveInitialTheme() {
  try {
    const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(THEME_STORAGE_KEY) : null;
    if (saved === 'dark' || saved === 'light') return saved;
  } catch {}
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function applyTheme(theme, persist = false) {
  const mode = theme === 'dark' ? 'dark' : 'light';
  if (document?.documentElement?.dataset) document.documentElement.dataset.theme = mode;
  if (persist) {
    try {
      if (typeof localStorage !== 'undefined') localStorage.setItem(THEME_STORAGE_KEY, mode);
    } catch {}
  }
  const nextLabel = mode === 'dark' ? 'Switch to light mode' : 'Switch to dark mode';
  const btn = $('#theme-toggle-btn');
  if (btn) {
    btn.setAttribute('aria-label', nextLabel);
    btn.setAttribute('title', nextLabel);
  }
  const label = $('#theme-toggle-label');
  if (label) label.textContent = nextLabel;
  const icon = $('#theme-toggle-icon');
  if (icon) icon.setAttribute('href', mode === 'dark' ? '#icon-sun' : '#icon-moon');
}

applyTheme(resolveInitialTheme(), false);

/* REUSABLE UI PRIMITIVES */
function uiBadge(text, variant = '', extraClass = '') {
  const classes = ['badge'];
  if (variant) classes.push(`badge-${variant}`);
  if (extraClass) classes.push(extraClass);
  return createEl('span', classes.join(' '), text);
}

function uiButton({
  label = '',
  shortLabel = label,
  icon = '',
  cls = 'btn btn-secondary',
  iconOnly = false,
  disabled = false,
  type = 'button',
  title = label,
  ariaLabel = label,
  onClick = null
} = {}) {
  const btn = createEl('button', iconOnly ? `${cls} icon-btn` : cls);
  btn.type = type;
  btn.disabled = Boolean(disabled);
  if (ariaLabel) btn.setAttribute('aria-label', ariaLabel);
  if (title) btn.title = title;
  if (icon) {
    btn.appendChild(svgIcon(icon));
    if (!iconOnly && shortLabel !== '') btn.appendChild(createEl('span', '', shortLabel));
  } else {
    btn.textContent = shortLabel;
  }
  if (onClick) btn.addEventListener('click', onClick);
  return btn;
}

function renderList(container, items, emptyMessage, renderItem, emptyClass = 'muted small') {
  if (!container) return;
  clearNode(container);
  if (!items || !items.length) {
    if (emptyMessage) container.appendChild(createEl('p', emptyClass, emptyMessage));
    return;
  }
  for (const item of items) {
    const node = renderItem(item);
    if (node) container.appendChild(node);
  }
}

function populateSelect(selectEl, items, {
  placeholder = '',
  getValue = x => x.id,
  getLabel = x => x.name || x.id,
  selectedValue = undefined
} = {}) {
  if (!selectEl) return;
  clearNode(selectEl);
  if (placeholder !== '' && placeholder !== null && placeholder !== undefined) {
    const opt = createEl('option', '', placeholder);
    opt.value = '';
    selectEl.appendChild(opt);
  }
  for (const item of items || []) {
    const opt = createEl('option', '', getLabel(item));
    opt.value = String(getValue(item));
    if (selectedValue !== undefined && opt.value === String(selectedValue)) opt.selected = true;
    selectEl.appendChild(opt);
  }
  if (selectedValue !== undefined) selectEl.value = String(selectedValue);
}

async function runGuardedAction(control, fn, { onError = err => showError(err.message), onFinally = null } = {}) {
  if (control?.disabled) return;
  if (control) control.disabled = true;
  try {
    return await fn();
  } catch (err) {
    if (onError) onError(err);
  } finally {
    if (control) control.disabled = false;
    if (onFinally) onFinally();
  }
}

function bindProjectForm(selector, { action, getBody, onSuccess = null, refreshAfter = true } = {}) {
  const form = $(selector);
  if (!form) return;
  form.addEventListener('submit', async e => {
    e.preventDefault();
    const submit = form.querySelector('[type=submit]');
    await runGuardedAction(submit, async () => {
      const res = await request(projectApi(action), { method: 'POST', body: getBody(form, e) });
      if (onSuccess) await onSuccess(res, form);
      if (refreshAfter) await refresh();
    });
  });
}

/* ROUTING & APPLICATION STATE */
const ACCESS_SECTIONS = [
  'access-members', 'project-roles-card', 'access-requests-card', 'access-devices',
  'access-permissions', 'access-limits', 'recovery-card'
];

const ROUTES = ['projects', 'chat', 'plans', 'todos', 'agents', 'search', 'settings'];
const ROUTE_TITLES = {
  projects: 'Projects',
  chat: 'Chat',
  plans: 'Plans',
  todos: 'To-dos',
  agents: 'Agents',
  search: 'Search',
  settings: 'Settings'
};

const state = {
  projects: [],
  projectOverview: [],
  localProjects: [],
  remoteProjects: [],
  dashboardAccess: { local_access: false, home_id: '', profile: '' },
  project: null,
  projectReadDenied: false,
  view: 'projects',
  previousView: 'chat',
  settingsTab: 'access',
  mobilePane: 'thread',
  recipient: '',
  channelId: '',
  channels: [],
  plan: null,
  planHistory: [],
  planThreads: [],
  selectedPlanID: '',
  planPane: 'list',
  contextRecords: [],
  contextError: '',
  selectedPlanRev: 0,
  comparePlanRev: 0,
  tasks: [],
  taskCounts: {},
  taskTotal: 0,
  taskCursor: '',
  taskFilters: { status: '', kind: '', assignee: '', query: '' },
  todoLayout: 'checklist',
  todoScope: 'project',
  todoGoalTab: 'all',
  todoAgent: '',
  projectsTab: 'local',
  agentsTab: 'all',
  agentDMs: [],
  agents: [],
  collapsedAgentIDs: new Set(),
  presence: [],
  permissions: [],
  roles: [],
  agentTasks: [],
  agentTasksAgentID: '',
  agentTasksLoading: false,
  agentTasksError: '',
  rolePresets: [],
  repositories: [],
  workspaces: [],
  fileReservations: [],
  certificates: [],
  certInvitations: [],
  replicas: [],
  remoteAccessEnabled: false,
  integrations: [],
  legacyImports: [],
  legacyRecords: [],
  events: [],
  retention: null,
  messages: [],
  messagesHasOlder: false,
  messagesOldestSeq: 0,
  messageDrafts: {},
  chatScroll: new Map(),
  chatLoaded: false,
  chatFetchedSequence: 0,
  chatUnseen: 0,
  chatActivity: new Map(),
  chatSeen: new Map(),
  settings: null,
  budget: null,
  checklists: { available: false, reason: '', items: [] },
  searchQuery: '',
  chatSearch: '',
  accessSection: 'access-members',
  todoRevision: 0,
  searchTab: 'results',
  agentQuery: '',
  projectQuery: '',
  searchPending: false,
  searchResults: null,
  graphify: null,
  generation: 0,
  loading: {},
  errorText: '',
  planConflict: '',
  pollTimer: null
};

let chatSearchTimer;
let chatSearchRequest = 0;
let liveTaskSearchRequest = 0;
let liveGlobalSearchRequest = 0;

function showError(msg) {
  state.errorText = msg || '';
  const box = $('#error');
  const text = $('#error-text');
  if (text) text.textContent = state.errorText;
  if (box) box.hidden = !state.errorText;
}

function expireProjectSession(projectId) {
  $('#project-roles-content')?.replaceChildren();
  if ($('#load-project-roles')) {
    const label = $('#load-project-roles span');
    if (label) label.textContent = 'Refresh';
    else $('#load-project-roles').textContent = 'Refresh';
    $('#load-project-roles').disabled = false;
  }
  Object.assign(state,{projects:[],projectOverview:[],localProjects:[],remoteProjects:[],project:null,projectReadDenied:false,channels:[],channelId:'',plan:null,planHistory:[],tasks:[],agents:[],agentDMs:[],presence:[],messages:[],events:[],repositories:[],workspaces:[],integrations:[],permissions:[],roles:[],rolePresets:[],certificates:[],certInvitations:[],replicas:[],fileReservations:[],legacyImports:[],legacyRecords:[],settings:null,budget:null,checklists:{available:false,items:[]},searchResults:null,graphify:null});
  render();
}

async function request(path, options = {}) {
  const init = {
    method: options.method || 'GET',
    headers: { ...(options.headers || {}) },
    credentials: 'same-origin'
  };
  if (options.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = typeof options.body === 'string' ? options.body : JSON.stringify(options.body);
  }
  const project = state.project;
  const response = await fetch(path, init);
  if (!response.ok) {
    const projectPath = String(path || '').match(/\/api\/projects\/([^/?]+)/);
    if (response.status === 401 && projectPath && state.project === project) {
      expireProjectSession(decodeURIComponent(projectPath[1]));
    }
    let message = '';
    try {
      const text = await response.text();
      if (text) {
        try {
          const parsed = JSON.parse(text);
          message = parsed.error || parsed.message || text;
        } catch {
          message = text;
        }
      }
    } catch {}
    const err = new Error(message || `HTTP ${response.status}`);
    err.status = response.status;
    throw err;
  }
  if (response.status === 204) return null;
  return response.json();
}

function projectApi(action, extraQuery = {}) {
  if (!state.project) throw new Error('No active project selected');
  const params = new URLSearchParams();
  if (state.project.home_id && state.dashboardAccess.home_id && state.project.home_id !== state.dashboardAccess.home_id) {
    params.set('home', state.project.home_id);
  }
  for (const [k, v] of Object.entries(extraQuery || {})) {
    if (v !== undefined && v !== null && String(v) !== '') {
      params.set(k, String(v));
    }
  }
  const qs = params.toString();
  return `/api/projects/${encodeURIComponent(state.project.project_id)}/${action}${qs ? `?${qs}` : ''}`;
}

function hasProjectPermission(perm) {
  const p = state.project;
  if (!p || state.projectReadDenied) return false;
  if (p.role === 'owner') return true;
  if (Array.isArray(p.permissions) && p.permissions.includes(perm)) return true;
  return state.permissions.some(entry => entry.principal_id === p.id && entry.permission === perm && !entry.revoked);
}

function canEditPlan() { return hasProjectPermission('plan.edit'); }
function canEditSettings() { return hasProjectPermission('settings.edit'); }
function canManageMembers() { return hasProjectPermission('membership.manage'); }
function canDelegateTask() { return hasProjectPermission('task.delegate'); }
function canClaimTask() { return hasProjectPermission('task.claim'); }
function canSubmitReport() { return hasProjectPermission('result.submit'); }
function canDecideReview() { return hasProjectPermission('review.decide'); }
function canSendMessage() { return hasProjectPermission('message.send'); }

function renderAccessScope() {
  const project = state.project;
  const nameEl = $('#access-project-name');
  if (nameEl) nameEl.textContent = project ? (project.project_name || project.project_id) : 'No project selected';
  if (!project) {
    if ($('#access-project-id')) $('#access-project-id').textContent = '';
    return;
  }
  $('#access-project-id').textContent=project.project_id;
}

function syncUrlState(replace = false) {
  if (typeof history?.replaceState !== 'function' && typeof history?.pushState !== 'function') return;
  const params = new URLSearchParams();
  if (state.project) {
    params.set('project', state.project.project_id);
    if (state.project.home_id && state.dashboardAccess.home_id && state.project.home_id !== state.dashboardAccess.home_id) {
      params.set('home', state.project.home_id);
    }
  }
  if (state.view && (state.view !== 'projects' || state.project)) params.set('view', state.view);
  if (state.view === 'chat' && state.channelId) params.set('channel', state.channelId);
  else if (state.view === 'chat' && state.recipient) params.set('recipient', state.recipient);
  if (state.view === 'settings') {
    if (state.settingsTab !== 'access') params.set('sub', state.settingsTab);
    else if (state.accessSection !== ACCESS_SECTIONS[0]) params.set('section', state.accessSection);
  }
  if (state.view === 'search') {
    if (state.searchQuery) params.set('q', state.searchQuery);
    if (state.searchTab === 'graph') params.set('search_tab', 'graph');
  }
  if (state.view === 'plans') {
    if (state.selectedPlanRev) params.set('revision', state.selectedPlanRev);
    if (state.selectedPlanID) params.set('plan', state.selectedPlanID);
    if (state.planTab === 'context') params.set('plan_tab', 'context');
    else if (state.planVariant) params.set('variant', state.planVariant);
  }
  if (state.view === 'todos') {
    if (state.todoRevision) params.set('revision', state.todoRevision);
    if (state.todoScope !== 'project') params.set('scope', state.todoScope);
    if (state.todoGoalTab !== 'all') params.set('goal', state.todoGoalTab);
    if (state.todoScope === 'agent' && state.todoAgent) params.set('agent', state.todoAgent);
    if (state.todoLayout === 'kanban') params.set('layout', 'kanban');
    if (state.taskFilters.status) params.set('status', state.taskFilters.status);
    if (state.taskFilters.kind) params.set('kind', state.taskFilters.kind);
    if (state.taskFilters.assignee) params.set('assignee', state.taskFilters.assignee);
    if (state.taskFilters.query) params.set('query', state.taskFilters.query);
  }
  const qs = params.toString();
  const nextUrl = `${location?.pathname || '/'}${qs ? `?${qs}` : ''}`;
  if (!replace && nextUrl === location.pathname + location.search) return;
  if (replace || typeof history.pushState !== 'function') history.replaceState({}, '', nextUrl);
  else history.pushState({}, '', nextUrl);
}

function readUrlState() {
  const params = new URLSearchParams(location?.search || '');
  const view = params.get('view') || '';
  const scope = params.get('scope');
  state.todoScope = ['mine', 'agent'].includes(scope) ? scope : 'project';
  const goal = params.get('goal');
  state.todoGoalTab = ['plan', 'global'].includes(goal) ? goal : 'all';
  state.todoAgent = state.todoScope === 'agent' ? params.get('agent') || '' : '';
  state.todoRevision = view === 'todos' ? Number(params.get('revision')) || 0 : 0;
  state.todoLayout = params.get('layout') === 'kanban' ? 'kanban' : 'checklist';
  if (ROUTES.includes(view)) state.view = view;
  const sub = params.get('sub') || '';
  state.settingsTab = ['connections', 'workspaces', 'history'].includes(sub) ? sub : 'access';
  const section = params.get('section');
  state.accessSection = ACCESS_SECTIONS.includes(section) ? section : ACCESS_SECTIONS[0];
  state.planTab = view === 'plans' && params.get('plan_tab') === 'context' ? 'context' : 'reader';
  const variant = params.get('variant') || '';
  state.planVariant = view === 'plans' && /^variant_[a-f0-9]{32}$/.test(variant) ? variant : '';
  state.channelId = params.get('channel') || '';
  state.recipient = state.channelId ? '' : (params.get('recipient') || '');
  state.searchQuery = params.get('q') || '';
  state.searchTab = view === 'search' && params.get('search_tab') === 'graph' ? 'graph' : 'results';
  state.selectedPlanRev = Number(params.get('revision')) || 0;
  state.selectedPlanID = params.get('plan') || '';
  state.planPane = state.selectedPlanID || state.selectedPlanRev ? 'reader' : 'list';
  state.taskFilters = {
    status: params.get('status') || '',
    kind: params.get('kind') || '',
    assignee: params.get('assignee') || '',
    query: params.get('query') || ''
  };
  return {
    projectID: params.get('project') || '',
    homeID: params.get('home') || '',
    explicitView: ROUTES.includes(view) ? view : '',
    revision: state.selectedPlanRev,
    planID: state.selectedPlanID,
    planPane: state.planPane,
    accessSection: state.accessSection,
    planTab: state.planTab,
    planVariant: state.planVariant,
    todoScope: state.todoScope,
    todoRevision: state.todoRevision,
    todoAgent: state.todoAgent,
    todoLayout: state.todoLayout,
    taskFilters: state.taskFilters,
    searchQuery: state.searchQuery,
    searchTab: state.searchTab,
    channelId: state.channelId,
    recipient: state.recipient
  };
}

function clearUrlProject() {
  if (typeof history?.replaceState === 'function') {
    history.replaceState({}, '', location?.pathname || '/');
  }
}

function selectProject(project, opts = {}) {
  $('#project-roles-content')?.replaceChildren();
  if ($('#load-project-roles')) {
    const label = $('#load-project-roles span');
    if (label) label.textContent = 'Refresh';
    else $('#load-project-roles').textContent = 'Refresh';
    $('#load-project-roles').disabled = false;
  }
  state.generation += 1;
  state.project=project;state.projectReadDenied=false;state.replicas=[];
  state.agentDMs = [];
  if ($('#copy-project-status')) $('#copy-project-status').textContent = '';
  state.plan = null;
  state.planHistory = [];
  state.planThreads = [];
  state.contextRecords = [];
  state.contextError = '';
  state.remoteAccessEnabled = false;
  state.selectedPlanRev = opts.revision || 0;
  state.selectedPlanID = opts.planID || '';
  state.planPane = opts.planPane || (opts.planID || opts.revision ? 'reader' : 'list');
  state.planTab = opts.planTab || 'reader';
  state.planVariant = opts.planVariant || '';
  state.comparePlanRev = 0;
  state.tasks = [];
  state.taskCounts = {};
  state.taskTotal = 0;
  state.taskCursor = '';
  state.todoScope = opts.todoScope || 'project';
  state.todoRevision = opts.todoRevision || 0;
  state.accessSection = opts.accessSection || state.accessSection;
  if (typeof closeAgentDMs === 'function') closeAgentDMs();
  if (typeof closeChatSearch === 'function') closeChatSearch();
  if (typeof resetPlanUiState === 'function') resetPlanUiState();
  state.todoAgent = opts.todoAgent || '';
  state.todoLayout = opts.todoLayout || 'checklist';
  state.taskFilters = opts.taskFilters || { status: '', kind: '', assignee: '', query: '' };
  for (const dlgId of [
    'add-project-dialog', 'message-actions-dialog', 'message-source-dialog',
    'personal-edit-dialog', 'plan-dialog', 'plan-walkthrough-dialog', 'context-dialog',
    'share-project-dialog', 'task-dialog', 'detail-dialog', 'audit-dialog',
    'work-roles-section', 'agent-parent-dialog'
  ]) {
    closeDialog(dlgId);
  }
  if ($('#recovery-output')) { $('#recovery-output').textContent = ''; $('#recovery-output').hidden = true; }
  if ($('#copy-recovery-btn')) $('#copy-recovery-btn').hidden = true;
  if ($('#invite-result')) { $('#invite-result').textContent = ''; $('#invite-result').hidden = true; }
  state.agentQuery = '';
  state.profileAgent = '';
  state.agentPane = 'roster';
  state.agents = [];
  state.presence = [];
  state.permissions = [];
  state.roles = [];
  state.agentTasks = [];
  state.agentTasksAgentID = '';
  state.agentTasksLoading = false;
  state.agentTasksError = '';
  state.rolePresets = [];
  state.repositories = [];
  state.workspaces = [];
  state.fileReservations = [];
  state.certificates = [];
  state.certInvitations = [];
  state.integrations = [];
  state.legacyImports = [];
  state.legacyRecords = [];
  state.events = [];
  state.retention = null;
  state.messages = [];
  state.chatLoaded = false;
  state.chatUnseen = 0;
  state.chatFetchedSequence = 0;
  state.chatActivity.clear();
  state.messagesHasOlder = false;
  state.messagesOldestSeq = 0;
  state.settings = null;
  state.budget = null;
  state.checklists = { available: false, reason: '', items: [] };
  state.searchQuery = opts.searchQuery || '';
  state.searchTab = opts.searchTab || 'results';
  state.searchResults = null;
  state.graphify = null;
  state.searchPending = false;
  if ($('#header-search-input')) $('#header-search-input').value = state.searchQuery;
  state.channels = [];
  state.channelId = opts.channelId !== undefined ? opts.channelId : '';
  state.recipient = state.channelId ? '' : (opts.recipient !== undefined ? opts.recipient : '');
  state.mobilePane = 'thread';
  if (opts.view && ROUTES.includes(opts.view)) {
    state.view = opts.view;
  }
  renderAccessScope();
  syncUrlState(Boolean(opts.replaceUrl));
  render();
}

/* SHARED DOMAIN HELPERS & CONTRACT INVARIANTS */
function principalLabel(principalID, fallbackName) {
  if (!principalID) return fallbackName || 'System';
  if (state.project && principalID === state.project.id) return 'You';
  const found = state.agents.find(a => a.id === principalID);
  if (found?.name) return found.name;
  return fallbackName || principalID;
}

function labelInitial(name) {
  return Array.from(String(name || '').trim())[0]?.toUpperCase() || '?';
}

function agentObject(principalID) {
  return state.agents.find(a => a.id === principalID) || null;
}

function presenceFor(principalID) {
  return state.presence.find(p => p.principal_id === principalID) || null;
}

function agentHasWorkRole(agentID, roleID) {
  if (!roleID) return true;
  return state.roles.some(r => r.principal_id === agentID && r.role_id === roleID);
}

function eligibleAssignees(t) {
  const contract=t.report_contract||t.contract||{};
  return state.agents.filter(a => {
    if (a.revoked) return false;
    const pres = presenceFor(a.id);
    if (!pres || !pres.online) return false;
    if (contract.required_role && a.role !== contract.required_role && !agentHasWorkRole(a.id, contract.required_role)) {
      return false;
    }
    const reportedCaps = Array.isArray(pres.reported_capabilities) ? pres.reported_capabilities : [];
    const hasCapabilities = !Array.isArray(contract.required_capabilities) || contract.required_capabilities.length === 0 || contract.required_capabilities.every(cap => reportedCaps.includes(cap));
    return hasCapabilities;
  });
}

function taskContractReviewLabel(t) {
  const contract=t.report_contract||t.contract||{};
  return contract.review_type==='diagram_report'?'Markdown report with UML diagrams':'Full code review';
}

const UNVERIFIED_TASK_ASSIGNMENT_HINT = 'Select an online agent reporting all required capabilities';
const HOST_REPORTED_NOTE = 'Host reported (unverified)';

function formatBudgetStatus(budget) {
  const usage = budget.reported_usage;
  const reported = usage?.records ? ` · ${Number(usage.total_tokens).toLocaleString()} reported tokens` : '';
  return `Reserved: ${budget.reserved_units || 0} · Revision: ${budget.revision || 0}${reported}`;
}

function appendAgentUsageDetails(details, presence) {
  if (presence?.reported_usage) {
    details.appendChild(createEl('p', 'muted small', 'Reported usage · ' + JSON.stringify(presence.reported_usage)));
    details.appendChild(createEl('p', 'muted small', 'Usage comes from the client. Independent provider billing is unavailable.'));
  }
}

function completionMark(done) {
  const mark = createEl('span', 'completion-mark' + (done ? ' is-done' : ''));
  mark.setAttribute('role', 'img');
  mark.setAttribute('aria-label', done ? 'Complete' : 'Incomplete');
  if (done) mark.appendChild(svgIcon('check'));
  return mark;
}

const taskStatusLabels = {
  ready: 'Ready', running: 'In progress', submitted: 'In review',
  accepted: 'Done', blocked: 'Blocked', needs_replan: 'Needs replan', cancelled: 'Cancelled'
};

/* FORMATTING, DIALOGS & SAFE MARKDOWN */
function formatTime(isoOrMs) {
  if (!isoOrMs) return '';
  const d = new Date(isoOrMs);
  if (Number.isNaN(d.getTime())) return String(isoOrMs);
  return d.toLocaleString();
}

function formatShortTime(isoOrMs) {
  if (!isoOrMs) return '';
  const d = new Date(isoOrMs);
  if (Number.isNaN(d.getTime())) return String(isoOrMs);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function openDialog(id) {
  const dlg = $(`#${id}`);
  if (dlg && typeof dlg.showModal === 'function' && !dlg.open) {
    dlg.showModal();
  }
}

function closeDialog(id) {
  const dlg = $(`#${id}`);
  if (dlg && typeof dlg.close === 'function' && dlg.open) {
    dlg.close();
  }
}

function setMobileRailOpen(open) {
  const rail = $('#app-rail');
  const backdrop = $('#rail-backdrop');
  if (rail) rail.classList.toggle('is-open', Boolean(open));
  if (backdrop) backdrop.hidden = !open;
  $('#rail-toggle-btn')?.setAttribute('aria-expanded', String(Boolean(open)));
}

function askConfirmation(title, message, quote = null) {
  return new Promise(resolve => {
    const dlg = $('#confirm-dialog');
    if (!dlg || typeof dlg.showModal !== 'function') {
      resolve(true);
      return;
    }
    $('#confirm-title').textContent = title;
    $('#confirm-message').textContent = message;
    const preview = $('#confirm-quote');
    preview.textContent = quote || '';
    preview.hidden = quote === null;
    const accept = $('#confirm-accept-btn');
    const cancel = $('#confirm-cancel-btn');
    const cleanup = result => {
      accept?.removeEventListener('click', onAccept);
      cancel?.removeEventListener('click', onCancel);
      dlg.removeEventListener('cancel', onCancel);
      dlg.removeEventListener('close', onCancel);
      preview.textContent = '';
      preview.hidden = true;
      if (dlg.open) dlg.close();
      resolve(result);
    };
    const onAccept = () => cleanup(true);
    const onCancel = () => cleanup(false);
    accept?.addEventListener('click', onAccept);
    cancel?.addEventListener('click', onCancel);
    dlg.addEventListener('cancel', onCancel);
    dlg.addEventListener('close', onCancel);
    dlg.showModal();
  });
}

function appendInlineFormatted(parent, text) {
  const parts = String(text || '').split(/(`[^`]+`|\*\*[^*]+\*\*|\[Source message\]\(gossip:message:msg_[0-9a-f]{32}\))/g);
  for (const part of parts) {
    if (!part) continue;
    const source = /^\[Source message\]\(gossip:message:(msg_[0-9a-f]{32})\)$/.exec(part);
    if (source) {
      parent.appendChild(uiButton({
        label: 'Open source message',
        cls: 'btn btn-xs btn-secondary',
        onClick: () => openSourceMessage(source[1])
      }));
      continue;
    }
    if (part.startsWith('`') && part.endsWith('`') && part.length >= 2) {
      parent.appendChild(createEl('code', '', part.slice(1, -1)));
    } else if (part.startsWith('**') && part.endsWith('**') && part.length >= 4) {
      parent.appendChild(createEl('strong', '', part.slice(2, -2)));
    } else {
      parent.appendChild(document.createTextNode ? document.createTextNode(part) : createEl('span', '', part));
    }
  }
}

function createSandboxedDiagramPreview(mermaidSource) {
  const wrap = createEl('div', 'stack-list');
  let frame = null;
  const toggleBtn = uiButton({
    label: 'Render UML diagram preview',
    cls: 'btn btn-xs btn-secondary',
    onClick: () => {
      if (frame) {
        frame.remove();
        frame = null;
        toggleBtn.textContent = 'Render UML diagram preview';
        return;
      }
      frame = createEl('iframe', 'diagram-frame');
      frame.setAttribute('sandbox','allow-scripts');
      frame.setAttribute('title', 'Sandboxed UML Diagram');
      frame.src = '/diagram.html';
      frame.addEventListener('load', () => {
        frame?.contentWindow?.postMessage(mermaidSource, '*');
      });
      wrap.appendChild(frame);
      toggleBtn.textContent = 'Hide diagram preview';
    }
  });
  wrap.appendChild(toggleBtn);
  return wrap;
}

function renderSafeMarkdown(container, markdownText) {
  clearNode(container);
  if (!container) return;
  const src = String(markdownText || '').trim();
  if (!src) {
    container.appendChild(createEl('p', 'muted', 'No content yet.'));
    return;
  }
  const lines = src.split('\n');
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const fence = line.match(/^```([\w-]*)\s*$/);
    if (fence) {
      const lang = (fence[1] || '').toLowerCase();
      const buf = [];
      i += 1;
      while (i < lines.length && !lines[i].startsWith('```')) {
        buf.push(lines[i]);
        i += 1;
      }
      i += 1;
      const codeText = buf.join('\n');
      container.appendChild(createEl('pre', 'code-block', codeText));
      if (lang === 'mermaid') container.appendChild(createSandboxedDiagramPreview(codeText));
      continue;
    }
    if (line.startsWith('### ')) {
      const h = createEl('h3');
      appendInlineFormatted(h, line.slice(4));
      container.appendChild(h);
      i += 1;
      continue;
    }
    if (line.startsWith('## ')) {
      const h = createEl('h2');
      appendInlineFormatted(h, line.slice(3));
      container.appendChild(h);
      i += 1;
      continue;
    }
    if (line.startsWith('# ')) {
      const h = createEl('h1');
      appendInlineFormatted(h, line.slice(2));
      container.appendChild(h);
      i += 1;
      continue;
    }
    if (/^\s*[-*]\s+/.test(line)) {
      const ul = createEl('ul');
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) {
        const li = createEl('li');
        appendInlineFormatted(li, lines[i].replace(/^\s*[-*]\s+/, ''));
        ul.appendChild(li);
        i += 1;
      }
      container.appendChild(ul);
      continue;
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      const ol = createEl('ol');
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        const li = createEl('li');
        appendInlineFormatted(li, lines[i].replace(/^\s*\d+\.\s+/, ''));
        ol.appendChild(li);
        i += 1;
      }
      container.appendChild(ol);
      continue;
    }
    if (!line.trim()) {
      i += 1;
      continue;
    }
    const p = createEl('p');
    appendInlineFormatted(p, line);
    container.appendChild(p);
    i += 1;
  }
}

/* DATA LOADERS (ROUTE-SCOPED + GENERATION GUARDED) */
async function loadDashboardProjects() {
  const gen = state.generation;
  try {
    const dash = await request('/api/dashboard/projects');
    if (gen !== state.generation) return dash;
    state.dashboardAccess = {
      local_access: Boolean(dash.local_access),
      home_id: dash.home_id || '',
      profile: dash.profile || ''
    };
    state.localProjects = Array.isArray(dash.local) ? dash.local : [];
    state.remoteProjects = Array.isArray(dash.remote) ? dash.remote : [];
    const legacy = await request('/api/projects');
    if (gen !== state.generation) return dash;
    state.projects = Array.isArray(legacy) ? legacy : [];
    $('#connect-required')?.setAttribute('hidden', '');
    return dash;
  } catch (err) {
    if ($('#connect-required')) $('#connect-required').hidden = false;
    throw err;
  }
}

function normalizePlanHistory(items) {
  return items.map(item => {
    if (item.plan_id) return item;
    const heading = String(item.body || '').split('\n').map(line => line.trim().replace(/^#+\s*/, '').trim()).find(Boolean);
    return { ...item, plan_id: `legacy-plan-${item.revision}`, plan_title: heading || `Legacy plan ${item.revision}` };
  });
}

function planVersion(plan) {
  if (!plan) return 0;
  const revisions = state.planHistory
    .filter(item => item.plan_id === plan.plan_id)
    .sort((a, b) => a.revision - b.revision);
  const index = revisions.findIndex(item => item.revision === plan.revision);
  return index < 0 ? 1 : index + 1;
}

function planVersionLabel(plan) {
  return `Revision ${planVersion(plan)}`;
}

function normalizePlanThreads(threads, history) {
  if (threads.length) return threads;
  const grouped = new Map();
  for (const plan of history) {
    if (!grouped.has(plan.plan_id)) {
      grouped.set(plan.plan_id, {
        id: plan.plan_id,
        title: plan.plan_title || `Plan ${plan.revision}`,
        latest_revision: plan.revision,
        status: plan.status || 'archived',
        created_at: plan.created_at
      });
    }
  }
  return [...grouped.values()];
}

async function fetchCoreProjectContext(gen) {
  const [identityRes, agentsRes, presenceRes, plansRes, threadsRes, rolesRes, permsRes, channelsRes] = await Promise.allSettled([
    request(projectApi('identity')),
    request(projectApi('agents')),
    request(projectApi('presence')),
    request(projectApi('plans')),
    request(projectApi('plan-threads')),
    request(projectApi('roles')),
    request(projectApi('permissions')),
    request(projectApi('channels'))
  ]);
  if (gen !== state.generation) return;
  if (identityRes.status === 'rejected') throw identityRes.reason;
  const identity = identityRes.value;
  Object.assign(state.project, {
    id: identity.id, name: identity.name, role: identity.role, project_role: identity.project_role, permissions: []
  });
  if (agentsRes.status === 'rejected' && agentsRes.reason?.status) {
    throw agentsRes.reason;
  }
  if (plansRes.status === 'rejected' && plansRes.reason?.status && plansRes.reason.status !== 404) {
    throw plansRes.reason;
  }
  state.agents = agentsRes.status === 'fulfilled' && Array.isArray(agentsRes.value) ? agentsRes.value : [];
  state.presence = presenceRes.status === 'fulfilled' && Array.isArray(presenceRes.value) ? presenceRes.value : [];
  const plansList = plansRes.status === 'fulfilled' && Array.isArray(plansRes.value) ? plansRes.value : [];
  state.planHistory = normalizePlanHistory(plansList);
  state.plan = state.planHistory[0] || null;
  state.planThreads = normalizePlanThreads(threadsRes.status === 'fulfilled' && Array.isArray(threadsRes.value) ? threadsRes.value : [], state.planHistory);
  if (!state.selectedPlanID || !state.planThreads.some(t => t.id === state.selectedPlanID)) {
    state.selectedPlanID = state.plan?.plan_id || state.planThreads[0]?.id || '';
  }
  state.roles = rolesRes.status === 'fulfilled' && Array.isArray(rolesRes.value) ? rolesRes.value : [];
  state.permissions = permsRes.status === 'fulfilled' && Array.isArray(permsRes.value) ? permsRes.value : [];
  state.channels = channelsRes.status === 'fulfilled' && Array.isArray(channelsRes.value) ? channelsRes.value : [];
}

async function loadChatMessages(gen, opts = {}) {
  if (!state.project) return;
  if (state.channelId) {
    const ch = state.channels.find(c => c.id === state.channelId);
    const isOwner = state.project.role === 'owner';
    if (!ch || (!isOwner && ch.my_status !== 'joined')) {
      state.messages = [];
      state.messagesHasOlder = false;
      state.messagesOldestSeq = 0;
      return;
    }
  }
  const query = state.channelId
    ? { channel: state.channelId, limit: 60 }
    : { recipient: state.recipient || '', limit: 60 };
  if (opts.before) query.before = opts.before;
  else if (opts.incremental) query.after = state.chatFetchedSequence;
  else query.recent = 1;
  const res = await request(projectApi('messages', query));
  if (gen !== state.generation) return;
  const items = Array.isArray(res) ? res : (Array.isArray(res?.messages) ? res.messages : []);
  state.chatLoaded = true;
  if (opts.before) {
    const existingIDs = new Set(state.messages.map(m => m.id));
    state.messages = [...items.filter(m => !existingIDs.has(m.id)), ...state.messages];
    state.messagesHasOlder = items.length >= 60;
  } else if (opts.incremental) {
    const existingIDs = new Set(state.messages.map(m => m.id));
    const incoming = items.filter(m => !existingIDs.has(m.id));
    state.messages.push(...incoming);
    state.chatUnseen += incoming.filter(m => m.sender !== state.project.id).length;
  } else {
    state.messages = items;
    state.messagesHasOlder = items.length >= 60;
  }
  if (!opts.before) {
    state.chatFetchedSequence = items.at(-1)?.sequence || (opts.incremental ? state.chatFetchedSequence : 0);
    if (!opts.incremental) state.chatUnseen = 0;
  }
  state.messagesOldestSeq = state.messages.reduce(
    (oldest, message) => (message.sequence && (!oldest || message.sequence < oldest) ? message.sequence : oldest),
    0
  );
}

async function loadConversationActivity(generation) {
  try {
    const activity = await request(projectApi('conversation-activity'));
    if (generation !== state.generation) return;
    state.chatActivity = new Map(activity.map(({ room, sequence }) => [room, sequence]));
    if (state.project?.source === 'remote') {
      for (const room of state.chatActivity.keys()) {
        if (room.startsWith('channel:')) state.chatActivity.delete(room);
      }
    }
    if (typeof renderConversationBadges === 'function') renderConversationBadges();
  } catch {
    // Keep unread markers during temporary failures and support peers without the activity route.
  }
}

async function loadTasksRouteData(gen, append = false) {
  if (!state.project) return;
  const query = {
    revision: state.todoRevision || '',
    status: state.taskFilters.status,
    kind: state.taskFilters.kind,
    assignee: state.todoScope === 'mine' ? state.project.id : state.todoScope === 'agent' ? (state.todoAgent || state.project.id) : state.taskFilters.assignee,
    query: state.taskFilters.query,
    cursor: append ? state.taskCursor : ''
  };
  const [searchRes, countsRes, checklistsRes] = await Promise.allSettled([
    request(projectApi('task-search', query)),
    request(projectApi('task-counts')),
    request(projectApi('personal-items'))
  ]);
  if (gen !== state.generation) return;
  if (searchRes.status === 'rejected') throw searchRes.reason;
  const payload = searchRes.value || {};
  const pageTasks = Array.isArray(payload.tasks) ? payload.tasks : (Array.isArray(payload) ? payload : []);
  state.tasks = append ? [...state.tasks, ...pageTasks] : pageTasks;
  state.taskTotal = typeof payload.total === 'number' ? payload.total : state.tasks.length;
  state.taskCursor = payload.next_cursor || '';
  state.taskCounts = countsRes.status === 'fulfilled' && countsRes.value ? countsRes.value : {};
  state.checklists = checklistsRes.status === 'fulfilled' && checklistsRes.value ? checklistsRes.value : { available: false, reason: 'Unavailable', items: [] };
}

async function loadTaskSearchOnly(gen, append = false, isCurrent = () => true) {
  if (!state.project) return;
  const payload = await request(projectApi('task-search', {
    revision: state.todoRevision || '',
    status: state.taskFilters.status,
    kind: state.taskFilters.kind,
    assignee: state.todoScope === 'mine' ? state.project.id : state.todoScope === 'agent' ? (state.todoAgent || state.project.id) : state.taskFilters.assignee,
    query: state.taskFilters.query,
    cursor: append ? state.taskCursor : ''
  }));
  if (gen !== state.generation || !isCurrent()) return;
  const pageTasks = Array.isArray(payload?.tasks) ? payload.tasks : (Array.isArray(payload) ? payload : []);
  state.tasks = append ? [...state.tasks, ...pageTasks] : pageTasks;
  state.taskTotal = typeof payload?.total === 'number' ? payload.total : state.tasks.length;
  state.taskCursor = payload?.next_cursor || '';
}

async function loadPlansRouteData(gen) {
  const [plansRes, threadsRes, reposRes, contextRes] = await Promise.allSettled([
    request(projectApi('plans')),
    request(projectApi('plan-threads')),
    request(projectApi('repositories')),
    request(projectApi('context'))
  ]);
  if (gen !== state.generation) return;
  state.contextRecords = contextRes.status === 'fulfilled' ? contextRes.value : [];
  state.contextError = contextRes.status === 'rejected' ? contextRes.reason.message : '';
  state.planHistory = normalizePlanHistory(plansRes.status === 'fulfilled' && Array.isArray(plansRes.value) ? plansRes.value : (state.plan ? [state.plan] : []));
  state.planThreads = normalizePlanThreads(threadsRes.status === 'fulfilled' && Array.isArray(threadsRes.value) ? threadsRes.value : [], state.planHistory);
  if (!state.selectedPlanID || !state.planThreads.some(t => t.id === state.selectedPlanID)) state.selectedPlanID = state.plan?.plan_id || state.planThreads[0]?.id || '';
  const selectedThreadHistory = state.planHistory.filter(p => p.plan_id === state.selectedPlanID);
  if (!selectedThreadHistory.some(p => p.revision === state.selectedPlanRev)) state.selectedPlanRev = selectedThreadHistory[0]?.revision || 0;
  state.repositories = reposRes.status === 'fulfilled' && Array.isArray(reposRes.value) ? reposRes.value : [];
}

async function loadAgentsRouteData(gen) {
  const selectedID = state.profileAgent || state.agents.find(agent => agent.id !== state.project?.id)?.id || state.agents[0]?.id || '';
  if (!state.profileAgent && selectedID) state.profileAgent = selectedID;
  state.agentTasksAgentID = selectedID;
  state.agentTasksLoading = Boolean(selectedID);
  state.agentTasksError = '';
  const [presetsRes, checklistsRes, tasksRes] = await Promise.allSettled([
    request(projectApi('role-presets')),
    request(projectApi('personal-items')),
    selectedID ? request(projectApi('task-search', { assignee: selectedID })) : Promise.resolve({ tasks: [] })
  ]);
  if (gen !== state.generation) return;
  state.rolePresets = presetsRes.status === 'fulfilled' && Array.isArray(presetsRes.value) ? presetsRes.value : [];
  state.checklists = checklistsRes.status === 'fulfilled' && checklistsRes.value ? checklistsRes.value : { available: false, reason: '', items: [] };
  state.agentTasks = tasksRes.status === 'fulfilled' ? (Array.isArray(tasksRes.value?.tasks) ? tasksRes.value.tasks : []) : [];
  state.agentTasksError = tasksRes.status === 'rejected' ? tasksRes.reason.message : '';
  state.agentTasksLoading = false;
}

async function loadAgentTaskPreview(agentID, gen = state.generation) {
  state.agentTasks = [];
  state.agentTasksAgentID = agentID;
  state.agentTasksLoading = true;
  state.agentTasksError = '';
  if (typeof renderAgentsView === 'function') renderAgentsView();
  try {
    const result = await request(projectApi('task-search', { assignee: agentID }));
    if (gen !== state.generation || state.profileAgent !== agentID) return;
    state.agentTasks = Array.isArray(result?.tasks) ? result.tasks : [];
  } catch (error) {
    if (gen === state.generation && state.profileAgent === agentID) state.agentTasksError = error.message;
  } finally {
    if (gen === state.generation && state.profileAgent === agentID) {
      state.agentTasksLoading = false;
      if (typeof renderAgentsView === 'function') renderAgentsView();
    }
  }
}

async function loadSearchRouteData(gen, isCurrent = () => true) {
  const q = state.searchQuery.trim();
  const [searchRes, graphRes] = await Promise.allSettled([
    q ? request(projectApi('search', { q })) : Promise.resolve(null),
    request(projectApi('graphify', { q }))
  ]);
  if (gen !== state.generation || !isCurrent()) return;
  state.searchPending = false;
  if (searchRes.status === 'rejected') throw searchRes.reason;
  state.searchResults = searchRes.status === 'fulfilled' ? searchRes.value : null;
  state.graphify = graphRes.status === 'fulfilled' ? graphRes.value : { status: 'unavailable', reason: 'Unable to inspect Graphify.' };
}

async function loadSettingsRouteData(gen) {
  const tab = state.settingsTab;
  const section = state.accessSection;
  const resources = {
    workspaces: ['repositories', 'workspaces', 'file-reservations', 'integrations'],
    connections: ['replicas', 'remote-access'],
    history: ['events', 'retention', 'legacy-imports']
  }[tab] || {
    'access-devices': ['certificates', 'certificate-invitations'],
    'access-limits': ['settings', 'budget']
  }[section] || [];
  const current = () => gen === state.generation && state.view === 'settings'
    && state.settingsTab === tab && (tab !== 'access' || state.accessSection === section);
  if (!current()) return;
  const status = $('#settings-load-status');
  if (status) {
    status.hidden = !resources.length;
    status.textContent = resources.length ? 'Loading section…' : '';
  }
  const results = await Promise.allSettled(resources.map(resource => request(
    resource === 'remote-access'
      ? '/api/remote-access'
      : resource === 'events'
        ? projectApi('events', { recent: 1 })
        : projectApi(resource))));
  if (!current()) return;
  const fields = {
    'file-reservations': 'fileReservations',
    'certificate-invitations': 'certInvitations',
    'legacy-imports': 'legacyImports'
  };
  resources.forEach((resource, index) => {
    const result = results[index];
    const value = result.status === 'fulfilled' ? result.value : null;
    if (resource === 'remote-access') {
      state.remoteAccessEnabled = Boolean(value?.enabled);
    } else if (['budget', 'settings', 'retention'].includes(resource)) {
      state[resource] = value;
    } else {
      state[fields[resource] || resource] = Array.isArray(value) ? value : [];
    }
  });
  if (tab === 'history') {
    state.legacyRecords = [];
    const first = state.legacyImports[0];
    if (first) {
      try {
        const records = await request(projectApi('legacy-records', { import_id: first.id }));
        if (current() && Array.isArray(records)) state.legacyRecords = records;
      } catch {}
    }
  }
  if (current()) {
    const failures = results.filter(result => result.status === 'rejected' && ![403, 404, 501].includes(result.reason?.status));
    if (status) {
      status.hidden = !failures.length;
      status.textContent = failures.length ? 'Some settings could not be loaded. Refresh to try again.' : '';
    }
    if (typeof renderSettingsView === 'function') renderSettingsView();
  }
}

async function refresh() {
  const gen = state.generation;
  const project = state.project;
  try {
    if (!project) {
      await loadDashboardProjects();
      if (gen === state.generation) render();
      return;
    }
    await fetchCoreProjectContext(gen);
    if (gen !== state.generation) return;
    if (state.view === 'chat') {
      await loadChatMessages(gen);
      if (gen === state.generation) await loadConversationActivity(gen);
      if (gen === state.generation && typeof loadAgentDMThreads === 'function') await loadAgentDMThreads(gen);
    } else if (state.view === 'plans') {
      await loadPlansRouteData(gen);
    } else if (state.view === 'todos') {
      await loadTasksRouteData(gen, false);
    } else if (state.view === 'agents') {
      await loadAgentsRouteData(gen);
    } else if (state.view === 'search') {
      await loadSearchRouteData(gen);
    } else if (state.view === 'settings') {
      await loadSettingsRouteData(gen);
      if (gen === state.generation && state.settingsTab === 'access' && state.accessSection === 'project-roles-card' && typeof loadProjectRoleEditor === 'function') {
        await loadProjectRoleEditor();
      }
    } else if (state.view === 'projects') {
      await loadDashboardProjects();
    }
    if (gen !== state.generation) return;
    showError('');
    render();
  } catch (e) {
    if (gen !== state.generation) return;
    if (e.status === 403 && state.project === project) {
      state.projectReadDenied=true;
      Object.assign(state,{tasks:[],messages:[],repositories:[],workspaces:[],integrations:[],agents:[],presence:[],plan:null,planHistory:[]});
      render();
      return;
    }
    if (state.project === null) {
      render();
      return;
    }
    showError(e.message || 'Unable to load project data.');
    render();
  }
}

function renderAuditEventRow(ev) {
  const row = createEl('div', 'list-item');
  const meta = [formatTime(ev.created_at), ev.actor ? principalLabel(ev.actor) : '', ev.summary]
    .filter(Boolean)
    .join(' · ');
  row.append(
    createEl('div', 'list-item-title', `${ev.kind} · #${ev.sequence}`),
    createEl('div', 'muted small', meta)
  );
  return row;
}

function setView(viewName, opts = {}) {
  if (!ROUTES.includes(viewName)) return;
  if (state.view && state.view !== 'search' && viewName === 'search') {
    state.previousView = state.view;
  }
  if (state.view === 'chat' && viewName !== 'chat') {
    if (typeof closeAgentDMs === 'function') closeAgentDMs();
    if (typeof closeChatSearch === 'function') closeChatSearch();
  }
  const routeChanged = state.view !== viewName;
  state.view = viewName;
  if (viewName === 'plans') {
    if (opts.planID !== undefined) state.selectedPlanID = opts.planID;
    if (opts.revision !== undefined) state.selectedPlanRev = opts.revision;
    if (opts.planID !== undefined || opts.revision !== undefined) state.planPane = 'reader';
  }
  if (opts.revision !== undefined && viewName === 'todos') {
    state.todoRevision = opts.revision;
    if (opts.revision > 0) {
      state.todoScope = 'project';
      state.todoGoalTab = 'plan';
    }
  }
  if (opts.todoAgent) {
    state.todoScope = opts.todoAgent === state.project?.id ? 'mine' : 'agent';
    state.todoAgent = opts.todoAgent;
  }
  if (viewName === 'chat' && (opts.channelId !== undefined || opts.recipient !== undefined)) {
    state.messages = [];
    state.messagesHasOlder = false;
    state.messagesOldestSeq = 0;
    state.chatLoaded = false;
  }
  if (opts.channelId !== undefined) {
    state.channelId = opts.channelId;
    if (opts.channelId) state.recipient = '';
  }
  if (opts.recipient !== undefined) {
    state.recipient = opts.recipient;
    if (opts.recipient) state.channelId = '';
  }
  if (opts.mobilePane) state.mobilePane = opts.mobilePane;
  setMobileRailOpen(false);
  syncUrlState(Boolean(opts.replaceUrl));
  if (state.project || viewName === 'projects') {
    state.generation += 1;
    render();
    if (routeChanged) window.scrollTo(0, 0);
    refresh();
  } else {
    render();
  }
}

/* SHELL CHROME & ROUTE DISPATCHER */
function renderShellChrome() {
  const p = state.project;
  if ($('.app-shell')) $('.app-shell').dataset.view = state.view;
  const share = $('#share-project-btn');
  if (share) {
    share.hidden = state.project?.role !== 'owner' || state.project?.source === 'remote';
    share.onclick = () => typeof openShareProject === 'function' && openShareProject();
  }
  const authBadge = $('#authority-badge');
  const signOut = $('#logout');
  if (signOut) signOut.hidden = Boolean(state.dashboardAccess.local_access);
  if (authBadge) {
    if (state.dashboardAccess.local_access) authBadge.textContent = 'Local authority';
    else authBadge.textContent = state.dashboardAccess.profile ? `Profile: ${state.dashboardAccess.profile}` : 'Scoped session';
  }
  const titleEl = $('#topbar-title');
  if (titleEl) titleEl.textContent = ROUTE_TITLES[state.view] || 'Projects';
  document.title = (p ? p.project_name + ' · ' : '') + (ROUTE_TITLES[state.view] || 'Projects') + ' · Gossip';

  const projBadge = $('#topbar-project-badge');
  if (projBadge) {
    if (p) {
      projBadge.textContent = p.project_name || p.project_id;
      projBadge.hidden = false;
    } else {
      projBadge.hidden = true;
    }
  }
  const homeBadge = $('#topbar-home-badge');
  if (homeBadge) {
    if (p && p.home_id && state.dashboardAccess.home_id && p.home_id !== state.dashboardAccess.home_id) {
      homeBadge.textContent = 'Remote';
      homeBadge.hidden = false;
    } else if (p) {
      homeBadge.textContent = 'Local';
      homeBadge.hidden = false;
    } else {
      homeBadge.hidden = true;
    }
  }

  const search = $('#header-search-input');
  const label = {
    projects: 'Find projects',
    chat: 'Search messages',
    plans: 'Search project content',
    todos: 'Search tasks',
    agents: 'Find agents',
    search: 'Search all project content',
    settings: 'Search project content'
  }[state.view] || 'Search project content';
  if (search) {
    search.placeholder = label + '…';
    search.setAttribute('aria-label', label);
    const searchButton = $('#header-search-form .search-submit');
    if (searchButton) {
      searchButton.setAttribute('aria-label', label);
      searchButton.title = label;
    }
    if (search.dataset.view !== state.view) {
      search.dataset.view = state.view;
      search.value = {
        projects: state.projectQuery, search: state.searchQuery, agents: state.agentQuery, todos: state.taskFilters.query
      }[state.view] || '';
    }
    const canSearch = Boolean(p) || state.view === 'projects';
    if ($('#header-search-form')) $('#header-search-form').hidden = !canSearch;
    search.disabled = !canSearch;
  }

  const switcher = $('#project-switcher');
  const switcherOptions = $('#project-switcher-options');
  const switcherLabel = $('#project-switcher-label');
  const switcherIconUse = $('#project-switcher-icon use');
  const switcherEntries = [];
  if (switcher || switcherOptions) {
    if (switcher) clearNode(switcher);
    if (switcherOptions) clearNode(switcherOptions);
    const emptyOpt = createEl('option', '', 'Select a project…');
    emptyOpt.value = '';
    switcher?.appendChild(emptyOpt);
    const localReady = state.localProjects.filter(lp => lp.status === 'ready');
    for (const lp of localReady) {
      const selected = Boolean(p && p.project_id === lp.project_id && (!p.home_id || p.home_id === lp.home_id));
      switcherEntries.push({
        value: `local:${lp.project_id}`,
        name: lp.project_name || lp.project_id,
        icon: 'projects',
        selected,
        nativeLabel: `${lp.project_name} (Local)`,
        open: () => typeof openLocalProject === 'function' && openLocalProject(lp, state.view === 'projects' ? 'chat' : state.view)
      });
    }
    for (const rp of state.remoteProjects) {
      const selected = Boolean(p && p.project_id === rp.project_id && p.home_id === rp.home_id);
      switcherEntries.push({
        value: `remote:${rp.home_id}:${rp.project_id}`,
        name: rp.project_name || rp.project_id,
        icon: 'globe',
        selected,
        nativeLabel: `${rp.project_name} (${rp.status})`,
        open: () => typeof openRemoteProject === 'function' && openRemoteProject(rp, state.view === 'projects' ? 'chat' : state.view)
      });
    }
    for (const ep of state.projects) {
      const already = localReady.some(lp => lp.project_id === ep.project_id)
        || state.remoteProjects.some(rp => rp.project_id === ep.project_id && rp.home_id === ep.home_id);
      if (already) continue;
      const selected = Boolean(p && p.project_id === ep.project_id);
      switcherEntries.push({
        value: `session:${ep.project_id}`,
        name: ep.project_name || ep.project_id,
        icon: ep.source === 'remote' ? 'globe' : 'projects',
        selected,
        nativeLabel: ep.project_name || ep.project_id,
        open: async () => {
          selectProject(ep, { view: state.view === 'projects' ? 'chat' : state.view });
          await refresh();
        }
      });
    }
    for (const item of switcherEntries) {
      const opt = createEl('option', '', item.nativeLabel);
      opt.value = item.value;
      if (item.selected) opt.selected = true;
      switcher?.appendChild(opt);
      if (switcherOptions) {
        const optionBtn = createEl('button', `project-switcher-option${item.selected ? ' is-selected' : ''}`);
        optionBtn.type = 'button';
        optionBtn.setAttribute('role', 'option');
        optionBtn.setAttribute('aria-selected', String(item.selected));
        optionBtn.dataset.value = item.value;
        optionBtn.append(
          svgIcon(item.icon),
          createEl('span', 'project-switcher-option-name', item.name)
        );
        if (item.selected) optionBtn.appendChild(svgIcon('check'));
        optionBtn.addEventListener('click', async () => {
          const menu = $('#project-switcher-menu');
          if (menu) menu.hidden = true;
          $('#project-switcher-btn')?.setAttribute('aria-expanded', 'false');
          if (switcher) switcher.value = item.value;
          await item.open();
        });
        switcherOptions.appendChild(optionBtn);
      }
    }
    const activeEntry = switcherEntries.find(item => item.selected) || null;
    if (switcherLabel) switcherLabel.textContent = activeEntry ? activeEntry.name : (p ? (p.project_name || p.project_id) : 'Select a project…');
    if (switcherIconUse) switcherIconUse.setAttribute('href', `#icon-${activeEntry ? activeEntry.icon : (p?.source === 'remote' ? 'globe' : 'projects')}`);
    if (switcherOptions && !switcherEntries.length) {
      switcherOptions.appendChild(createEl('p', 'muted small project-switcher-empty', 'No projects ready yet.'));
    }
  }

  const railHint = $('#rail-project-hint');
  if (railHint) {
    const needsRepair = state.localProjects.filter(lp => lp.status !== 'ready');
    if (needsRepair.length > 0) {
      railHint.textContent = `${needsRepair.length} local project(s) need owner selection in Projects.`;
      railHint.hidden = false;
    } else {
      railHint.hidden = true;
    }
  }

  $$('.nav-tab').forEach(btn => {
    const v = btn.getAttribute('data-view');
    btn.classList.toggle('is-active', v === state.view);
    if (v === state.view) btn.setAttribute('aria-current', 'page');
    else btn.removeAttribute('aria-current');
    btn.disabled = v !== 'projects' && !p;
  });

  const planCount = state.planThreads.length;
  const plansNav = $('.nav-tab[data-view="plans"]');
  if (plansNav) {
    plansNav.setAttribute('aria-label', planCount ? `Plans, ${planCount} independent plans` : 'Plans');
    plansNav.title = planCount ? `${planCount} independent plans` : 'Plans';
  }
  if ($('#nav-badge-plans')) $('#nav-badge-plans').textContent = planCount ? String(planCount) : '';
  const openCount = (state.taskCounts?.open || 0) + (state.taskCounts?.assigned || 0) + (state.taskCounts?.in_progress || 0);
  if ($('#nav-badge-todos')) $('#nav-badge-todos').textContent = openCount ? String(openCount) : '';
  const onlineAgents = state.presence.filter(pr => pr.online).length;
  if ($('#nav-badge-agents')) $('#nav-badge-agents').textContent = state.agents.length ? `${onlineAgents}/${state.agents.length}` : '';

  const idName = $('#identity-name');
  const idRole = $('#identity-role');
  if (idName) idName.textContent = p ? `You · ${p.name || p.id}` : 'You';
  if (idRole) idRole.textContent = p ? `Role: ${p.project_role || p.role || 'member'} · ${p.project_name || p.project_id}` : 'No project opened';
  if ($('#home-id-label')) $('#home-id-label').textContent = state.dashboardAccess.home_id ? `Home: ${state.dashboardAccess.home_id}` : '';

  const auditBtn = $('#open-audit-btn');
  if (auditBtn) auditBtn.hidden = !p;
  const denied = $('#access-denied-banner');
  if (denied) denied.hidden = !state.projectReadDenied;

  ROUTES.forEach(route => {
    const el = $(`#view-${route}`);
    if (el) el.hidden = state.view !== route;
  });
}

function render() {
  renderShellChrome();
  const renderers = {
    projects: typeof renderProjectsView === 'function' ? renderProjectsView : null,
    chat: typeof renderChatView === 'function' ? renderChatView : null,
    plans: typeof renderPlansView === 'function' ? renderPlansView : null,
    todos: typeof renderTodosView === 'function' ? renderTodosView : null,
    agents: typeof renderAgentsView === 'function' ? renderAgentsView : null,
    search: typeof renderSearchView === 'function' ? renderSearchView : null,
    settings: typeof renderSettingsView === 'function' ? renderSettingsView : null
  };
  renderers[state.view]?.();
}

function wireShellEvents() {
  const shell = $('.app-shell');
  const resizeHandle = $('#rail-resize-handle');
  const resizeKey = 'gossip.sidebar.width';
  const RAIL_MIN_WIDTH = 248;
  const RAIL_MAX_WIDTH = 380;
  const RAIL_DEFAULT_WIDTH = 264;
  const setRailWidth = raw => {
    const width = Math.max(RAIL_MIN_WIDTH, Math.min(RAIL_MAX_WIDTH, window.innerWidth - 32, Math.round(raw)));
    shell?.style.setProperty('--rail-width', `${width}px`);
    resizeHandle?.setAttribute('aria-valuenow', String(width));
    try { localStorage.setItem(resizeKey, String(width)); } catch {}
  };
  try {
    const saved = Number(localStorage.getItem(resizeKey));
    if (saved >= RAIL_MIN_WIDTH && saved <= RAIL_MAX_WIDTH) setRailWidth(saved);
    else setRailWidth(RAIL_DEFAULT_WIDTH);
  } catch {}
  let resizingRail = false;
  resizeHandle?.addEventListener('pointerdown', event => {
    if ((window.innerWidth < 1100 && !$('#app-rail').classList.contains('is-open')) || event.button !== 0) return;
    resizingRail = true;
    resizeHandle.setPointerCapture(event.pointerId);
    event.preventDefault();
  });
  resizeHandle?.addEventListener('pointermove', event => {
    if (resizingRail) setRailWidth(event.clientX);
  });
  const finishResize = () => { resizingRail = false; };
  resizeHandle?.addEventListener('pointerup', finishResize);
  resizeHandle?.addEventListener('pointercancel', finishResize);
  resizeHandle?.addEventListener('keydown', event => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const width = Number(resizeHandle.getAttribute('aria-valuenow')) || RAIL_DEFAULT_WIDTH;
    setRailWidth(event.key === 'Home' ? RAIL_MIN_WIDTH : event.key === 'End' ? RAIL_MAX_WIDTH : width + (event.key === 'ArrowRight' ? 12 : -12));
  });

  const switcherBtn = $('#project-switcher-btn');
  const switcherMenu = $('#project-switcher-menu');
  const closeProjectMenu = () => {
    if (switcherMenu) switcherMenu.hidden = true;
    switcherBtn?.setAttribute('aria-expanded', 'false');
  };
  switcherBtn?.addEventListener('click', () => {
    if (!switcherMenu) return;
    const willOpen = switcherMenu.hidden;
    switcherMenu.hidden = !willOpen;
    switcherBtn.setAttribute('aria-expanded', String(willOpen));
  });
  switcherBtn?.addEventListener('keydown', event => {
    if (!switcherMenu || event.key !== 'ArrowDown') return;
    event.preventDefault();
    switcherMenu.hidden = false;
    switcherBtn.setAttribute('aria-expanded', 'true');
    const first = switcherMenu.querySelector('[role="menuitem"]:not([hidden])');
    first?.focus();
  });
  switcherMenu?.addEventListener('keydown', event => {
    if (event.key === 'Escape') {
      closeProjectMenu();
      switcherBtn?.focus();
      return;
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const items = Array.from(switcherMenu.querySelectorAll('[role="menuitem"]:not([hidden]):not(:disabled)'));
    if (!items.length) return;
    event.preventDefault();
    const idx = items.indexOf(document.activeElement);
    let nextIdx = 0;
    if (event.key === 'ArrowDown') nextIdx = idx < 0 ? 0 : (idx + 1) % items.length;
    else if (event.key === 'ArrowUp') nextIdx = idx <= 0 ? items.length - 1 : idx - 1;
    else if (event.key === 'Home') nextIdx = 0;
    else if (event.key === 'End') nextIdx = items.length - 1;
    items[nextIdx].focus();
  });
  $('#search-back-btn')?.addEventListener('click', () => {
    const target = state.previousView && state.previousView !== 'search' ? state.previousView : 'chat';
    setView(target);
  });
  ['#projects-tabs', '#plan-view-tabs', '#todo-scope-tabs', '#agents-view-tabs', '#search-mode-tabs', '#settings-tabs']
    .forEach(selector => {
      const tablist = $(selector);
      if (!tablist) return;
      tablist.addEventListener('keydown', event => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
        const tabs = Array.from(tablist.querySelectorAll('[role="tab"]:not([hidden]):not(:disabled)'));
        const currentIndex = tabs.indexOf(document.activeElement);
        if (currentIndex === -1 || !tabs.length) return;
        event.preventDefault();
        let nextIndex = currentIndex;
        if (event.key === 'ArrowRight') nextIndex = (currentIndex + 1) % tabs.length;
        else if (event.key === 'ArrowLeft') nextIndex = (currentIndex - 1 + tabs.length) % tabs.length;
        else if (event.key === 'Home') nextIndex = 0;
        else if (event.key === 'End') nextIndex = tabs.length - 1;
        tabs[nextIndex].focus();
        tabs[nextIndex].click();
      });
    });

  applyTheme(document?.documentElement?.dataset?.theme || resolveInitialTheme(), false);
  $('#theme-toggle-btn')?.addEventListener('click', () => {
    const next = document?.documentElement?.dataset?.theme === 'dark' ? 'light' : 'dark';
    applyTheme(next, true);
  });
  if (typeof window !== 'undefined') {
    window.matchMedia?.('(prefers-color-scheme: dark)')?.addEventListener?.('change', e => {
      try {
        if (typeof localStorage !== 'undefined' && localStorage.getItem(THEME_STORAGE_KEY)) return;
      } catch {}
      applyTheme(e.matches ? 'dark' : 'light', false);
    });
  }

  $('#error-dismiss')?.addEventListener('click', () => showError(''));
  const closeRail = () => {
    setMobileRailOpen(false);
    $('#rail-toggle-btn')?.focus();
  };
  $('#rail-toggle-btn')?.addEventListener('click', () => {
    setMobileRailOpen(true);
    $('#rail-close-btn')?.focus();
  });
  $('#rail-close-btn')?.addEventListener('click', closeRail);
  $('#rail-backdrop')?.addEventListener('click', closeRail);
  $('#app-rail')?.addEventListener('keydown', event => {
    if (event.key === 'Escape' && $('#app-rail').classList.contains('is-open')) closeRail();
  });

  $$('.modal-close, [data-close-dialog]').forEach(btn => {
    btn.addEventListener('click', () => closeDialog(btn.getAttribute('data-close-dialog')));
  });

  $('#project-switcher')?.addEventListener('change', async e => {
    const val = e.target.value || '';
    if (!val) return;
    if (val.startsWith('local:')) {
      const id = val.slice(6);
      const lp = state.localProjects.find(x => x.project_id === id);
      if (lp && typeof openLocalProject === 'function') await openLocalProject(lp, state.view === 'projects' ? 'chat' : state.view);
    } else if (val.startsWith('remote:')) {
      const [, homeID, projID] = val.split(':');
      const rp = state.remoteProjects.find(x => x.home_id === homeID && x.project_id === projID);
      if (rp && typeof openRemoteProject === 'function') await openRemoteProject(rp, state.view === 'projects' ? 'chat' : state.view);
    } else if (val.startsWith('session:')) {
      const id = val.slice(8);
      const sp = state.projects.find(x => x.project_id === id);
      if (sp) {
        selectProject(sp, { view: state.view === 'projects' ? 'chat' : state.view });
        await refresh();
      }
    }
  });

  $$('.nav-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      const view = btn.getAttribute('data-view');
      if (view) setView(view);
      if (view === 'search') $('#header-search-input')?.focus();
    });
  });

  $('#refresh')?.addEventListener('click', () => {
    if ($('#shell-actions')) $('#shell-actions').open = false;
    refresh();
  });
  document.addEventListener('click', event => {
    const actions = $('#shell-actions');
    if (actions?.open && !actions.contains(event.target)) actions.open = false;
    const dropdown = $('#project-dropdown');
    const menu = $('#project-switcher-menu');
    if (menu && !menu.hidden && dropdown && !dropdown.contains(event.target)) {
      menu.hidden = true;
      $('#project-switcher-btn')?.setAttribute('aria-expanded', 'false');
    }
    $$('.agent-profile-action-menu[open]').forEach(node => {
      if (!node.contains(event.target)) node.open = false;
    });
  });

  $('#logout').addEventListener('click',async()=>{
    if (!await askConfirmation('Sign out?', 'This removes project credentials imported into this browser session. Local projects and saved peer connections remain available.')) return;
    try { await request('/api/logout', { method: 'POST', body: {} }); }
    catch (error) { showError(error.message); return; }
    expireProjectSession(state.project?.project_id);
    clearUrlProject();
    try { await loadDashboardProjects(); } catch {}
    state.view = 'projects';
    render();
  });

  $('#open-audit-btn')?.addEventListener('click', async () => {
    if (!state.project) return;
    try {
      const events = await request(projectApi('events', { recent: 1 }));
      renderList($('#audit-list'), events || [], 'No recent events recorded.', renderAuditEventRow);
      openDialog('audit-dialog');
    } catch (err) {
      showError(err.message);
    }
  });

  $('#header-search-input')?.addEventListener('input', event => {
    clearTimeout(chatSearchTimer);
    if (state.view === 'projects') {
      state.projectQuery = event.target.value.trim();
      if (typeof renderProjectsView === 'function') renderProjectsView();
      return;
    }
    if (state.view === 'agents' && state.project) {
      state.agentQuery = event.target.value.trim();
      state.agentPane = 'roster';
      if (typeof renderAgentsView === 'function') renderAgentsView();
      return;
    }
    if (state.view === 'todos' && state.project) {
      state.taskFilters.query = event.target.value.trim();
      const generation = state.generation;
      const requestID = ++liveTaskSearchRequest;
      chatSearchTimer = setTimeout(async () => {
        if (generation !== state.generation || state.view !== 'todos' || requestID !== liveTaskSearchRequest) return;
        try {
          await loadTaskSearchOnly(generation, false, () => requestID === liveTaskSearchRequest);
          if (generation === state.generation && state.view === 'todos' && requestID === liveTaskSearchRequest && typeof renderTodosView === 'function') {
            renderTodosView();
          }
        } catch (error) {
          if (generation === state.generation) showError(error.message);
        }
      }, 250);
      return;
    }
    if (state.view === 'search' && state.project) {
      const query = event.target.value.trim();
      const generation = state.generation;
      const requestID = ++liveGlobalSearchRequest;
      state.searchQuery = query;
      state.searchResults = null;
      state.searchPending = Boolean(query);
      if (typeof renderSearchView === 'function') renderSearchView();
      chatSearchTimer = setTimeout(async () => {
        if (generation !== state.generation || state.view !== 'search' || requestID !== liveGlobalSearchRequest) return;
        try {
          await loadSearchRouteData(generation, () => requestID === liveGlobalSearchRequest);
          if (generation === state.generation && state.view === 'search' && requestID === liveGlobalSearchRequest && typeof renderSearchView === 'function') {
            renderSearchView();
          }
        } catch (error) {
          if (generation === state.generation) showError(error.message);
        }
      }, 250);
      return;
    }
    if (state.view !== 'chat' || !state.project) return;
    const query = event.target.value.trim();
    const generation = state.generation;
    chatSearchRequest += 1;
    if (!query) {
      if (typeof closeChatSearch === 'function') closeChatSearch();
      if (typeof renderChatView === 'function') renderChatView();
    } else {
      chatSearchTimer = setTimeout(() => {
        if (generation === state.generation && state.view === 'chat' && typeof searchChat === 'function') void searchChat(query);
      }, 300);
    }
  });

  $('#header-search-form')?.addEventListener('submit', event => {
    event.preventDefault();
    clearTimeout(chatSearchTimer);
    const query = $('#header-search-input').value.trim();
    if (state.view === 'projects') {
      state.projectQuery = query;
      if (typeof renderProjectsView === 'function') renderProjectsView();
      $('#local-projects-list button, #remote-projects-list button')?.focus();
      return;
    }
    if (!state.project) return;
    if (state.view === 'chat') {
      if (typeof searchChat === 'function') void searchChat(query);
      return;
    }
    if (state.view === 'todos') {
      state.taskFilters.query = query;
      if ($('#filter-query')) $('#filter-query').value = query;
      $('#todo-filters')?.requestSubmit();
      return;
    }
    if (state.view === 'agents') {
      state.agentQuery = query;
      state.agentPane = 'roster';
      if (typeof renderAgentsView === 'function') renderAgentsView();
      $('#agents-list button')?.focus();
      return;
    }
    const graph = state.view === 'search' && state.searchTab === 'graph';
    state.searchQuery = query;
    state.searchResults = null;
    state.graphify = null;
    state.searchPending = Boolean(state.searchQuery);
    state.searchTab = graph ? 'graph' : 'results';
    setView('search');
  });
}

function wireEvents() {
  wireShellEvents();
  if (typeof wireProjectDialogEvents === 'function') wireProjectDialogEvents();
  if (typeof wireChatEvents === 'function') wireChatEvents();
  if (typeof wirePlanEvents === 'function') wirePlanEvents();
  if (typeof wireTodoEvents === 'function') wireTodoEvents();
  if (typeof wireAgentEvents === 'function') wireAgentEvents();
  if (typeof wireSettingsEvents === 'function') wireSettingsEvents();
}

async function boot() {
  wireEvents();
  if (matchMedia('(max-width: 700px)').matches && $('#quick-task-disclosure')) {
    $('#quick-task-disclosure').open = false;
  }
  const initial = readUrlState();
  try {
    await loadDashboardProjects();
  } catch (err) {
    showError(err.message);
    render();
    return;
  }

  let selected = typeof projectFromLocation === 'function' ? projectFromLocation(initial) : null;
  if (!initial.projectID) {
    const firstReadyLocal = state.localProjects.find(project => project.status === 'ready');
    const needsRepair = state.localProjects.some(project => project.status !== 'ready');
    if (firstReadyLocal && typeof principalFromLocalProject === 'function') selected = principalFromLocalProject(firstReadyLocal);
    else if (!needsRepair && state.projects.length) selected = state.projects[0];
  }

  if (selected) {
    const defaultView = initial.explicitView || 'chat';
    selectProject(selected, { ...initial, view: defaultView, replaceUrl: true });
    await refresh();
  } else {
    state.view = 'projects';
    render();
  }

  if (typeof window?.addEventListener === 'function') {
    window.addEventListener('popstate', async () => {
      const previousView = state.view;
      const previousQuery = state.searchQuery;
      const previousSettings = state.settingsTab + '/' + state.accessSection;
      const previousRoom = typeof activeChatDraftKey === 'function' ? activeChatDraftKey() : '';
      const previousTaskQuery = JSON.stringify([state.todoScope, state.todoAgent, state.todoRevision, state.taskFilters]);
      const next = readUrlState();
      if ($('#header-search-input')) $('#header-search-input').value = next.searchQuery;
      const nextSelected = typeof projectFromLocation === 'function' ? projectFromLocation(next) : null;
      const current = state.project;
      const sameProject = nextSelected && current && nextSelected.project_id === current.project_id
        && (nextSelected.home_id || state.dashboardAccess.home_id) === (current.home_id || state.dashboardAccess.home_id);
      const view = next.explicitView || (nextSelected ? 'chat' : 'projects');
      if (!sameProject && (nextSelected || current)) {
        selectProject(nextSelected, { ...next, view, replaceUrl: true });
        await refresh();
        return;
      }
      state.view = view;
      if (previousView === 'chat') {
        if (typeof closeAgentDMs === 'function') closeAgentDMs();
        if (typeof closeChatSearch === 'function') closeChatSearch();
      }
      const tasksChanged = view === 'todos' && previousTaskQuery !== JSON.stringify([state.todoScope, state.todoAgent, state.todoRevision, state.taskFilters]);
      const searchChanged = state.view === 'search' && previousQuery !== next.searchQuery;
      const roomChanged = state.view === 'chat' && typeof activeChatDraftKey === 'function' && previousRoom !== activeChatDraftKey();
      if (searchChanged) {
        state.searchResults = null;
        state.graphify = null;
        state.searchPending = Boolean(next.searchQuery);
      }
      if (roomChanged) {
        state.messages = [];
        state.messagesOldestSeq = 0;
        state.messagesHasOlder = false;
        state.chatLoaded = false;
        state.mobilePane = 'thread';
      }
      if (state.view !== previousView || searchChanged || roomChanged || tasksChanged) {
        state.generation += 1;
        render();
        await refresh();
      } else {
        render();
        if (view === 'settings' && previousSettings !== state.settingsTab + '/' + state.accessSection) {
          await loadSettingsRouteData(state.generation);
        }
      }
    });
  }
}

document.addEventListener('DOMContentLoaded', boot);
