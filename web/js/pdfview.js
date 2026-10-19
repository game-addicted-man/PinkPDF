// PDF.js integration: opening documents (with password prompts), page
// thumbnails through a small render queue, and full-page rendering.
import * as pdfjs from '../pdfjs/pdf.min.mjs';
import { api, fileURL } from './api.js';
import { prompt } from './ui.js';

pdfjs.GlobalWorkerOptions.workerSrc = '/pdfjs/pdf.worker.min.mjs';

const BASE = {
  cMapUrl: '/pdfjs/cmaps/',
  cMapPacked: true,
  standardFontDataUrl: '/pdfjs/standard_fonts/',
  wasmUrl: '/pdfjs/wasm/',
  iccUrl: '/pdfjs/iccs/',
  isEvalSupported: false,
  enableXfa: false,
};

const docs = new Map(); // file id -> Promise<PDFDocumentProxy>

export class PasswordCanceled extends Error {}

async function askPassword(file, wrong) {
  return prompt({
    title: wrong ? '암호가 맞지 않습니다' : '암호로 보호된 PDF',
    message: `"${file.name}" 파일을 열려면 암호가 필요합니다.`,
    label: '암호', type: 'password', okText: '열기',
  });
}

// Opens a PDF once and caches it. Resolves to the pdf.js document.
export function loadDoc(file) {
  if (docs.has(file.id)) return docs.get(file.id);
  const task = pdfjs.getDocument({ ...BASE, url: fileURL(file.id), password: file.password || undefined });
  let used = file.password || null;
  let canceled = false;
  task.onPassword = async (update, reason) => {
    const pw = await askPassword(file, reason === pdfjs.PasswordResponses.INCORRECT_PASSWORD);
    if (pw == null) {
      canceled = true;
      task.destroy();
      return;
    }
    used = pw;
    update(pw);
  };
  const p = task.promise.then(async (doc) => {
    file.pages = doc.numPages;
    if (used && used !== file.passwordSent) {
      file.password = used;
      try {
        await api(`/api/files/${file.id}/password`, { password: used });
        file.passwordSent = used;
        file.locked = false;
      } catch { /* the backend will ask again if needed */ }
    }
    return doc;
  }, (err) => {
    docs.delete(file.id);
    if (canceled) {
      file.locked = true;
      throw new PasswordCanceled('암호를 입력하지 않았습니다');
    }
    throw new Error(`PDF를 열 수 없습니다: ${err?.message || err}`);
  });
  docs.set(file.id, p);
  return p;
}

export function closeDoc(id) {
  const p = docs.get(id);
  if (!p) return;
  docs.delete(id);
  p.then((d) => d.destroy()).catch(() => {});
  for (const k of [...thumbs.keys()]) if (k.startsWith(id + ':')) thumbs.delete(k);
}

// ---- render queue (keeps the UI responsive with big documents) ----
const queue = [];
let running = 0;
const MAX = 2;

function enqueue(fn, priority = false) {
  return new Promise((resolve, reject) => {
    const job = () => fn().then(resolve, reject);
    priority ? queue.unshift(job) : queue.push(job);
    pump();
  });
}

function pump() {
  while (running < MAX && queue.length) {
    const job = queue.shift();
    running++;
    job().finally(() => { running--; pump(); });
  }
}

const thumbs = new Map(); // `${id}:${page}:${box}` -> Promise<canvas>

// Renders page `no` to fit a box (CSS pixels). Cached per size.
export function thumbnail(file, no, boxW, boxH) {
  const key = `${file.id}:${no}:${boxW}x${boxH}`;
  if (thumbs.has(key)) return thumbs.get(key);
  // open the document first (it may wait on a password prompt) so a locked
  // file never holds one of the render slots
  const p = loadDoc(file).then((doc) => enqueue(async () => {
    const page = await doc.getPage(no);
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const vp1 = page.getViewport({ scale: 1 });
    const s = Math.min(boxW / vp1.width, boxH / vp1.height);
    const vp = page.getViewport({ scale: s * dpr });
    const canvas = document.createElement('canvas');
    canvas.width = Math.max(1, Math.floor(vp.width));
    canvas.height = Math.max(1, Math.floor(vp.height));
    canvas.style.width = `${Math.floor(vp.width / dpr)}px`;
    canvas.style.height = `${Math.floor(vp.height / dpr)}px`;
    await page.render({ canvas, viewport: vp }).promise;
    page.cleanup();
    return canvas;
  }));
  p.catch(() => thumbs.delete(key));
  thumbs.set(key, p);
  return p;
}

// Places a lazily rendered thumbnail into `holder` (a .page element).
export function fillThumb(holder, file, no, boxW, boxH, { lazy = false, root = null, onSize } = {}) {
  const go = () => {
    thumbnail(file, no, boxW, boxH).then((canvas) => {
      // the same cached canvas may be shown again after a re-render
      holder.classList.remove('loading');
      holder.replaceChildren(canvas.isConnected && canvas.parentNode !== holder ? cloneCanvas(canvas) : canvas);
      onSize?.(canvas);
    }, () => {
      holder.classList.remove('loading');
      holder.replaceChildren();
      holder.classList.add('broken');
    });
  };
  if (!lazy) return go();
  const io = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting)) { io.disconnect(); go(); }
  }, { root, rootMargin: '600px' });
  io.observe(holder);
}

function cloneCanvas(src) {
  const c = document.createElement('canvas');
  c.width = src.width; c.height = src.height;
  c.style.width = src.style.width; c.style.height = src.style.height;
  c.getContext('2d').drawImage(src, 0, 0);
  return c;
}

// Renders a page at a given DPI and returns an image Blob.
export async function renderPageBlob(doc, no, dpi, type, quality) {
  const page = await doc.getPage(no);
  let scale = dpi / 72;
  const vp1 = page.getViewport({ scale: 1 });
  // keep canvases within what browsers can allocate (about 16k px / 120 MP)
  const maxSide = 14000, maxArea = 100e6;
  scale = Math.min(scale, maxSide / vp1.width, maxSide / vp1.height, Math.sqrt(maxArea / (vp1.width * vp1.height)));
  const vp = page.getViewport({ scale });
  const canvas = document.createElement('canvas');
  canvas.width = Math.max(1, Math.round(vp.width));
  canvas.height = Math.max(1, Math.round(vp.height));
  await page.render({ canvas, viewport: vp, background: '#ffffff' }).promise;
  const blob = await new Promise((res) => canvas.toBlob(res, type, quality));
  page.cleanup();
  canvas.width = canvas.height = 0;
  if (!blob) throw new Error('이미지를 만들지 못했습니다 (페이지가 너무 큽니다)');
  return blob;
}

// Extracts the text of a page, keeping line breaks.
export async function pageText(doc, no) {
  const page = await doc.getPage(no);
  const content = await page.getTextContent();
  let out = '';
  let lastY = null;
  let lastH = 10;
  const newline = () => { if (out && !out.endsWith('\n')) out += '\n'; };
  for (const item of content.items) {
    if (!('str' in item)) continue;
    const y = item.transform ? item.transform[5] : null;
    if (lastY !== null && y !== null && Math.abs(y - lastY) > 2) {
      newline();
      // a gap well beyond one line height starts a new paragraph
      if (Math.abs(lastY - y) > Math.max(lastH, item.height || 0) * 1.9 && !out.endsWith('\n\n')) out += '\n';
    }
    if (item.str) {
      out += item.str;
      if (y !== null) lastY = y;
      if (item.height) lastH = item.height;
    }
    if (item.hasEOL) newline();
  }
  page.cleanup();
  return out.replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
}
