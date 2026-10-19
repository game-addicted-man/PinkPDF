// Small DOM helpers. User-provided text is always set with textContent.

export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k === 'style') el.setAttribute('style', v);
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (v === true) el.setAttribute(k, '');
      else el.setAttribute(k, v);
    }
  }
  append(el, kids);
  return el;
}

function append(el, kids) {
  for (const k of kids) {
    if (k == null || k === false) continue;
    if (Array.isArray(k)) append(el, k);
    else if (k instanceof Node) el.appendChild(k);
    else el.appendChild(document.createTextNode(String(k)));
  }
}

const ICONS = {
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M6 6l12 12M18 6 6 18"/>',
  left: '<path d="m14 6-6 6 6 6"/>',
  right: '<path d="m10 6 6 6-6 6"/>',
  trash: '<path d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  open: '<path d="M14 4h6v6M20 4l-9 9"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
  check: '<path d="m5 12 5 5L20 7"/>',
  checkCircle: '<circle cx="12" cy="12" r="9"/><path d="m8 12 3 3 5-6"/>',
  alert: '<circle cx="12" cy="12" r="9"/><path d="M12 7v6M12 16.5v.5"/>',
  lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  file: '<path d="M6 3h8l5 5v12a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M14 3v5h5"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="m21 16-5-5-9 9"/>',
  scissors: '<circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M8.6 7.6 20 18M8.6 16.4 20 6"/>',
  sort: '<path d="M7 4v16M3 16l4 4 4-4M14 6h7M14 12h5M14 18h3"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m11 12 9-9M17 6l3 3M14 9l2 2"/>',
  minus: '<path d="M5 12h14"/>',
};

export function icon(name, cls = 'i') {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('class', cls);
  svg.setAttribute('aria-hidden', 'true');
  svg.innerHTML = ICONS[name] || '';
  return svg;
}

export function iconButton(name, label, onclick, cls = 'btn ghost icon small') {
  return h('button', { class: cls, type: 'button', title: label, 'aria-label': label, onclick }, icon(name));
}

export function fmtBytes(n) {
  if (n == null) return '';
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)} ${u[i]}`;
}

export function toast(msg, kind = '') {
  const box = document.getElementById('toasts');
  const t = h('div', { class: `toast ${kind}` }, msg);
  box.appendChild(t);
  setTimeout(() => t.remove(), kind === 'err' ? 6500 : 3800);
}

// ---- modal dialogs ----
const modal = () => document.getElementById('modal');

// One <dialog> serves every modal, so prompts take turns: two locked PDFs
// added together ask for their passwords one after the other, and a prompt
// that comes up while the settings are open waits until they close.
let turn = Promise.resolve();

function whenClosed(dlg) {
  return dlg.open ? new Promise((r) => dlg.addEventListener('close', () => r(), { once: true })) : null;
}

export async function prompt(opts) {
  let done;
  const mine = new Promise((r) => { done = r; });
  const before = turn;
  turn = turn.then(() => mine);
  await before;
  try {
    await whenClosed(modal());
    return await promptNow(opts);
  } finally {
    done();
  }
}

// Resolves with the entered text, or null when canceled. It resolves from
// the dialog's close event (which fires a moment after close()), so the next
// prompt never receives the previous one's close.
function promptNow({ title, message, label, type = 'text', okText = '확인', value = '', check }) {
  return new Promise((resolve) => {
    const dlg = modal();
    let result = null;
    const input = h('input', { class: 'input', type, value, 'aria-label': label || title, autocomplete: 'off' });
    const err = h('div', { class: 'err' });
    const form = h('form', { method: 'dialog' },
      h('h2', { text: title }),
      message ? h('p', { text: message }) : null,
      input, err,
      h('div', { class: 'actions' },
        h('button', { class: 'btn', type: 'button', onclick: () => dlg.close() }, '취소'),
        h('button', { class: 'btn ok', type: 'submit' }, okText)));
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (check) {
        const msg = await check(input.value);
        if (msg) { err.textContent = msg; input.select(); return; }
      }
      result = input.value;
      dlg.close();
    });
    dlg.replaceChildren(form);
    dlg.onclose = () => {
      dlg.onclose = null;
      resolve(result);
    };
    dlg.className = 'modal';
    dlg.showModal();
    input.focus();
  });
}

export function showModal(content, wide = false) {
  const dlg = modal();
  dlg.replaceChildren(content);
  dlg.className = wide ? 'modal wide' : 'modal';
  dlg.onclose = null;
  dlg.showModal();
  return dlg;
}

export function closeModal() { modal().close(); }

// Parses "1-3, 5, 8-" like the Go side does; returns sorted unique pages or throws.
export function parsePages(expr, count) {
  const norm = expr.replace(/[~∼–—−～]/g, '-').replace(/[，、;]/g, ',').replace(/끝|마지막/g, 'end')
    .replace(/\s*-\s*/g, '-').replace(/쪽/g, '');
  const parts = norm.split(/[,\s]+/).filter(Boolean);
  if (!parts.length) throw new Error('페이지 범위를 입력해 주세요 (예: 1-3, 5, 8-)');
  const out = [];
  const num = (s, def) => {
    if (s === '') return def;
    if (s === 'end') return count;
    if (!/^\d+$/.test(s)) throw new Error(`페이지 범위를 이해할 수 없습니다: ${expr}`);
    const n = parseInt(s, 10);
    if (n < 1 || n > count) throw new Error(`${n}쪽은 없습니다 (전체 ${count}쪽)`);
    return n;
  };
  for (const p of parts) {
    const m = p.split('-');
    if (m.length > 2) throw new Error(`페이지 범위를 이해할 수 없습니다: ${p}`);
    let a, b;
    if (m.length === 2) { a = num(m[0], 1); b = num(m[1], count); } else { a = b = num(m[0]); }
    const group = [];
    if (a <= b) for (let i = a; i <= b; i++) group.push(i);
    else for (let i = a; i >= b; i--) group.push(i);
    out.push(group);
  }
  return out;
}

export function formatPages(pages) {
  const s = [...pages].sort((a, b) => a - b);
  const out = [];
  for (let i = 0; i < s.length;) {
    let j = i;
    while (j + 1 < s.length && s[j + 1] === s[j] + 1) j++;
    out.push(j > i ? `${s[i]}-${s[j]}` : `${s[i]}`);
    i = j + 1;
  }
  return out.join(', ');
}

export function baseName(name) {
  const i = name.lastIndexOf('.');
  return i > 0 ? name.slice(0, i) : name;
}
