/* Reusable custom select dropdown matching the project-switcher visual design. */
const customSelectRegistry = new WeakMap();
let customSelectBooted = false;
let activeCustomSelect = null;

function isCustomSelectCandidate(selectEl) {
  if (!selectEl || selectEl.tagName !== 'SELECT') return false;
  if (selectEl.id === 'project-switcher' || selectEl.dataset.nativeSelect === 'true') return false;
  if (selectEl.multiple || (Number(selectEl.size) || 0) > 1) return false;
  return true;
}

function selectedSelectOption(selectEl) {
  const options = Array.from(selectEl.options || []);
  if (!options.length) return null;
  const idx = selectEl.selectedIndex;
  if (idx >= 0 && idx < options.length) return options[idx];
  return options.find(opt => opt.value === selectEl.value) || options[0];
}

function closeProjectSwitcherDropdown() {
  const menu = typeof $ === 'function' ? $('#project-switcher-menu') : document.getElementById('project-switcher-menu');
  const btn = typeof $ === 'function' ? $('#project-switcher-btn') : document.getElementById('project-switcher-btn');
  if (menu && !menu.hidden) menu.hidden = true;
  btn?.setAttribute('aria-expanded', 'false');
}

function closeCustomSelect(entry, { restoreFocus = false } = {}) {
  if (!entry) return;
  entry.menu.hidden = true;
  entry.btn.setAttribute('aria-expanded', 'false');
  if (activeCustomSelect === entry) activeCustomSelect = null;
  if (restoreFocus && !entry.btn.disabled) entry.btn.focus();
}

function closeAllCustomSelects(exceptEntry = null) {
  if (activeCustomSelect && activeCustomSelect !== exceptEntry) {
    closeCustomSelect(activeCustomSelect);
  }
}

function positionCustomSelectMenu(entry) {
  const rect = entry.btn.getBoundingClientRect();
  const vw = window.innerWidth || document.documentElement.clientWidth || 1024;
  const vh = window.innerHeight || document.documentElement.clientHeight || 768;
  const gap = 5;
  const edge = 8;

  entry.menu.style.minWidth = `${Math.max(Math.round(rect.width), 120)}px`;
  entry.menu.style.left = '0px';
  entry.menu.style.top = '0px';
  entry.optionsEl.style.maxHeight = '15rem';

  const spaceBelow = Math.max(0, vh - rect.bottom - edge - gap);
  const spaceAbove = Math.max(0, rect.top - edge - gap);
  const menuHeight = entry.menu.offsetHeight || 0;
  const openUpward = menuHeight > spaceBelow && spaceAbove > spaceBelow;
  const availHeight = openUpward ? spaceAbove : spaceBelow;
  const chromeHeight = Math.max(0, menuHeight - (entry.optionsEl.offsetHeight || 0));
  const cappedOptionsHeight = Math.max(88, Math.min(240, availHeight - chromeHeight));
  entry.optionsEl.style.maxHeight = `${Math.floor(cappedOptionsHeight)}px`;

  const finalRect = entry.menu.getBoundingClientRect();
  const top = openUpward
    ? Math.max(edge, rect.top - finalRect.height - gap)
    : Math.min(vh - finalRect.height - edge, rect.bottom + gap);
  const left = Math.max(edge, Math.min(rect.left, vw - finalRect.width - edge));

  entry.menu.style.top = `${Math.round(Math.max(edge, top))}px`;
  entry.menu.style.left = `${Math.round(left)}px`;
}

function renderCustomSelectOptions(entry) {
  const { selectEl, optionsEl } = entry;
  clearNode(optionsEl);

  const options = Array.from(selectEl.options || []);
  const selectedOpt = selectedSelectOption(selectEl);
  if (!options.length) {
    optionsEl.appendChild(createEl('p', 'muted small ui-select-empty', 'No options available.'));
    return;
  }

  options.forEach((opt, index) => {
    const isSelected = selectedOpt ? opt === selectedOpt : false;
    const itemBtn = createEl('button', `ui-select-option${isSelected ? ' is-selected' : ''}`);
    itemBtn.type = 'button';
    itemBtn.setAttribute('role', 'option');
    itemBtn.setAttribute('aria-selected', String(isSelected));
    itemBtn.disabled = Boolean(opt.disabled);
    itemBtn.dataset.optionIndex = String(index);
    itemBtn.appendChild(createEl('span', 'ui-select-option-label', opt.textContent || ''));
    if (isSelected) itemBtn.appendChild(svgIcon('check'));
    itemBtn.addEventListener('click', event => {
      event.preventDefault();
      event.stopPropagation();
      if (opt.disabled) return;
      const prevIndex = selectEl.selectedIndex;
      const prevValue = selectEl.value;
      const idxDesc = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'selectedIndex');
      if (idxDesc?.set) idxDesc.set.call(selectEl, index);
      else selectEl.selectedIndex = index;
      syncCustomSelectEntry(entry);
      closeCustomSelect(entry, { restoreFocus: true });
      if (prevIndex !== index || prevValue !== selectEl.value) {
        selectEl.dispatchEvent(new Event('change', { bubbles: true }));
      }
    });
    optionsEl.appendChild(itemBtn);
  });
}

function syncCustomSelectEntry(entry) {
  if (!entry) return;
  const { selectEl, wrapper, btn, labelEl } = entry;
  if (!selectEl.isConnected || !wrapper.isConnected) return;

  wrapper.hidden = Boolean(selectEl.hidden);
  wrapper.classList.toggle('ui-select-compact', selectEl.classList.contains('select-compact'));
  if (selectEl.id) wrapper.dataset.selectId = selectEl.id;
  else delete wrapper.dataset.selectId;

  const ariaLabel = selectEl.getAttribute('aria-label');
  if (ariaLabel) btn.setAttribute('aria-label', ariaLabel);
  else btn.removeAttribute('aria-label');

  const options = Array.from(selectEl.options || []);
  const selectedOpt = selectedSelectOption(selectEl);
  labelEl.textContent = selectedOpt ? (selectedOpt.textContent || '') : 'Select…';
  btn.disabled = Boolean(selectEl.disabled) || !options.length;
  wrapper.classList.toggle('is-disabled', btn.disabled);

  if (activeCustomSelect === entry && !entry.menu.hidden) {
    renderCustomSelectOptions(entry);
    positionCustomSelectMenu(entry);
  }
}

function openCustomSelect(entry, { focusTarget = 'selected' } = {}) {
  if (!entry || entry.btn.disabled || entry.wrapper.hidden) return;
  closeProjectSwitcherDropdown();
  closeAllCustomSelects(entry);
  syncCustomSelectEntry(entry);
  renderCustomSelectOptions(entry);
  entry.menu.hidden = false;
  entry.btn.setAttribute('aria-expanded', 'true');
  activeCustomSelect = entry;
  positionCustomSelectMenu(entry);

  const selected = entry.optionsEl.querySelector('.ui-select-option.is-selected:not(:disabled)');
  const enabled = Array.from(entry.optionsEl.querySelectorAll('.ui-select-option:not(:disabled)'));
  const target = focusTarget === 'last'
    ? enabled[enabled.length - 1]
    : focusTarget === 'first'
      ? enabled[0]
      : (selected || enabled[0]);
  target?.scrollIntoView?.({ block: 'nearest' });
  target?.focus?.();
}

function syncCustomSelect(selectEl) {
  const entry = customSelectRegistry.get(selectEl);
  if (entry) {
    syncCustomSelectEntry(entry);
  } else if (isCustomSelectCandidate(selectEl)) {
    enhanceCustomSelect(selectEl);
  }
}

function enhanceCustomSelect(selectEl) {
  if (!isCustomSelectCandidate(selectEl) || customSelectRegistry.has(selectEl) || !selectEl.parentNode) {
    const existing = customSelectRegistry.get(selectEl);
    if (existing) syncCustomSelectEntry(existing);
    return existing || null;
  }

  const wrapper = createEl('div', 'ui-select');
  const btn = createEl('button', 'ui-select-btn');
  btn.type = 'button';
  btn.setAttribute('aria-haspopup', 'listbox');
  btn.setAttribute('aria-expanded', 'false');

  const labelEl = createEl('span', 'ui-select-label');
  const caret = svgIcon('chevron-down');
  caret.classList.add('ui-select-caret');
  btn.append(labelEl, caret);

  const menu = createEl('div', 'ui-select-menu');
  menu.setAttribute('role', 'listbox');
  menu.hidden = true;
  const optionsEl = createEl('div', 'ui-select-options');
  menu.appendChild(optionsEl);

  const entry = { selectEl, wrapper, btn, labelEl, menu, optionsEl };
  customSelectRegistry.set(selectEl, entry);

  selectEl.parentNode.insertBefore(wrapper, selectEl);
  wrapper.append(btn, menu, selectEl);
  selectEl.classList.add('ui-select-native');
  selectEl.tabIndex = -1;
  selectEl.setAttribute('aria-hidden', 'true');

  btn.addEventListener('click', event => {
    event.preventDefault();
    event.stopPropagation();
    if (!menu.hidden) {
      closeCustomSelect(entry);
      return;
    }
    openCustomSelect(entry);
  });

  btn.addEventListener('keydown', event => {
    if (!['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    openCustomSelect(entry, { focusTarget: event.key === 'ArrowUp' ? 'last' : 'selected' });
  });

  menu.addEventListener('keydown', event => {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      closeCustomSelect(entry, { restoreFocus: true });
      return;
    }
    if (event.key === 'Tab') {
      closeCustomSelect(entry);
      return;
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const items = Array.from(optionsEl.querySelectorAll('.ui-select-option:not(:disabled)'));
    if (!items.length) return;
    event.preventDefault();
    event.stopPropagation();
    const idx = items.indexOf(document.activeElement);
    let nextIdx = 0;
    if (event.key === 'ArrowDown') nextIdx = idx < 0 ? 0 : (idx + 1) % items.length;
    else if (event.key === 'ArrowUp') nextIdx = idx <= 0 ? items.length - 1 : idx - 1;
    else if (event.key === 'Home') nextIdx = 0;
    else if (event.key === 'End') nextIdx = items.length - 1;
    items[nextIdx].scrollIntoView?.({ block: 'nearest' });
    items[nextIdx].focus();
  });

  selectEl.addEventListener('change', () => syncCustomSelectEntry(entry));
  selectEl.addEventListener('focus', () => {
    if (!btn.disabled && !wrapper.hidden) btn.focus();
  });

  if (typeof MutationObserver === 'function') {
    const selectObserver = new MutationObserver(() => syncCustomSelectEntry(entry));
    selectObserver.observe(selectEl, {
      childList: true,
      subtree: true,
      characterData: true,
      attributes: true,
      attributeFilter: ['hidden', 'disabled', 'aria-label', 'class']
    });
  }

  syncCustomSelectEntry(entry);
  return entry;
}

function patchNativeSelectDescriptors() {
  if (typeof HTMLSelectElement === 'undefined') return;
  ['value', 'selectedIndex'].forEach(prop => {
    const desc = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, prop);
    if (!desc?.set || !desc?.get) return;
    Object.defineProperty(HTMLSelectElement.prototype, prop, {
      configurable: true,
      enumerable: desc.enumerable,
      get() {
        return desc.get.call(this);
      },
      set(next) {
        desc.set.call(this, next);
        const entry = customSelectRegistry.get(this);
        if (entry) syncCustomSelectEntry(entry);
      }
    });
  });
}

function initCustomSelects(root = typeof document !== 'undefined' ? document : null) {
  if (!root || typeof document === 'undefined') return;

  if (!customSelectBooted) {
    customSelectBooted = true;
    patchNativeSelectDescriptors();

    document.addEventListener('click', event => {
      if (!activeCustomSelect) return;
      if (!activeCustomSelect.wrapper.contains(event.target)) {
        closeCustomSelect(activeCustomSelect);
      }
    }, true);

    document.addEventListener('keydown', event => {
      if (event.key === 'Escape' && activeCustomSelect) {
        event.preventDefault();
        event.stopPropagation();
        closeCustomSelect(activeCustomSelect, { restoreFocus: true });
      }
    }, true);

    document.addEventListener('reset', event => {
      const form = event.target;
      if (!form?.querySelectorAll) return;
      const selects = Array.from(form.querySelectorAll('select'));
      queueMicrotask(() => selects.forEach(syncCustomSelect));
    }, true);

    window?.addEventListener?.('resize', () => closeAllCustomSelects());
    window?.addEventListener?.('scroll', event => {
      if (!activeCustomSelect) return;
      if (activeCustomSelect.menu.contains(event.target)) return;
      closeCustomSelect(activeCustomSelect);
    }, true);

    if (typeof MutationObserver === 'function' && document.body) {
      let queued = false;
      const flushScan = () => {
        queued = false;
        document.querySelectorAll('select').forEach(enhanceCustomSelect);
      };
      const domObserver = new MutationObserver(mutations => {
        for (const mutation of mutations) {
          for (const node of mutation.addedNodes || []) {
            if (node.nodeType !== 1) continue;
            if (node.tagName === 'SELECT' || node.querySelector?.('select')) {
              if (!queued) {
                queued = true;
                queueMicrotask(flushScan);
              }
              return;
            }
          }
        }
      });
      domObserver.observe(document.body, { childList: true, subtree: true });
    }
  }

  root.querySelectorAll?.('select').forEach(enhanceCustomSelect);
}
