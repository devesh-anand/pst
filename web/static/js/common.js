// Shared helpers for the create and view pages.

export const $ = (id) => document.getElementById(id);

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function api(path, options = {}) {
  const res = await fetch(path, { ...options, credentials: 'omit', cache: 'no-store' });
  if (res.status === 204) return null;
  let body = null;
  try {
    body = await res.json();
  } catch {
    // Non-JSON error, e.g. from a reverse proxy.
  }
  if (!res.ok) {
    throw new ApiError(res.status, body?.code ?? 'http_error', body?.error ?? `Request failed (${res.status})`);
  }
  return body;
}

const EXPIRY_LABELS = new Map([
  [0, 'Never'],
  [600, '10 minutes'],
  [3600, '1 hour'],
  [86400, '1 day'],
  [604800, '1 week'],
  [2592000, '30 days'],
  [31536000, '1 year'],
]);

export function expiryLabel(seconds) {
  return EXPIRY_LABELS.get(seconds) ?? `${Math.round(seconds / 3600)} hours`;
}

export function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
const UNITS = [
  ['year', 31536000],
  ['month', 2592000],
  ['week', 604800],
  ['day', 86400],
  ['hour', 3600],
  ['minute', 60],
];

export function relativeTime(date) {
  const secs = (new Date(date).getTime() - Date.now()) / 1000;
  for (const [unit, size] of UNITS) {
    if (Math.abs(secs) >= size) return rtf.format(Math.round(secs / size), unit);
  }
  return rtf.format(Math.round(secs), 'second');
}

// Delete tokens are remembered per browser so the creator gets a Delete
// button. Storage may be unavailable (private mode), which is fine.
const tokenKey = (id) => `pst:delete:${id}`;

export function rememberDeleteToken(id, token) {
  try {
    localStorage.setItem(tokenKey(id), token);
  } catch {}
}

export function recallDeleteToken(id) {
  try {
    return localStorage.getItem(tokenKey(id));
  } catch {
    return null;
  }
}

export function forgetDeleteToken(id) {
  try {
    localStorage.removeItem(tokenKey(id));
  } catch {}
}

export async function copyText(text, button) {
  try {
    await navigator.clipboard.writeText(text);
    const label = button.textContent;
    button.textContent = 'Copied';
    setTimeout(() => (button.textContent = label), 1500);
  } catch {
    button.textContent = 'Copy failed';
  }
}
