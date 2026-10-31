// The control panel's API: JSON both ways. A failure throws an ApiError
// with what the server said.
//
// The session's end: the server ends a session after a while without a
// request, and after some hours at most; each answer says how long it has
// left. Someone typing or clicking here is still here: the session is kept
// alive for them, at most once a minute. Once it's over (they went away),
// the panel goes to the sign-in page at once, and back here after: nothing
// is ever changed on a page whose session already ended.

export class ApiError extends Error {
  type?: string;
  status: number;
  constructor(message: string, status: number, type?: string) {
    super(message);
    this.status = status;
    this.type = type;
  }
}

let sessionEnds = 0; // Date.now() it ends at; 0 until an answer says
let sessionTimer: ReturnType<typeof setTimeout> | undefined;
let lastAsked = Date.now();

/** signInAgain goes to the sign-in page, and back to this page after. */
export function signInAgain(back = `/admin/${location.hash}`) {
  location.href = `/admin/login?return=${encodeURIComponent(back)}`;
}

function sessionLeft(res: Response) {
  lastAsked = Date.now();
  const left = Number(res.headers.get('Overfin-Session-Left'));
  if (!res.headers.has('Overfin-Session-Left') || !Number.isFinite(left)) return;
  sessionEnds = Date.now() + left * 1000;
  clearTimeout(sessionTimer);
  // A little early: the clocks never quite agree.
  sessionTimer = setTimeout(sessionOver, Math.max(0, left * 1000 - 5000));
}

function sessionOver() {
  if (sessionEnds && Date.now() >= sessionEnds - 5000) signInAgain();
}

// A timer may wait while the computer sleeps or the tab is hidden: checked
// again when it's back.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) sessionOver();
});
for (const kind of ['keydown', 'pointerdown', 'input', 'wheel']) {
  document.addEventListener(kind, () => {
    if (!sessionEnds || Date.now() - lastAsked < 60_000) return;
    if (Date.now() >= sessionEnds - 5000) {
      signInAgain();
      return;
    }
    lastAsked = Date.now();
    fetch('/admin/api/session').then((res) => (res.status === 401 ? signInAgain() : sessionLeft(res)), () => {});
  }, { passive: true, capture: true });
}

/** api asks the server. Signed out, it goes to the sign-in page and never
 * answers: nothing waiting on it goes on. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, headers: {} };
  if (body !== undefined) {
    (init.headers as Record<string, string>)['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  sessionLeft(res);
  if (res.status === 204) return null as T;
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) {
    signInAgain();
    return new Promise<T>(() => {});
  }
  if (!res.ok) throw new ApiError(data.detail || data.error || data.type || `HTTP ${res.status}`, res.status, data.type);
  return data as T;
}

/** upload sends a file as it is (branding, titles' images), with its own
 * type. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function upload<T = any>(path: string, file: File, method = 'PUT'): Promise<T> {
  const res = await fetch(path, { method, headers: { 'Content-Type': file.type || 'image/png' }, body: file });
  sessionLeft(res);
  const out = await res.json().catch(() => ({}));
  if (res.status === 401) {
    signInAgain();
    return new Promise<T>(() => {});
  }
  if (!res.ok) throw new ApiError(out.detail || (res.status === 415 ? 'That isn\'t an image.' : `HTTP ${res.status}`), res.status, out.type);
  return out as T;
}
