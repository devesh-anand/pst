import { encrypt } from './crypto.js';
import { $, api, ApiError, copyText, expiryLabel, formatBytes, rememberDeleteToken } from './common.js';

const DEFAULT_EXPIRY = 604800; // 1 week

const form = $('create-form');
const text = $('text');
const submit = $('submit');
const errorBox = $('error');
const writePassword = $('write-password');
let config = null;

function showError(msg) {
  errorBox.textContent = msg;
  errorBox.hidden = !msg;
}

function updateSize() {
  const bytes = new TextEncoder().encode(text.value).length;
  $('size').textContent = bytes ? formatBytes(bytes) : '';
}

async function init() {
  try {
    config = await api('/api/config');
  } catch (err) {
    showError(`Could not reach the server: ${err.message}`);
    submit.disabled = true;
    return;
  }
  const select = $('expiry');
  for (const secs of config.expiries) {
    const opt = document.createElement('option');
    opt.value = String(secs);
    opt.textContent = expiryLabel(secs);
    opt.selected = secs === DEFAULT_EXPIRY;
    select.append(opt);
  }
  if (config.writePasswordRequired) {
    $('write-password-row').hidden = false;
    writePassword.required = true;
  }
}

async function create(event) {
  event.preventDefault();
  showError('');
  if (!text.value) {
    showError('Nothing to paste.');
    return;
  }
  if (config.writePasswordRequired && !writePassword.value) {
    showError('Enter the write password.');
    writePassword.focus();
    return;
  }
  submit.disabled = true;
  submit.textContent = 'Encrypting…';
  try {
    const { envelope, key } = await encrypt(text.value, $('paste-password').value);
    if (envelope.length > config.maxPasteBytes) {
      throw new Error(
        `Paste is too large: ${formatBytes(envelope.length)} after encryption, limit is ${formatBytes(config.maxPasteBytes)}.`,
      );
    }
    submit.textContent = 'Uploading…';
    const headers = { 'Content-Type': 'application/json' };
    if (config.writePasswordRequired) headers.Authorization = `Bearer ${writePassword.value}`;
    const paste = await api('/api/pastes', {
      method: 'POST',
      headers,
      body: JSON.stringify({
        content: envelope,
        expiresIn: Number($('expiry').value),
        burnAfterRead: $('burn').checked,
      }),
    });
    rememberDeleteToken(paste.id, paste.deleteToken);
    showResult(paste, `${location.origin}/${paste.id}#${key}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      writePassword.value = '';
      writePassword.focus();
      showError('Wrong write password.');
    } else {
      showError(err.message);
    }
  } finally {
    submit.disabled = false;
    submit.textContent = 'Create paste';
  }
}

function showResult(paste, url) {
  form.reset();
  form.hidden = true;
  $('share-url').value = url;
  $('open-link').href = url;
  $('delete-token').value = paste.deleteToken;
  const parts = [paste.expiresAt ? `Expires ${new Date(paste.expiresAt).toLocaleString()}` : 'Never expires'];
  if (paste.burnAfterRead) parts.push('deleted after the first read');
  $('result-meta').textContent = parts.join(' · ');
  $('result').hidden = false;
  $('share-url').select();
}

form.addEventListener('submit', create);
text.addEventListener('input', updateSize);
text.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) form.requestSubmit();
});
$('copy-url').addEventListener('click', (e) => copyText($('share-url').value, e.currentTarget));
$('copy-token').addEventListener('click', (e) => copyText($('delete-token').value, e.currentTarget));

init();
