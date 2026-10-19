import { api, runJob, cancelJob, openPath, revealPath, writeOutput, fileURL, token } from './api.js';
import { h, icon, iconButton, fmtBytes, toast, parsePages, formatPages, baseName, prompt } from './ui.js';
import { loadDoc, fillThumb, renderPageBlob, pageText, PasswordCanceled } from './pdfview.js';

const BROWSER_IMAGE = /\.(jpe?g|jfif|jpe|png|gif|bmp|webp)$/i; // what the window itself can preview

// =====================================================================
// Base class: a tool with a list of files, options and one action.
// =====================================================================
class Tool {
  constructor(app, cfg) {
    this.app = app;
    Object.assign(this, cfg);
    this.files = [];
    this.job = null;
    this.busy = null;
    this.raf = 0;
  }

  accepts(f) { return this.kinds.includes(f.kind); }

  acceptAttr() {
    const s = this.app.state;
    const list = [];
    if (this.kinds.includes('pdf')) list.push('.pdf');
    if (this.kinds.includes('image')) list.push(...s.imageExts);
    if (this.kinds.includes('document')) list.push(...s.documentExts);
    return list.join(',');
  }

  get mounted() { return this.app.current === this && this.desk; }

  mount(desk, panel) {
    this.desk = desk;
    this.panel = panel;
    this.render();
  }

  addFiles(list) {
    if (!this.multiple) {
      if (this.files.length) this.app.forget(this.files);
      this.files = [list[0]];
      if (list.length > 1) toast('이 도구는 파일을 하나씩 처리합니다. 첫 번째 파일만 넣었습니다.');
    } else {
      this.files.push(...list);
    }
    this.job = null;
    this.filesChanged?.();
    this.render();
    for (const f of list) if (f.kind === 'pdf') this.probe(f);
  }

  probe(f) {
    loadDoc(f).then((doc) => {
      f.pages = doc.numPages;
      f.error = null;
      this.scheduleRender();
    }, (err) => {
      if (err instanceof PasswordCanceled) f.locked = true;
      else f.error = err.message;
      this.scheduleRender();
    });
  }

  async unlock(f) {
    f.locked = false;
    f.error = null;
    this.probe(f);
  }

  removeFile(f) {
    this.files = this.files.filter((x) => x !== f);
    this.app.forget([f]);
    this.job = null;
    this.filesChanged?.();
    this.render();
  }

  clearFiles() {
    this.app.forget(this.files);
    this.files = [];
    this.job = null;
    this.filesChanged?.();
    this.render();
  }

  move(from, to) {
    if (to < 0 || to >= this.files.length || from === to) return;
    const [f] = this.files.splice(from, 1);
    this.files.splice(to, 0, f);
    this.render();
  }

  setBusy(msg) {
    this.busy = msg;
    if (this.mounted) this.renderDesk();
  }

  scheduleRender() {
    if (!this.mounted || this.raf) return;
    this.raf = requestAnimationFrame(() => { this.raf = 0; this.render(); });
  }

  render() {
    if (!this.mounted) return;
    keepFocus(() => {
      this.renderDesk();
      this.renderPanel();
    });
  }

  renderDesk() {
    const d = this.desk;
    if (this.busy) {
      d.replaceChildren(h('div', { class: 'dropsheet' }, h('h2', { text: this.busy })));
      return;
    }
    if (!this.files.length) {
      d.replaceChildren(dropSheet(this));
      return;
    }
    d.replaceChildren(...this.deskContent());
  }

  renderPanel() {
    if (!this.mounted) return;
    const body = h('div', { class: 'panel-body' }, ...this.options());
    if (this.job && this.job.state !== 'running') body.appendChild(this.resultsView());
    const foot = h('div', { class: 'panel-foot' }, this.where(), this.footer());
    keepFocus(() => this.panel.replaceChildren(body, foot));
    if (this.job && this.job.state !== 'running' && this.scrollToResults) {
      this.scrollToResults = false;
      body.scrollTop = body.scrollHeight;
    }
  }

  // ----- shared pieces -----
  toolbar(extra = []) {
    return h('div', { class: 'toolbar' },
      h('span', { class: 'count', text: this.countText() }),
      ...extra,
      h('button', { class: 'btn', type: 'button', onclick: () => this.app.pick(this) }, icon('plus'), '파일 추가'),
      h('button', { class: 'btn ghost', type: 'button', onclick: () => this.clearFiles() }, icon('trash'), '모두 빼기'));
  }

  countText() {
    const n = this.files.length;
    const size = fmtBytes(this.files.reduce((s, f) => s + f.size, 0));
    if (this.kinds.length === 1 && this.kinds[0] === 'image') return `이미지 ${n}장, ${size}`;
    if (this.kinds.length === 1 && this.kinds[0] === 'document') return `문서 ${n}개, ${size}`;
    const pages = this.files.reduce((s, f) => s + (f.kind === 'pdf' ? (f.pages || 0) : 1), 0);
    const known = this.files.every((f) => f.kind !== 'pdf' || f.pages);
    return `파일 ${n}개${known && pages ? `, 모두 ${pages}쪽` : ''}, ${size}`;
  }

  where() {
    const [text, full] = this.app.where(this.files[0]);
    return h('div', { class: 'where' }, icon('folder'),
      h('span', { title: full || text, text: `저장 위치: ${text}` }),
      h('button', { type: 'button', onclick: () => this.app.select('settings') }, '바꾸기'));
  }

  footer() {
    const j = this.job;
    if (j && j.state === 'running') {
      const pct = Math.round((j.progress || 0) * 100);
      this.progressEl = h('div', { class: 'progress' },
        h('div', { class: 'bar' }, h('i', { style: `width:${pct}%` })),
        h('div', { class: 'msg' }, h('span', { text: j.message || '처리 중' }), h('span', { class: 'num', text: `${pct}%` })));
      return h('div', {}, this.progressEl,
        h('button', { class: 'btn', type: 'button', style: 'width:100%', onclick: () => this.cancel() }, icon('stop'), '그만하기'));
    }
    const problem = this.problem();
    return h('div', {},
      problem ? h('p', { class: 'help', style: 'margin:0 0 10px;font-size:12px;color:var(--ink-3)', text: problem }) : null,
      h('button', { class: 'btn primary', type: 'button', disabled: !!problem, onclick: () => this.run() }, this.actionLabel));
  }

  updateProgress() {
    const j = this.job;
    if (!this.mounted || !this.progressEl || !this.progressEl.isConnected) { this.renderPanel(); return; }
    const pct = Math.round((j.progress || 0) * 100);
    this.progressEl.querySelector('.bar i').style.width = `${pct}%`;
    const spans = this.progressEl.querySelectorAll('.msg span');
    spans[0].textContent = j.message || '처리 중';
    spans[1].textContent = `${pct}%`;
  }

  problem() {
    if (!this.files.length) return '먼저 파일을 추가해 주세요.';
    const locked = this.files.find((f) => f.locked);
    if (locked) return `"${locked.name}"의 암호를 입력해 주세요.`;
    const broken = this.files.find((f) => f.kind === 'pdf' && f.error);
    if (broken) return `"${broken.name}"은(는) 열 수 없는 PDF입니다. 목록에서 빼 주세요.`;
    return null;
  }

  async startJob(kind, body) {
    this.job = { state: 'running', progress: 0, message: '시작하는 중' };
    this.renderPanel();
    try {
      this.job = await runJob(kind, body, (j) => { this.job = j; this.updateProgress(); });
    } catch (e) {
      this.job = { state: 'error', error: e.message, results: [] };
    }
    this.scrollToResults = true;
    this.render();
  }

  cancel() {
    if (this.job?.id) cancelJob(this.job.id);
    this.cancelLocal = true;
  }

  resultsView() {
    const j = this.job;
    const res = j.results || [];
    const ok = res.filter((r) => !r.error);
    const bad = res.filter((r) => r.error);
    const box = h('div', { class: 'results' });
    if (j.state === 'canceled') {
      box.appendChild(h('h3', { class: 'bad' }, icon('alert'), '작업을 그만두었습니다'));
    } else if (j.state === 'error') {
      // some files may be done before the error; say so and show the error
      box.appendChild(h('h3', { class: 'bad' }, icon('alert'), ok.length ? '일부만 마쳤습니다' : '작업을 마치지 못했습니다'));
      if (j.error && !bad.some((r) => r.error === j.error)) box.appendChild(h('div', { class: 'res err' }, h('div', { class: 's', style: 'margin:0', text: j.error })));
    } else {
      const nFiles = ok.filter((r) => !r.isDir).length;
      box.appendChild(h('h3', {}, icon('checkCircle'), nFiles > 1 ? `완료했습니다 (${nFiles}개 파일)` : '완료했습니다'));
    }
    const dirs = ok.filter((r) => r.isDir);
    const files = ok.filter((r) => !r.isDir);
    const shownFiles = files.slice(0, dirs.length ? 5 : 40);
    for (const r of [...dirs, ...shownFiles]) box.appendChild(resultItem(r));
    const hidden = files.length - shownFiles.length;
    if (hidden > 0) box.appendChild(h('div', { class: 'more', text: `나머지 ${hidden}개 파일은 폴더에서 확인하세요.` }));
    for (const r of bad) box.appendChild(this.errorItem(r));
    return box;
  }

  errorItem(r) {
    const item = h('div', { class: 'res err' },
      h('div', { class: 'n' }, icon('alert'), h('span', { text: r.source || r.name })),
      h('div', { class: 's', text: r.error }));
    if (r.needPassword) {
      const f = this.files.find((x) => x.id === r.sourceId);
      if (f) {
        item.appendChild(h('div', { class: 'a' }, h('button', {
          class: 'btn small', type: 'button', onclick: () => askBackendPassword(f).then((ok) => { if (ok) { this.job = null; this.render(); } }),
        }, icon('key'), '암호 입력')));
      }
    }
    return item;
  }
}

// keeps keyboard focus and caret across re-renders (elements carry data-key)
function keepFocus(fn) {
  const a = document.activeElement;
  const key = a?.dataset?.key;
  const sel = key && 'selectionStart' in a ? [a.selectionStart, a.selectionEnd] : null;
  fn();
  if (!key) return;
  const b = document.querySelector(`[data-key="${CSS.escape(key)}"]`);
  if (b && b !== document.activeElement) {
    b.focus({ preventScroll: true });
    if (sel && b.setSelectionRange) try { b.setSelectionRange(sel[0], sel[1]); } catch { /* not a text input */ }
  }
}

async function askBackendPassword(f) {
  const pw = await prompt({
    title: '암호로 보호된 PDF', message: `"${f.name}" 파일의 암호를 입력해 주세요.`, type: 'password', okText: '확인',
    check: async (v) => {
      try { await api(`/api/files/${f.id}/password`, { password: v }); return null; } catch (e) { return e.message; }
    },
  });
  if (pw == null) return false;
  f.password = pw;
  f.passwordSent = pw;
  f.locked = false;
  return true;
}

function resultItem(r) {
  const sub = [];
  if (r.isDir) sub.push(r.note || '폴더');
  else if (r.inSize && r.size) {
    const saved = r.inSize - r.size;
    const pct = Math.round((saved / r.inSize) * 100);
    sub.push(h('span', {}, `${fmtBytes(r.inSize)} → ${fmtBytes(r.size)} `, saved > 0 ? h('span', { class: 'good', text: `(${pct}% 줄었음)` }) : null));
  } else {
    if (r.size) sub.push(fmtBytes(r.size));
    if (r.note) sub.push(r.note);
  }
  if (r.inSize && r.note) sub.push(h('div', { text: r.note }));
  const actions = r.isDir
    ? [h('button', { class: 'btn small', type: 'button', onclick: () => openPath(r.path).catch((e) => toast(e.message, 'err')) }, icon('folder'), '폴더 열기')]
    : [
      h('button', { class: 'btn small', type: 'button', onclick: () => openPath(r.path).catch((e) => toast(e.message, 'err')) }, icon('open'), '열기'),
      h('button', { class: 'btn small ghost', type: 'button', onclick: () => revealPath(r.path).catch((e) => toast(e.message, 'err')) }, icon('folder'), '폴더에서 보기'),
    ];
  return h('div', { class: 'res' },
    h('div', { class: 'n' }, icon(r.isDir ? 'folder' : 'file'), h('span', { text: r.name, title: r.path })),
    h('div', { class: 's' }, ...sub.flatMap((x, i) => (i ? [', ', x] : [x]))),
    h('div', { class: 'a' }, ...actions));
}

function dropSheet(tool) {
  const el = h('div', { class: 'dropsheet' },
    h('div', { class: 'stack', 'aria-hidden': 'true' }, h('span'), h('span'), h('span', {}, h('i'), h('i'), h('i'), h('i'))),
    h('h2', { text: tool.emptyTitle }),
    h('p', { text: tool.emptyHint }),
    h('button', { class: 'btn', type: 'button', onclick: () => tool.app.pick(tool) }, icon('plus'), '파일 선택'));
  el.addEventListener('dragover', () => el.classList.add('over'));
  el.addEventListener('dragleave', () => el.classList.remove('over'));
  el.addEventListener('drop', () => el.classList.remove('over'));
  return el;
}

function fileSub(f) {
  if (f.kind === 'pdf') {
    if (f.locked) return null;
    if (f.error) return h('div', { class: 'sub warn', text: '열 수 없는 PDF입니다' });
    return h('div', { class: 'sub' }, f.pages ? `${f.pages}쪽, ${fmtBytes(f.size)}` : fmtBytes(f.size));
  }
  return h('div', { class: 'sub', text: fmtBytes(f.size) });
}

function lockButton(tool, f) {
  return h('button', { class: 'lock', type: 'button', onclick: () => tool.unlock(f) }, icon('lock'), '암호 입력');
}

// paper thumbnail for a file (first page of a PDF, the image itself, or a type badge)
function paperFor(f, boxW, boxH, layout) {
  const paper = h('div', { class: 'paper' });
  const page = h('div', { class: 'page loading' });
  paper.appendChild(page);
  if (f.kind === 'pdf') {
    if (f.locked || f.error) {
      page.classList.remove('loading');
      page.style.cssText = 'width:76%;height:100%;color:#8a949e';
      page.appendChild(icon(f.locked ? 'lock' : 'alert'));
    } else {
      if (f.pages > 1) paper.classList.add('multi');
      fillThumb(page, f, 1, boxW, boxH, {
        onSize: (c) => {
          paper.style.setProperty('--pw', c.style.width);
          paper.style.setProperty('--ph', c.style.height);
        },
      });
    }
  } else if (f.kind === 'image') {
    layImage(page, f, layout || { pageSize: 'a4', orientation: 'auto', margin: 0 }, boxW, boxH);
  } else {
    page.className = 'page badge';
    page.style.background = docColor(f.ext);
    page.append(icon('file'), (f.ext || '').replace('.', '').toUpperCase());
  }
  return paper;
}

const PAGE_MM = { a3: [297, 420], a4: [210, 297], a5: [148, 210], b5: [182, 257], letter: [215.9, 279.4], legal: [215.9, 355.6] };

export function imageSrc(f, size = 360) {
  return BROWSER_IMAGE.test(f.name) ? fileURL(f.id) : `/api/files/${encodeURIComponent(f.id)}/thumb?s=${size}&t=${encodeURIComponent(token)}`;
}

// Shows an image the way it will sit on its PDF page (paper size, orientation, margin).
function layImage(sheet, f, layout, boxW, boxH) {
  const img = h('img', { alt: '', decoding: 'async', src: imageSrc(f), draggable: 'false' });
  img.addEventListener('load', () => {
    const iw = img.naturalWidth || 1, ih = img.naturalHeight || 1;
    let pw, ph;
    if (layout.pageSize === 'image') {
      pw = (iw / 96) * 25.4 + 2 * layout.margin;
      ph = (ih / 96) * 25.4 + 2 * layout.margin;
    } else {
      [pw, ph] = PAGE_MM[layout.pageSize] || PAGE_MM.a4;
      const land = layout.orientation === 'landscape' || (layout.orientation === 'auto' && iw > ih);
      if (land) [pw, ph] = [ph, pw];
    }
    const s = Math.min(boxW / pw, boxH / ph);
    sheet.className = 'page imgpage';
    sheet.style.cssText = `width:${pw * s}px;height:${ph * s}px;padding:${layout.margin * s}px`;
    sheet.replaceChildren(img);
  });
  img.addEventListener('error', () => {
    sheet.className = 'page badge';
    sheet.style.background = '#6b7480';
    sheet.replaceChildren(icon('image'), (f.ext || '').replace('.', '').toUpperCase());
  });
}

function docColor(ext) {
  if (/^\.(docx?|docm|dotx?|rtf|odt|txt)$/.test(ext)) return '#2b579a';
  if (/^\.(xlsx?|xlsm|xlsb|csv|ods)$/.test(ext)) return '#217346';
  if (/^\.(pptx?|pptm|ppsx?|odp)$/.test(ext)) return '#c43e1c';
  if (/^\.(hwpx?)$/.test(ext)) return '#1f78c8';
  return '#6b7480';
}

// ----- sheet cards with drag & drop ordering -----
function sheetCards(tool, { ranges = false, layout = null } = {}) {
  const grid = h('div', { class: 'sheets' });
  tool.files.forEach((f, i) => {
    const paper = paperFor(f, 150, 196, layout);
    paper.prepend(h('span', { class: 'badge-order', text: String(i + 1) }));
    const card = h('div', { class: 'card', draggable: 'true', dataset: { index: String(i) } },
      paper,
      h('div', { class: 'meta' }, h('div', { class: 'name', text: f.name, title: f.name }), f.locked ? lockButton(tool, f) : fileSub(f)));
    if (ranges && f.kind === 'pdf' && !f.error) {
      const inp = h('input', {
        class: 'range', type: 'text', value: f.range || '', placeholder: '모든 쪽 (예: 1-3, 5)',
        'aria-label': `${f.name} 쪽 범위`, dataset: { key: `range-${f.id}` }, spellcheck: 'false',
      });
      const check = () => {
        f.range = inp.value;
        let bad = false;
        if (f.range.trim() && f.pages) { try { parsePages(f.range, f.pages); } catch { bad = true; } }
        inp.classList.toggle('bad', bad);
        inp.title = bad ? '쪽 범위를 확인해 주세요' : '';
      };
      check();
      inp.addEventListener('input', () => { check(); tool.renderPanel(); });
      inp.addEventListener('mousedown', (e) => e.stopPropagation());
      card.appendChild(inp);
    }
    card.appendChild(h('div', { class: 'ops' },
      iconButton('left', '앞으로', () => tool.move(i, i - 1)),
      iconButton('right', '뒤로', () => tool.move(i, i + 1)),
      h('span', { class: 'spacer' }),
      iconButton('x', '목록에서 빼기', () => tool.removeFile(f))));
    card.addEventListener('dragstart', (e) => {
      if (e.target.closest('input')) { e.preventDefault(); return; }
      e.dataTransfer.setData('text/x-pinkpdf-index', String(i));
      e.dataTransfer.effectAllowed = 'move';
      card.classList.add('dragging');
    });
    card.addEventListener('dragend', () => card.classList.remove('dragging'));
    card.addEventListener('dragover', (e) => {
      if (![...e.dataTransfer.types].includes('text/x-pinkpdf-index')) return;
      e.preventDefault();
      grid.querySelectorAll('.drop-before').forEach((c) => c.classList.remove('drop-before'));
      card.classList.add('drop-before');
    });
    card.addEventListener('dragleave', () => card.classList.remove('drop-before'));
    card.addEventListener('drop', (e) => {
      const from = e.dataTransfer.getData('text/x-pinkpdf-index');
      if (from === '') return;
      e.preventDefault();
      e.stopPropagation();
      const a = parseInt(from, 10);
      const to = a < i ? i - 1 : i;
      tool.move(a, to);
    });
    grid.appendChild(card);
  });
  // drop after the last card
  grid.addEventListener('dragover', (e) => {
    if ([...e.dataTransfer.types].includes('text/x-pinkpdf-index') && e.target === grid) e.preventDefault();
  });
  grid.addEventListener('drop', (e) => {
    const from = e.dataTransfer.getData('text/x-pinkpdf-index');
    if (from === '' || e.target !== grid) return;
    e.preventDefault();
    tool.move(parseInt(from, 10), tool.files.length - 1);
  });
  return grid;
}

// ----- list rows -----
function fileRows(tool, extra) {
  return h('div', { class: 'rows' }, ...tool.files.map((f) => {
    const thumb = h('div', { class: 'thumb' });
    if (f.kind === 'pdf' && !f.locked && !f.error) {
      const page = h('div', { class: 'page loading', style: 'width:42px;height:54px' });
      thumb.appendChild(page);
      fillThumb(page, f, 1, 52, 60, { onSize: () => { page.style.width = ''; page.style.height = ''; } });
    } else if (f.kind === 'pdf') {
      thumb.appendChild(h('div', { class: 'ph' }, icon(f.locked ? 'lock' : 'alert')));
    } else {
      thumb.appendChild(h('div', { class: 'ph doc', style: `background:${docColor(f.ext)}`, text: (f.ext || '').replace('.', '').toUpperCase() }));
    }
    const sub = h('div', { class: 'sub' });
    if (f.locked) sub.appendChild(lockButton(tool, f));
    else if (f.error) sub.appendChild(h('span', { class: 'bad', text: '열 수 없는 PDF입니다' }));
    else {
      if (f.kind === 'pdf' && f.pages) sub.appendChild(h('span', { text: `${f.pages}쪽` }));
      sub.appendChild(h('span', { text: fmtBytes(f.size) }));
    }
    const more = extra ? extra(f) : null;
    return h('div', { class: 'row' }, thumb,
      h('div', { style: 'min-width:0' }, h('div', { class: 'name', text: f.name }), sub, more),
      h('div', { class: 'ops' }, iconButton('x', '목록에서 빼기', () => tool.removeFile(f))));
  }));
}

function radioChoice(name, options, value, onchange) {
  return h('div', { class: 'choice', role: 'radiogroup' }, ...options.map((o) => {
    const input = h('input', { type: 'radio', name, value: o.value, checked: value === o.value, onchange: () => onchange(o.value) });
    return h('label', { class: 'opt' }, input, h('span', { class: 't', text: o.title }),
      o.desc ? h('span', { class: 'd', text: o.desc }) : null,
      o.extra && value === o.value ? h('span', { class: 'extra' }, o.extra()) : null);
  }));
}

function seg(name, options, value, onchange, label) {
  return h('div', { class: 'seg', role: 'radiogroup', 'aria-label': label }, ...options.map((o) => h('label', {},
    h('input', { type: 'radio', name, value: o.value, checked: value === o.value, onchange: () => onchange(o.value) }),
    o.title)));
}

function field(label, ...content) {
  return h('div', { class: 'field' }, label ? h('span', { class: 'label', text: label }) : null, ...content);
}

function help(text) { return h('p', { class: 'help', text }); }

// =====================================================================
// 합치기
// =====================================================================
class MergeTool extends Tool {
  constructor(app) {
    super(app, {
      id: 'merge', title: '합치기', desc: '여러 PDF와 이미지를 원하는 순서대로 한 PDF로 묶습니다. 파일마다 필요한 쪽만 고를 수도 있습니다.',
      kinds: ['pdf', 'image'], filter: 'pdfimage', multiple: true, actionLabel: 'PDF 합치기',
      emptyTitle: '합칠 PDF나 이미지를 여기로 끌어오세요', emptyHint: '여러 파일을 한꺼번에 고를 수 있습니다. 순서는 나중에 끌어서 바꿀 수 있어요.',
    });
    this.bookmarks = true;
    this.outName = '';
  }

  deskContent() {
    return [
      this.toolbar([h('button', {
        class: 'btn ghost', type: 'button',
        onclick: () => { this.files.sort((a, b) => a.name.localeCompare(b.name, 'ko', { numeric: true })); this.render(); },
      }, icon('sort'), '이름순')]),
      sheetCards(this, { ranges: true }),
    ];
  }

  options() {
    const pdfs = this.files.filter((f) => f.kind === 'pdf').length;
    const imgs = this.files.length - pdfs;
    let total = 0, known = true;
    for (const f of this.files) {
      if (f.kind !== 'pdf') { total += 1; continue; }
      if (!f.pages) { known = false; continue; }
      if (f.range && f.range.trim()) {
        try { total += parsePages(f.range, f.pages).flat().length; } catch { known = false; }
      } else total += f.pages;
    }
    const parts = [];
    if (pdfs) parts.push(`PDF ${pdfs}개`);
    if (imgs) parts.push(`이미지 ${imgs}개`);
    return [
      this.files.length ? h('div', { class: 'summary' }, `${parts.join('와 ')}를 순서대로 합칩니다.`, known ? h('b', { text: ` 모두 ${total}쪽` }) : null) : null,
      field('저장할 파일 이름',
        h('input', {
          class: 'input', type: 'text', value: this.outName, dataset: { key: 'merge-name' }, spellcheck: 'false',
          placeholder: this.files[0] ? `${baseName(this.files[0].name)}_합본` : '합본',
          oninput: (e) => { this.outName = e.target.value; },
        }),
        help('같은 이름의 파일이 있으면 번호를 붙여 따로 저장합니다.')),
      field(null, h('label', { class: 'check' },
        h('input', { type: 'checkbox', checked: this.bookmarks, onchange: (e) => { this.bookmarks = e.target.checked; } }),
        h('span', {}, '파일마다 책갈피 만들기')),
      help('PDF 뷰어의 책갈피에서 원래 파일 위치로 바로 이동할 수 있습니다.')),
    ];
  }

  problem() {
    const p = super.problem();
    if (p) return p;
    if (this.files.length < 2 && !(this.files[0]?.range || '').trim()) return '합치려면 파일을 2개 이상 넣어 주세요.';
    for (const f of this.files) {
      if (f.kind === 'pdf' && f.range && f.range.trim() && f.pages) {
        try { parsePages(f.range, f.pages); } catch (e) { return `${f.name}: ${e.message}`; }
      }
    }
    return null;
  }

  run() {
    this.startJob('merge', {
      items: this.files.map((f) => ({ id: f.id, pages: f.kind === 'pdf' ? (f.range || '') : '' })),
      bookmarks: this.bookmarks, outName: this.outName,
    });
  }
}

// =====================================================================
// 나누기
// =====================================================================
class SplitTool extends Tool {
  constructor(app) {
    super(app, {
      id: 'split', title: '나누기', desc: 'PDF 하나를 여러 파일로 나누거나, 필요한 쪽만 골라 새 PDF로 뽑아냅니다.',
      kinds: ['pdf'], filter: 'pdf', multiple: false, actionLabel: 'PDF 나누기',
      emptyTitle: '나눌 PDF를 여기로 끌어오세요', emptyHint: 'PDF 파일 하나를 넣으면 모든 쪽을 미리 보여 드립니다.',
    });
    this.mode = 'ranges';
    this.every = 2;
    this.ranges = '';
    this.selection = new Set();
    this.gridTimer = 0;
  }

  filesChanged() {
    this.selection = new Set();
    this.ranges = '';
  }

  get file() { return this.files[0]; }

  groups() {
    const f = this.file;
    if (!f || !f.pages) return { groups: [], error: null };
    const n = f.pages;
    try {
      switch (this.mode) {
        case 'each': return { groups: Array.from({ length: n }, (_, i) => [i + 1]) };
        case 'every': {
          const k = parseInt(this.every, 10);
          if (!(k >= 1)) throw new Error('몇 쪽씩 나눌지 1 이상의 숫자로 적어 주세요.');
          const g = [];
          for (let s = 1; s <= n; s += k) g.push(Array.from({ length: Math.min(k, n - s + 1) }, (_, i) => s + i));
          return { groups: g };
        }
        case 'ranges':
          if (!this.ranges.trim()) return { groups: [], error: '나눌 범위를 적어 주세요. 예: 1-3, 4-8, 9-' };
          return { groups: parsePages(this.ranges, n) };
        case 'extract': {
          if (!this.ranges.trim()) return { groups: [], error: '뽑아낼 쪽을 적거나 왼쪽에서 쪽을 눌러 고르세요.' };
          const seen = new Set(), list = [];
          for (const p of parsePages(this.ranges, n).flat()) if (!seen.has(p)) { seen.add(p); list.push(p); }
          return { groups: [list] };
        }
      }
    } catch (e) {
      return { groups: [], error: e.message };
    }
    return { groups: [] };
  }

  deskContent() {
    const f = this.file;
    const bar = h('div', { class: 'docbar' },
      h('div', { class: 'grow' }, h('div', { class: 'name', text: f.name }),
        h('div', { class: 'sub', text: f.pages ? `${f.pages}쪽, ${fmtBytes(f.size)}` : (f.locked ? '암호가 필요합니다' : f.error ? f.error : '쪽을 불러오는 중…') })),
      f.locked ? lockButton(this, f) : null,
      h('button', { class: 'btn', type: 'button', onclick: () => this.app.pick(this) }, '다른 파일 고르기'));
    if (!f.pages) return [bar];
    this.gridEl = h('div', { class: 'pages' });
    this.fillGrid();
    return [bar, this.gridEl];
  }

  fillGrid() {
    const f = this.file;
    const grid = this.gridEl;
    if (!grid || !f?.pages) return;
    const { groups } = this.groups();
    const groupOf = new Map();
    groups.forEach((g, gi) => g.forEach((p) => { if (!groupOf.has(p)) groupOf.set(p, gi); }));
    const extract = this.mode === 'extract';
    const label = this.mode === 'ranges' || this.mode === 'every';
    const firstOf = new Set(groups.map((g) => g[0]));
    const nodes = [];
    for (let p = 1; p <= f.pages; p++) {
      const gi = groupOf.has(p) ? groupOf.get(p) : -1;
      if (!extract && p > 1 && groups.length > 1 && gi !== -1 && groupOf.has(p - 1) && groupOf.get(p - 1) !== gi) {
        nodes.push(h('div', { class: 'cut', title: '여기서 나뉩니다', 'aria-hidden': 'true' }, icon('scissors')));
      }
      const page = h('div', { class: 'page loading', style: 'width:96px;height:124px' });
      const paper = h('div', { class: 'paper', title: extract ? `${p}쪽 고르기` : `${p}쪽` }, page);
      fillThumb(page, f, p, 114, 148, { lazy: true, root: this.desk, onSize: () => { page.style.width = ''; page.style.height = ''; } });
      const cls = ['pg'];
      if (extract && groupOf.has(p)) cls.push('selected');
      if (!extract && groups.length && gi === -1) cls.push('dim');
      const pg = h('div', { class: cls.join(' ') },
        label && firstOf.has(p) && gi !== -1 ? h('div', { class: 'grp', text: `파일 ${gi + 1}` }) : null,
        paper, h('span', { class: 'tick' }, icon('check')), h('div', { class: 'no', text: `${p}` }));
      paper.addEventListener('click', () => this.clickPage(p));
      nodes.push(pg);
    }
    grid.replaceChildren(...nodes);
  }

  clickPage(p) {
    if (this.mode !== 'extract') {
      this.mode = 'extract';
      this.ranges = '';
    }
    const f = this.file;
    let sel = new Set();
    try { if (this.ranges.trim()) sel = new Set(parsePages(this.ranges, f.pages).flat()); } catch { /* start over */ }
    sel.has(p) ? sel.delete(p) : sel.add(p);
    this.ranges = formatPages([...sel]);
    this.fillGrid();
    this.renderPanel();
  }

  refreshGridSoon() {
    clearTimeout(this.gridTimer);
    this.gridTimer = setTimeout(() => this.fillGrid(), 150);
  }

  options() {
    if (!this.file) return [h('p', { class: 'help', text: 'PDF를 넣으면 나누는 방법을 고를 수 있습니다.' })];
    const set = (m) => { this.mode = m; this.fillGrid(); this.renderPanel(); };
    const rangeInput = (ph) => h('input', {
      class: 'input', type: 'text', value: this.ranges, placeholder: ph, spellcheck: 'false', dataset: { key: `split-${this.mode}` },
      oninput: (e) => { this.ranges = e.target.value; this.refreshGridSoon(); this.renderSummary(); },
    });
    this.summaryEl = h('div', { class: 'summary' });
    this.renderSummary();
    return [
      field('나누는 방법', radioChoice('split-mode', [
        { value: 'ranges', title: '범위를 정해서 나누기', desc: '쉼표로 구분한 범위마다 PDF를 하나씩 만듭니다.', extra: () => rangeInput('예: 1-3, 4-8, 9-') },
        { value: 'every', title: '몇 쪽씩 묶어서 나누기', desc: '정한 쪽 수마다 새 파일로 나눕니다.',
          extra: () => h('div', { style: 'display:flex;align-items:center;gap:8px' },
            h('input', {
              class: 'input num', type: 'number', min: '1', max: String(this.file.pages || 9999), value: String(this.every), style: 'width:90px',
              dataset: { key: 'split-every' }, oninput: (e) => { this.every = e.target.value; this.refreshGridSoon(); this.renderSummary(); },
            }), '쪽씩') },
        { value: 'each', title: '한 쪽씩 모두 나누기', desc: '모든 쪽을 각각 PDF 파일로 만듭니다.' },
        { value: 'extract', title: '원하는 쪽만 뽑아내기', desc: '고른 쪽을 모아 PDF 하나로 만듭니다. 왼쪽 미리보기에서 쪽을 눌러 고를 수도 있습니다.', extra: () => rangeInput('예: 1, 3, 5-7') },
      ], this.mode, set)),
      this.summaryEl,
    ];
  }

  renderSummary() {
    const el = this.summaryEl;
    if (!el) return;
    const f = this.file;
    if (!f?.pages) { el.textContent = f?.locked ? '먼저 암호를 입력해 주세요.' : '쪽 수를 확인하는 중입니다.'; el.className = 'summary'; return; }
    const { groups, error } = this.groups();
    el.className = error ? 'summary warn' : 'summary';
    if (error) { el.textContent = error; return; }
    if (!groups.length) { el.textContent = ''; return; }
    if (this.mode === 'extract') {
      el.replaceChildren(h('b', { text: `${groups[0].length}개 쪽` }), `(${formatPages(groups[0])}쪽)을 모아 PDF 하나로 만듭니다.`);
    } else {
      const desc = groups.slice(0, 4).map((g) => (g.length === 1 ? `${g[0]}쪽` : `${g[0]}-${g[g.length - 1]}쪽`)).join(', ');
      el.replaceChildren(h('b', { text: `PDF ${groups.length}개` }), `를 만듭니다: ${desc}${groups.length > 4 ? ' …' : ''}`);
      if (groups.length > 1) el.append(h('div', { style: 'margin-top:4px', text: `'${baseName(f.name)}_나누기' 폴더에 저장합니다.` }));
    }
    // keep the action button in sync without rebuilding the options (typing must not lose focus)
    const foot = this.panel?.querySelector('.panel-foot');
    if (foot && this.job?.state !== 'running') foot.replaceChildren(this.where(), this.footer());
  }

  problem() {
    const p = super.problem();
    if (p) return p;
    if (!this.file.pages) return this.file.error || '쪽 수를 확인하는 중입니다.';
    const { error, groups } = this.groups();
    if (error) return error;
    if (!groups.length) return '나눌 방법을 정해 주세요.';
    return null;
  }

  run() {
    this.startJob('split', { id: this.file.id, mode: this.mode, every: parseInt(this.every, 10) || 1, ranges: this.ranges });
  }
}

// =====================================================================
// 압축하기
// =====================================================================
const LEVELS = [
  { value: 'low', title: '가볍게 (화질 그대로)', desc: '이미지는 그대로 두고 문서 구조만 정리합니다. 줄어드는 양은 적습니다.' },
  { value: 'medium', title: '권장', desc: '이미지를 150dpi로 맞춥니다. 화면으로 보거나 보통 인쇄하기에 충분합니다.' },
  { value: 'high', title: '강하게', desc: '이미지를 100dpi로 줄입니다. 메일 첨부나 업로드 용량 제한에 맞출 때 좋습니다.' },
  { value: 'max', title: '최대로', desc: '이미지를 72dpi로 줄입니다. 화면으로만 볼 문서에 쓰세요.' },
  { value: 'custom', title: '직접 정하기', desc: '해상도와 이미지 화질을 직접 고릅니다.' },
];

class CompressTool extends Tool {
  constructor(app) {
    super(app, {
      id: 'compress', title: '압축하기', desc: 'PDF 용량을 줄입니다. 글자와 도형은 그대로 두고 사진과 그림을 알맞은 해상도로 다시 저장합니다.',
      kinds: ['pdf'], filter: 'pdf', multiple: true, actionLabel: 'PDF 압축하기',
      emptyTitle: '용량을 줄일 PDF를 여기로 끌어오세요', emptyHint: '여러 파일을 한 번에 압축할 수 있습니다.',
    });
    this.level = app.settings.compressLevel || 'medium';
    this.dpi = 150;
    this.quality = 70;
    this.gray = false;
  }

  deskContent() {
    const bySource = new Map();
    for (const r of this.job?.results || []) if (r.sourceId) bySource.set(r.sourceId, r);
    return [this.toolbar(), fileRows(this, (f) => {
      const r = bySource.get(f.id);
      if (!r || r.error) return null;
      const pct = r.inSize ? Math.max(0, Math.round((1 - r.size / r.inSize) * 100)) : 0;
      return h('div', {},
        h('div', { class: 'sizebar', title: `${fmtBytes(r.inSize)} → ${fmtBytes(r.size)}` }, h('i', { style: `width:${Math.max(1, 100 - pct)}%` })),
        h('div', { class: 'sub' }, h('span', { class: 'good', text: pct > 0 ? `${fmtBytes(r.size)}로 ${pct}% 줄었습니다` : '더 줄일 수 없는 파일입니다' })));
    })];
  }

  options() {
    const custom = () => h('div', { class: 'sliders' },
      slider('해상도', 50, 300, 10, this.dpi, (v) => `${v} dpi`, (v) => { this.dpi = v; }),
      slider('이미지 화질', 10, 95, 5, this.quality, (v) => `${v}`, (v) => { this.quality = v; }));
    return [
      field('압축 정도', radioChoice('level', LEVELS.map((l) => (l.value === 'custom' ? { ...l, extra: custom } : l)), this.level,
        (v) => { this.level = v; this.renderPanel(); })),
      field(null, h('label', { class: 'check' },
        h('input', { type: 'checkbox', checked: this.gray, onchange: (e) => { this.gray = e.target.checked; } }),
        h('span', {}, '사진을 흑백(회색조)으로 바꾸기')),
      help('컬러 사진을 회색조로 저장해 더 작게 만듭니다. 글자와 도형의 색은 바뀌지 않습니다.')),
    ];
  }

  run() {
    this.startJob('compress', { ids: this.files.map((f) => f.id), level: this.level, dpi: this.dpi, quality: this.quality, grayscale: this.gray });
  }
}

function slider(label, min, max, step, value, fmt, onchange) {
  const out = h('output', { text: fmt(value) });
  const input = h('input', {
    type: 'range', min: String(min), max: String(max), step: String(step), value: String(value), 'aria-label': label,
    oninput: (e) => { const v = parseInt(e.target.value, 10); out.textContent = fmt(v); onchange(v); },
  });
  return h('label', { class: 'slider' }, h('span', { text: label }), out, input);
}

// =====================================================================
// 이미지를 PDF로
// =====================================================================
class ImagesTool extends Tool {
  constructor(app) {
    super(app, {
      id: 'img2pdf', title: '이미지를 PDF로', desc: '사진이나 스캔 이미지를 PDF로 만듭니다. JPG는 다시 압축하지 않아 화질이 그대로 유지됩니다.',
      kinds: ['image'], filter: 'image', multiple: true, actionLabel: 'PDF 만들기',
      emptyTitle: 'PDF로 만들 이미지를 여기로 끌어오세요', emptyHint: 'JPG, PNG, GIF, BMP, TIFF, WEBP를 쓸 수 있습니다. 휴대폰 사진의 방향도 바로잡아 줍니다.',
    });
    this.combine = true;
    this.pageSize = 'a4';
    this.orientation = 'auto';
    this.margin = 0;
    this.outName = '';
  }

  deskContent() {
    return [
      this.toolbar([h('button', {
        class: 'btn ghost', type: 'button',
        onclick: () => { this.files.sort((a, b) => a.name.localeCompare(b.name, 'ko', { numeric: true })); this.render(); },
      }, icon('sort'), '이름순')]),
      sheetCards(this, { layout: { pageSize: this.pageSize, orientation: this.orientation, margin: this.margin } }),
    ];
  }

  options() {
    const many = this.files.length > 1;
    return [
      many ? field('만드는 방식', seg('combine', [{ value: 'one', title: '한 PDF로 합치기' }, { value: 'each', title: '이미지마다 따로' }],
        this.combine ? 'one' : 'each', (v) => { this.combine = v === 'one'; this.renderPanel(); }, '만드는 방식')) : null,
      field('용지 크기', h('select', {
        class: 'input', 'aria-label': '용지 크기',
        onchange: (e) => { this.pageSize = e.target.value; this.render(); },
      }, ...[['a4', 'A4 (210×297mm)'], ['a3', 'A3'], ['a5', 'A5'], ['b5', 'B5'], ['letter', 'Letter (미국)'], ['legal', 'Legal (미국)'], ['image', '이미지 크기 그대로']]
        .map(([v, t]) => h('option', { value: v, selected: this.pageSize === v }, t)))),
      this.pageSize !== 'image' ? field('방향', seg('orient', [{ value: 'auto', title: '자동' }, { value: 'portrait', title: '세로' }, { value: 'landscape', title: '가로' }],
        this.orientation, (v) => { this.orientation = v; this.renderDesk(); }, '방향'), help('자동은 가로로 긴 사진을 가로 용지에 놓습니다.')) : null,
      field('여백', seg('margin', [{ value: '0', title: '없음' }, { value: '5', title: '좁게' }, { value: '10', title: '보통' }, { value: '20', title: '넓게' }],
        String(this.margin), (v) => { this.margin = parseInt(v, 10); this.renderDesk(); }, '여백')),
      (this.combine || !many) ? field('저장할 파일 이름', h('input', {
        class: 'input', type: 'text', value: this.outName, dataset: { key: 'img-name' }, spellcheck: 'false',
        placeholder: this.files[0] ? (many ? `${baseName(this.files[0].name)} 외 ${this.files.length - 1}장` : baseName(this.files[0].name)) : '사진',
        oninput: (e) => { this.outName = e.target.value; },
      })) : null,
    ];
  }

  run() {
    this.startJob('images', {
      ids: this.files.map((f) => f.id), combine: this.combine, pageSize: this.pageSize,
      orientation: this.orientation, margin: this.margin, outName: this.outName,
    });
  }
}

// =====================================================================
// PDF를 이미지로 (rendered here with PDF.js, saved by the backend)
// =====================================================================
class LocalJobTool extends Tool {
  async runLocal(work) {
    this.cancelLocal = false;
    this.job = { state: 'running', progress: 0, message: '시작하는 중', results: [] };
    this.renderPanel();
    const job = this.job;
    const update = (p, msg) => { job.progress = p; job.message = msg; this.updateProgress(); };
    try {
      await work(job, update);
      job.state = this.cancelLocal ? 'canceled' : (job.results.some((r) => !r.error) || !job.results.length ? 'done' : 'error');
      if (job.state === 'error') job.error = job.results.find((r) => r.error)?.error;
    } catch (e) {
      job.state = 'error';
      job.error = e.message;
    }
    this.scrollToResults = true;
    this.render();
    if (job.state === 'done' && this.app.settings.openFolder) {
      const r = job.results.find((x) => x.isDir) || job.results.find((x) => !x.error);
      if (r) openPath(r.isDir ? r.path : r.dir).catch(() => {});
    }
  }
}

class PdfToImageTool extends LocalJobTool {
  constructor(app) {
    super(app, {
      id: 'pdf2img', title: 'PDF를 이미지로', desc: 'PDF의 각 쪽을 PNG나 JPG 그림 파일로 저장합니다.',
      kinds: ['pdf'], filter: 'pdf', multiple: true, actionLabel: '이미지로 저장',
      emptyTitle: '이미지로 바꿀 PDF를 여기로 끌어오세요', emptyHint: '쪽이 여러 개면 파일 이름으로 된 폴더에 쪽마다 저장합니다.',
    });
    this.format = 'png';
    this.dpi = 150;
    this.which = 'all';
    this.pages = '';
  }

  deskContent() { return [this.toolbar(), fileRows(this)]; }

  options() {
    return [
      field('파일 형식', seg('fmt', [{ value: 'png', title: 'PNG' }, { value: 'jpg', title: 'JPG' }], this.format, (v) => { this.format = v; this.renderPanel(); }, '파일 형식'),
        help(this.format === 'png' ? 'PNG는 글자가 선명하고 화질 손실이 없습니다.' : 'JPG는 사진이 많은 문서를 작은 용량으로 저장합니다.')),
      field('해상도', radioChoice('dpi', [
        { value: '96', title: '화면용 (96dpi)', desc: '파일이 작고 화면으로 보기에 알맞습니다.' },
        { value: '150', title: '보통 (150dpi)', desc: '대부분의 용도에 알맞습니다.' },
        { value: '300', title: '인쇄용 (300dpi)', desc: '인쇄해도 선명하지만 파일이 큽니다.' },
      ], String(this.dpi), (v) => { this.dpi = parseInt(v, 10); this.renderPanel(); })),
      field('저장할 쪽', seg('which', [{ value: 'all', title: '모든 쪽' }, { value: 'some', title: '일부만' }], this.which,
        (v) => { this.which = v; this.renderPanel(); }, '저장할 쪽'),
      this.which === 'some' ? h('input', {
        class: 'input', type: 'text', style: 'margin-top:8px', value: this.pages, placeholder: '예: 1-3, 5', spellcheck: 'false',
        dataset: { key: 'pdf2img-pages' }, oninput: (e) => { this.pages = e.target.value; },
      }) : null),
    ];
  }

  problem() {
    const p = super.problem();
    if (p) return p;
    if (this.which === 'some' && !this.pages.trim()) return '저장할 쪽을 적어 주세요.';
    if (this.files.some((f) => !f.pages && !f.error)) return '쪽 수를 확인하는 중입니다.';
    return null;
  }

  run() {
    const files = this.files.filter((f) => f.pages);
    const type = this.format === 'png' ? 'image/png' : 'image/jpeg';
    const ext = this.format === 'png' ? '.png' : '.jpg';
    this.runLocal(async (job, update) => {
      const plan = [];
      for (const f of files) {
        let pages;
        try {
          pages = this.which === 'some' ? [...new Set(parsePages(this.pages, f.pages).flat())] : Array.from({ length: f.pages }, (_, i) => i + 1);
        } catch (e) {
          job.results.push({ name: f.name, source: f.name, error: e.message });
          continue;
        }
        plan.push({ f, pages });
      }
      const total = plan.reduce((s, x) => s + x.pages.length, 0);
      let done = 0;
      for (const { f, pages } of plan) {
        if (this.cancelLocal) break;
        try {
          const doc = await loadDoc(f);
          const base = baseName(f.name);
          const sess = await api('/api/output/session', { sourceId: f.id, subfolder: pages.length > 1 ? `${base}_이미지` : '' });
          const width = String(f.pages).length;
          let last = null;
          for (const p of pages) {
            if (this.cancelLocal) break;
            update(done / total, `${f.name} ${p}쪽`);
            const blob = await renderPageBlob(doc, p, this.dpi, type, 0.9);
            const name = pages.length > 1 ? `${base}_${String(p).padStart(width, '0')}${ext}` : `${base}${ext}`;
            last = await writeOutput(sess.session, name, blob);
            done++;
          }
          if (pages.length > 1) job.results.push({ name: sess.dir.split(/[\\/]/).pop(), path: sess.dir, isDir: true, note: `그림 ${pages.length}개`, sourceId: f.id });
          else if (last) job.results.push(last);
        } catch (e) {
          job.results.push({ name: f.name, source: f.name, sourceId: f.id, error: e.message });
        }
      }
      update(1, '완료');
    });
  }
}

// =====================================================================
// PDF에서 글자 추출
// =====================================================================
class PdfToTextTool extends LocalJobTool {
  constructor(app) {
    super(app, {
      id: 'pdf2txt', title: 'PDF에서 글자 추출', desc: 'PDF에 들어 있는 글자를 메모장에서 열 수 있는 텍스트 파일(.txt)로 저장합니다.',
      kinds: ['pdf'], filter: 'pdf', multiple: true, actionLabel: '텍스트로 저장',
      emptyTitle: '글자를 뽑아낼 PDF를 여기로 끌어오세요', emptyHint: '스캔한 문서처럼 그림으로 된 PDF에는 글자 정보가 없을 수 있습니다.',
    });
    this.markPages = true;
  }

  deskContent() { return [this.toolbar(), fileRows(this)]; }

  options() {
    return [
      field(null, h('label', { class: 'check' },
        h('input', { type: 'checkbox', checked: this.markPages, onchange: (e) => { this.markPages = e.target.checked; } }),
        h('span', {}, '쪽이 바뀌는 곳에 구분선 넣기'))),
      h('div', { class: 'note', text: '스캔해서 만든 PDF는 글자가 아니라 사진이라 내용이 비어 있을 수 있습니다. 이 경우 문자 인식(OCR)이 필요합니다.' }),
    ];
  }

  problem() {
    const p = super.problem();
    if (p) return p;
    if (this.files.some((f) => !f.pages && !f.error)) return '쪽 수를 확인하는 중입니다.';
    return null;
  }

  run() {
    const files = this.files.filter((f) => f.pages);
    this.runLocal(async (job, update) => {
      const total = files.reduce((s, f) => s + f.pages, 0);
      let done = 0;
      for (const f of files) {
        if (this.cancelLocal) break;
        try {
          const doc = await loadDoc(f);
          const parts = [];
          let chars = 0;
          for (let p = 1; p <= f.pages; p++) {
            if (this.cancelLocal) break;
            update(done / total, `${f.name} ${p}쪽`);
            const t = await pageText(doc, p);
            chars += t.replace(/\s/g, '').length;
            parts.push(this.markPages ? `${p === 1 ? '' : '\n\n'}──────── ${p}쪽 ────────\n\n${t}` : t);
            done++;
          }
          if (this.cancelLocal) break;
          const sess = await api('/api/output/session', { sourceId: f.id, subfolder: '' });
          const text = parts.join(this.markPages ? '' : '\n\n').replace(/\n/g, '\r\n');
          const r = await writeOutput(sess.session, `${baseName(f.name)}.txt`, new Blob(['﻿' + text], { type: 'text/plain;charset=utf-8' }));
          r.note = chars ? `글자 ${chars.toLocaleString('ko-KR')}자` : '글자를 찾지 못했습니다 (스캔한 문서일 수 있습니다)';
          r.sourceId = f.id;
          job.results.push(r);
        } catch (e) {
          job.results.push({ name: f.name, source: f.name, sourceId: f.id, error: e.message });
        }
      }
      update(1, '완료');
    });
  }
}

// =====================================================================
// 문서를 PDF로 / PDF를 Word로 (installed Office, Hancom or LibreOffice)
// =====================================================================
function engineList(conv) {
  const row = (ok, name) => h('div', { class: ok ? 'yes' : 'no' }, icon(ok ? 'check' : 'minus'), h('span', { text: name + (ok ? '' : ' (없음)') }));
  return h('div', { class: 'engines' },
    row(conv.word, 'Microsoft Word'), row(conv.excel, 'Microsoft Excel'), row(conv.powerpoint, 'Microsoft PowerPoint'),
    row(conv.hwp, '한컴오피스 한글'), row(!!conv.libreoffice, 'LibreOffice'));
}

class DocumentsTool extends Tool {
  constructor(app) {
    super(app, {
      id: 'doc2pdf', title: '문서를 PDF로', desc: 'Word, Excel, PowerPoint, 한글 문서를 PDF로 바꿉니다. 이 PC에 설치된 오피스 프로그램으로 변환합니다.',
      kinds: ['document'], filter: 'document', multiple: true, actionLabel: 'PDF로 변환',
      emptyTitle: 'PDF로 바꿀 문서를 여기로 끌어오세요', emptyHint: 'docx, xlsx, pptx, hwp, hwpx, odt 같은 문서를 쓸 수 있습니다.',
    });
  }

  deskContent() {
    const eng = this.app.state.documentEngine || {};
    return [this.toolbar(), fileRows(this, (f) => {
      const label = eng[f.ext];
      return h('div', { class: 'sub' }, label ? h('span', { class: 'good', text: `${label}로 변환` }) : h('span', { class: 'bad', text: '이 파일을 변환할 프로그램이 없습니다' }));
    })];
  }

  options() {
    const conv = this.app.state.converters;
    const any = conv.word || conv.excel || conv.powerpoint || conv.hwp || conv.libreoffice;
    return [
      field('이 PC에서 찾은 프로그램', engineList(conv)),
      any ? null : h('div', { class: 'summary warn', text: '변환할 프로그램이 없습니다. Microsoft Office나 한컴오피스 한글, 또는 무료인 LibreOffice를 설치하면 쓸 수 있습니다.' }),
      h('div', { class: 'note', text: '변환하는 동안 오피스 프로그램이 보이지 않게 실행됩니다. 한글 문서를 바꿀 때 한컴오피스가 접근 허용 여부를 물으면 [모두 허용]을 눌러 주세요.' }),
    ];
  }

  problem() {
    const p = super.problem();
    if (p) return p;
    const eng = this.app.state.documentEngine || {};
    if (!this.files.some((f) => eng[f.ext])) return '변환할 수 있는 프로그램이 없습니다.';
    return null;
  }

  run() { this.startJob('documents', { ids: this.files.map((f) => f.id) }); }
}

class PdfToWordTool extends Tool {
  constructor(app) {
    super(app, {
      id: 'pdf2word', title: 'PDF를 Word로', desc: 'PDF를 고쳐 쓸 수 있는 Word 문서(.docx)로 바꿉니다.',
      kinds: ['pdf'], filter: 'pdf', multiple: true, actionLabel: 'Word로 변환',
      emptyTitle: 'Word로 바꿀 PDF를 여기로 끌어오세요', emptyHint: '글자로 된 PDF일수록 결과가 좋습니다.',
    });
  }

  deskContent() { return [this.toolbar(), fileRows(this)]; }

  options() {
    const engine = this.app.state.pdfToWord;
    return [
      field('변환에 쓸 프로그램', engine
        ? h('div', { class: 'engines' }, h('div', { class: 'yes' }, icon('check'), h('span', { text: engine })))
        : h('div', { class: 'summary warn', text: 'Microsoft Word(2013 이상)나 무료인 LibreOffice가 있어야 변환할 수 있습니다.' })),
      h('div', { class: 'note', text: '표, 글상자, 복잡한 배치는 원본과 조금 달라질 수 있습니다. 스캔한 PDF는 글자를 인식하지 않고 그림으로 넣습니다.' }),
    ];
  }

  problem() {
    const p = super.problem();
    if (p) return p;
    if (!this.app.state.pdfToWord) return '변환할 프로그램이 없습니다.';
    return null;
  }

  run() { this.startJob('pdf2word', { ids: this.files.map((f) => f.id) }); }
}

export function createTools(app) {
  const list = [
    new MergeTool(app), new SplitTool(app), new CompressTool(app),
    new ImagesTool(app), new PdfToImageTool(app), new DocumentsTool(app), new PdfToWordTool(app), new PdfToTextTool(app),
  ];
  const tools = {};
  for (const t of list) tools[t.id] = t;
  tools.settings = { id: 'settings', title: '설정' };
  return tools;
}
