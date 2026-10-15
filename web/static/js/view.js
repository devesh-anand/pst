import { decrypt, DecryptError, parseEnvelope } from './crypto.js';
import { $, api, ApiError, copyText, forgetDeleteToken, recallDeleteToken, relativeTime } from './common.js';

const id = location.pathname.slice(1);
const key = location.hash.slice(1);
let text = '';

function showStatus(msg, isError = false) {
  const el = $('status');
  el.textContent = msg;
  el.classList.toggle('error', isError);
  el.hidden = !msg;
}

async function load() {
  if (!key) {
    showStatus('This link is missing its decryption key (the part after #). Ask the sender for the full link.', true);
    return;
  }
  let paste;
  try {
    paste = await api(`/api/pastes/${id}`);
  } catch (err) {
    showStatus(notFoundMessage(err), true);
    return;
  }
  if (paste.burnAfterRead) {
    // Revealing is an explicit POST so link-preview bots can't burn the paste.
    showStatus('');
    $('burn-confirm').hidden = false;
    $('reveal').addEventListener('click', () => reveal(), { once: true });
    return;
  }
  await unlock(paste, false);
}

async function reveal() {
  $('burn-confirm').hidden = true;
  showStatus('Opening…');
  try {
    const paste = await api(`/api/pastes/${id}/open`, { method: 'POST' });
    await unlock(paste, true);
  } catch (err) {
    showStatus(notFoundMessage(err), true);
  }
}

async function unlock(paste, burned) {
  let parsed;
  try {
    parsed = parseEnvelope(paste.content);
  } catch (err) {
    showStatus(`This paste is corrupted: ${err.message}.`, true);
    return;
  }
  if (!parsed.needsPassword) {
    showStatus('Decrypting…');
    try {
      render(paste, await decrypt(parsed, key), burned);
    } catch (err) {
      showStatus(decryptMessage(err), true);
    }
    return;
  }

  // The ciphertext stays in memory, so a burned paste can still be retried
  // with another password until this tab is closed.
  showStatus(burned ? 'This paste has been deleted from the server. Do not close this tab until it is decrypted.' : '');
  const form = $('password-form');
  const input = $('password');
  form.hidden = false;
  input.focus();
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const button = form.querySelector('button');
    button.disabled = true;
    $('password-error').hidden = true;
    try {
      const plaintext = await decrypt(parsed, key, input.value);
      form.hidden = true;
      render(paste, plaintext, burned);
    } catch (err) {
      $('password-error').textContent = err instanceof DecryptError ? 'Wrong password.' : decryptMessage(err);
      $('password-error').hidden = false;
      input.select();
    } finally {
      button.disabled = false;
    }
  });
}

function render(paste, plaintext, burned) {
  text = plaintext;
  // textContent only: paste text is never interpreted as HTML.
  $('content').textContent = plaintext;
  const meta = [`Created ${relativeTime(paste.createdAt)}`];
  if (burned) meta.push('deleted from the server, copy it now if you need it');
  else meta.push(paste.expiresAt ? `expires ${relativeTime(paste.expiresAt)}` : 'never expires');
  $('meta').textContent = meta.join(' · ');
  if (!burned && recallDeleteToken(id)) $('delete').hidden = false;
  showStatus('');
  $('paste').hidden = false;
}

function notFoundMessage(err) {
  if (err instanceof ApiError && err.status === 404) {
    return 'This paste does not exist, has expired, or was already read.';
  }
  return `Could not load the paste: ${err.message}`;
}

function decryptMessage(err) {
  if (err instanceof DecryptError) {
    return `Could not decrypt this paste (${err.message}). Check that you copied the full link.`;
  }
  return `Could not decrypt this paste: ${err.message}`;
}

$('copy').addEventListener('click', (e) => copyText(text, e.currentTarget));
$('download').addEventListener('click', () => {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = `${id}.txt`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
});
$('delete').addEventListener('click', async (e) => {
  if (!confirm('Delete this paste for everyone? This cannot be undone.')) return;
  e.currentTarget.disabled = true;
  try {
    await api(`/api/pastes/${id}`, { method: 'DELETE', headers: { 'X-Delete-Token': recallDeleteToken(id) } });
    forgetDeleteToken(id);
    $('paste').hidden = true;
    showStatus('Paste deleted.');
  } catch (err) {
    e.currentTarget.disabled = false;
    showStatus(`Could not delete: ${err.message}`, true);
  }
});

load();
