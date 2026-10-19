import { api, token } from './api.js';
import { h, toast, showModal, closeModal } from './ui.js';
import { closeDoc } from './pdfview.js';
import { createTools } from './tools.js';

export const app = {
  state: null,
  tools: {},
  current: null,

  get settings() { return this.state?.settings || { outMode: 'source' }; },

  // Where results will be saved: [short text, full path for the tooltip].
  where(file) {
    const s = this.settings;
    const short = (p) => {
      const parts = p.split(/[\\/]/).filter(Boolean);
      return parts.length > 2 ? `…${p.includes('\\') ? '\\' : '/'}${parts.slice(-2).join(p.includes('\\') ? '\\' : '/')}` : p;
    };
    if (s.outMode === 'custom' && s.outDir) return [short(s.outDir), s.outDir];
    if (file && !file.temp && file.dir) return ['원본 파일과 같은 폴더', file.dir];
    if (file && file.temp) return [`문서 폴더의 '${this.state.app}'`, this.state.defaultOutDir];
    return ['원본 파일과 같은 폴더', ''];
  },

  select(id) {
    const tool = this.tools[id];
    if (!tool) return;
    if (id === 'settings') { openSettings(); return; }
    this.current = tool;
    for (const b of document.querySelectorAll('.tool[data-tool]')) {
      if (b.dataset.tool === id) b.setAttribute('aria-current', 'page');
      else b.removeAttribute('aria-current');
    }
    document.getElementById('tool-title').textContent = tool.title;
    document.getElementById('tool-desc').textContent = tool.desc;
    tool.mount(document.getElementById('desk'), document.getElementById('panel'));
    try { localStorage.setItem('pinkpdf-tool', id); } catch { /* ignore */ }
  },

  // Opens the native file picker (or the browser one as a fallback).
  async pick(tool = this.current) {
    try {
      if (this.state?.nativeDialogs) {
        const res = await api('/api/dialog/files', { filter: tool.filter, multiple: tool.multiple });
        if (res.unsupported) return this.pickBrowser(tool);
        if (res.canceled) return;
        this.receive(tool, res);
        return;
      }
      this.pickBrowser(tool);
    } catch (e) {
      toast(e.message, 'err');
    }
  },

  pickBrowser(tool) {
    const input = document.getElementById('file-input');
    input.multiple = tool.multiple;
    input.accept = tool.acceptAttr();
    input.value = '';
    input.onchange = () => { if (input.files.length) this.upload(tool, input.files); };
    input.click();
  },

  async upload(tool, fileList) {
    const fd = new FormData();
    let n = 0;
    for (const f of fileList) {
      if (f.size === 0 && !f.type) continue; // folders dropped by mistake
      fd.append('file', f, f.name);
      n++;
    }
    if (!n) { toast('추가할 파일이 없습니다. 폴더 대신 파일을 끌어다 놓아 주세요.', 'err'); return; }
    tool.setBusy?.(`파일 ${n}개 불러오는 중…`);
    try {
      const res = await api('/api/files/upload', fd);
      this.receive(tool, res);
    } catch (e) {
      toast(e.message, 'err');
    } finally {
      tool.setBusy?.(null);
    }
  },

  receive(tool, res) {
    for (const e of res.errors || []) toast(e, 'err');
    const files = res.files || [];
    const ok = files.filter((f) => tool.accepts(f));
    const bad = files.filter((f) => !tool.accepts(f));
    if (bad.length) {
      toast(`${tool.title}에 쓸 수 없는 파일은 뺐습니다: ${bad.map((f) => f.name).slice(0, 3).join(', ')}${bad.length > 3 ? ` 외 ${bad.length - 3}개` : ''}`, 'err');
      api('/api/files/forget', { ids: bad.map((f) => f.id) }).catch(() => {});
    }
    if (ok.length) tool.addFiles(ok);
  },

  forget(files) {
    const ids = files.map((f) => f.id);
    for (const id of ids) closeDoc(id);
    api('/api/files/forget', { ids }).catch(() => {});
  },

  async saveSettings(next) {
    const s = await api('/api/settings', { ...this.settings, ...next });
    this.state.settings = s;
    this.current?.renderPanel?.();
    return s;
  },
};

// ---------- settings dialog ----------
function openSettings() {
  const s = app.settings;
  let mode = s.outMode;
  let dir = s.outDir || '';
  const dirInput = h('input', { class: 'input', value: dir, readonly: true, placeholder: '폴더를 선택해 주세요', 'aria-label': '저장 폴더' });
  const pickBtn = h('button', {
    class: 'btn', type: 'button', onclick: async () => {
      try {
        if (!app.state.nativeDialogs) {
          const p = window.prompt('결과를 저장할 폴더 경로를 입력해 주세요', dir);
          if (p) { dir = p; dirInput.value = p; setMode('custom'); }
          return;
        }
        const res = await api('/api/dialog/folder', {});
        if (res.path) { dir = res.path; dirInput.value = res.path; setMode('custom'); }
      } catch (e) { toast(e.message, 'err'); }
    },
  }, '폴더 선택');
  const r1 = h('input', { type: 'radio', name: 'outmode', value: 'source', checked: mode === 'source', onchange: () => setMode('source') });
  const r2 = h('input', { type: 'radio', name: 'outmode', value: 'custom', checked: mode === 'custom', onchange: () => setMode('custom') });
  function setMode(m) { mode = m; r1.checked = m === 'source'; r2.checked = m === 'custom'; }
  const openAfter = h('input', { type: 'checkbox', checked: !!s.openFolder });
  const err = h('div', { class: 'err' });

  const form = h('form', { method: 'dialog' },
    h('h2', { text: '설정' }),
    h('div', { class: 'settings-sec' },
      h('h3', { text: '결과 파일을 저장할 곳' }),
      h('div', { class: 'choice' },
        h('label', { class: 'opt' }, r1, h('span', { class: 't' }, '원본 파일과 같은 폴더'),
          h('span', { class: 'd' }, `끌어다 놓은 파일은 원래 위치를 알 수 없어 ${app.state.defaultOutDir}에 저장합니다.`)),
        h('label', { class: 'opt' }, r2, h('span', { class: 't' }, '항상 이 폴더에 저장'),
          h('span', { class: 'extra' }, h('div', { class: 'pathrow' }, dirInput, pickBtn)))),
      h('label', { class: 'check', style: 'margin-top:14px' }, openAfter, h('span', {}, '작업이 끝나면 저장한 폴더를 바로 열기'))),
    h('div', { class: 'settings-sec about' },
      h('h3', { text: '이 프로그램에 대하여' }),
      h('div', {}, `${app.state.app} ${app.state.version}. 모든 작업은 이 PC 안에서만 처리하며 파일을 인터넷으로 보내지 않습니다.`),
      h('div', {}, '사용한 오픈소스 (모두 무료 라이선스):'),
      h('ul', {},
        h('li', {}, 'pdfcpu (Apache 2.0) — PDF 합치기, 나누기, 압축'),
        h('li', {}, 'PDF.js (Apache 2.0) — 미리보기, 이미지와 텍스트 변환'),
        h('li', {}, 'go-webview2 (MIT), zenity (MIT), Go 표준 라이브러리 (BSD)'))),
    err,
    h('div', { class: 'actions' },
      h('button', { class: 'btn', type: 'button', onclick: () => closeModal() }, '닫기'),
      h('button', { class: 'btn ok', type: 'submit' }, '저장')));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (mode === 'custom' && !dir) { err.textContent = '저장할 폴더를 선택해 주세요.'; return; }
    try {
      await app.saveSettings({ outMode: mode, outDir: dir, openFolder: openAfter.checked });
      closeModal();
      toast('설정을 저장했습니다.');
    } catch (ex) { err.textContent = ex.message; }
  });
  showModal(form, true);
}

// ---------- drag & drop from Explorer ----------
function setupDrop() {
  const veil = document.getElementById('drop-veil');
  let depth = 0;
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
  window.addEventListener('dragenter', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth++;
    if (app.current) {
      document.getElementById('drop-veil-text').textContent = `놓으면 '${app.current.title}'에 추가합니다`;
      veil.hidden = false;
    }
  });
  window.addEventListener('dragleave', (e) => {
    if (!hasFiles(e)) return;
    depth = Math.max(0, depth - 1);
    if (!depth) veil.hidden = true;
  });
  window.addEventListener('dragover', (e) => { if (hasFiles(e)) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
  window.addEventListener('drop', (e) => {
    if (!hasFiles(e)) return; // internal drags (reordering) are handled by their elements
    e.preventDefault();
    depth = 0;
    veil.hidden = true;
    if (app.current && e.dataTransfer.files.length) app.upload(app.current, e.dataTransfer.files);
  });
}

async function boot() {
  try {
    app.state = await api('/api/state');
  } catch (e) {
    document.getElementById('desk').replaceChildren(h('p', { class: 'summary warn' }, e.message));
    return;
  }
  app.tools = createTools(app);
  for (const b of document.querySelectorAll('.tool[data-tool]')) {
    b.addEventListener('click', () => app.select(b.dataset.tool));
  }
  setupDrop();
  // right-click menus belong to native apps; keep it for text fields only
  document.addEventListener('contextmenu', (e) => {
    if (!e.target.closest('input, textarea')) e.preventDefault();
  });
  if (app.state.browserMode) {
    // the program keeps running while this page is open
    let lost = false;
    const ping = () => api('/api/ping', {}).then(() => { lost = false; }, () => {
      if (!lost) toast('프로그램과 연결이 끊어졌습니다. 프로그램이 종료되었다면 다시 실행해 주세요.', 'err');
      lost = true;
    });
    setInterval(ping, 10000);
    ping();
    window.addEventListener('pagehide', () => {
      try { navigator.sendBeacon(`/api/bye?t=${encodeURIComponent(token)}`); } catch { /* best effort */ }
    });
  }
  const skipped = app.state.initialSkipped || [];
  if (skipped.length) toast(`다음 항목은 넣지 못했습니다: ${skipped.join(', ')} (폴더, 찾을 수 없는 파일 또는 지원하지 않는 형식)`, 'err');
  const initial = app.state.initialFiles || [];
  if (initial.length) {
    // files dropped onto the program icon: pick the tool that fits them
    const kinds = new Set(initial.map((f) => f.kind));
    let id = 'merge';
    if (kinds.size === 1 && kinds.has('image')) id = 'img2pdf';
    else if (kinds.size === 1 && kinds.has('document')) id = 'doc2pdf';
    else if (kinds.size === 1 && kinds.has('pdf') && initial.length === 1) id = 'compress';
    app.select(id);
    app.receive(app.tools[id], { files: initial });
    return;
  }
  let first = 'merge';
  try { first = localStorage.getItem('pinkpdf-tool') || 'merge'; } catch { /* ignore */ }
  app.select(app.tools[first] && first !== 'settings' ? first : 'merge');
}

boot();
