// Talks to the local Go backend (127.0.0.1 only, guarded by a per-run token).

const params = new URLSearchParams(location.search);
let tok = params.get('t') || '';
try {
  if (tok) sessionStorage.setItem('pinkpdf-token', tok);
  else tok = sessionStorage.getItem('pinkpdf-token') || '';
} catch { /* storage may be unavailable */ }
if (params.has('t')) history.replaceState(null, '', location.pathname);

export const token = tok;

export async function api(path, body) {
  const opts = { method: body === undefined ? 'GET' : 'POST', headers: { 'X-Token': token } };
  if (body !== undefined) {
    if (body instanceof Blob || body instanceof FormData) {
      opts.body = body;
    } else {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new Error('프로그램 내부 서버와 연결이 끊겼습니다. 프로그램을 다시 실행해 주세요.');
  }
  let data = null;
  try { data = await res.json(); } catch { /* not json */ }
  if (!res.ok) throw new Error((data && data.error) || `요청이 실패했습니다 (${res.status})`);
  return data;
}

export const fileURL = (id) => `/api/files/${encodeURIComponent(id)}/content?t=${encodeURIComponent(token)}`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Starts a backend job and polls it until it finishes.
export async function runJob(kind, body, onUpdate) {
  let job = await api(`/api/jobs/${kind}`, body);
  onUpdate?.(job);
  let delay = 150;
  while (job.state === 'running') {
    await sleep(delay);
    delay = Math.min(400, delay + 50);
    job = await api(`/api/jobs/${job.id}`);
    onUpdate?.(job);
  }
  return job;
}

export const cancelJob = (id) => api(`/api/jobs/${id}/cancel`, {}).catch(() => {});

export const openPath = (path) => api('/api/shell/open', { path });
export const revealPath = (path) => api('/api/shell/reveal', { path });

// Writes a file produced in the page (rendered images, extracted text).
export async function writeOutput(session, name, blob) {
  const res = await fetch(`/api/output/write?session=${encodeURIComponent(session)}&name=${encodeURIComponent(name)}`, {
    method: 'POST', headers: { 'X-Token': token }, body: blob,
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error((data && data.error) || '파일을 저장하지 못했습니다');
  return data;
}
