// Client-side encryption. The server only ever stores the envelope below; the
// 256-bit key lives in the URL fragment, which browsers never send to servers.
//
// Envelope v1: "<header>.<ciphertext>", both base64url.
//   header = JSON { v: 1, iv, z?: "deflate-raw", kdf?: { name, salt, iter } }
//   plaintext = JSON { text }, deflate-compressed before encryption when z is set
// The encoded header is AES-GCM additional data, so it can't be altered
// without decryption failing.
//
// With a paste password, the AES key is PBKDF2(urlKey || password), so the
// link alone is not enough to read the paste.

const PBKDF2_ITERATIONS = 600_000;
const MAX_PLAINTEXT_BYTES = 64 << 20; // guards against decompression bombs

const te = new TextEncoder();
const td = new TextDecoder();

export class DecryptError extends Error {}

export function toB64url(bytes) {
  let bin = '';
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function fromB64url(str) {
  if (typeof str !== 'string' || !/^[A-Za-z0-9_-]*$/.test(str)) {
    throw new DecryptError('invalid base64url');
  }
  let bin;
  try {
    bin = atob(str.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (str.length % 4)) % 4));
  } catch {
    // e.g. a truncated link: length % 4 == 1 is never valid base64
    throw new DecryptError('invalid base64url');
  }
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const canCompress = typeof CompressionStream === 'function' && typeof DecompressionStream === 'function';

async function pipe(bytes, transform, limit = Infinity) {
  const reader = new Blob([bytes]).stream().pipeThrough(transform).getReader();
  const chunks = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.length;
    if (size > limit) {
      await reader.cancel();
      throw new DecryptError('paste is too large to display');
    }
    chunks.push(value);
  }
  const out = new Uint8Array(size);
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.length;
  }
  return out;
}

async function deriveKey(rawKey, password, kdf) {
  if (!kdf) {
    return crypto.subtle.importKey('raw', rawKey, 'AES-GCM', false, ['encrypt', 'decrypt']);
  }
  const pw = te.encode(password);
  const material = new Uint8Array(rawKey.length + pw.length);
  material.set(rawKey);
  material.set(pw, rawKey.length);
  const base = await crypto.subtle.importKey('raw', material, 'PBKDF2', false, ['deriveKey']);
  return crypto.subtle.deriveKey(
    { name: 'PBKDF2', hash: 'SHA-256', salt: fromB64url(kdf.salt), iterations: kdf.iter },
    base,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  );
}

// encrypt returns the envelope to upload and the base64url key for the link.
export async function encrypt(text, password = '') {
  const rawKey = crypto.getRandomValues(new Uint8Array(32));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const header = { v: 1, iv: toB64url(iv) };
  if (password) {
    header.kdf = {
      name: 'PBKDF2-SHA256',
      salt: toB64url(crypto.getRandomValues(new Uint8Array(16))),
      iter: PBKDF2_ITERATIONS,
    };
  }
  let data = te.encode(JSON.stringify({ text }));
  if (canCompress) {
    data = await pipe(data, new CompressionStream('deflate-raw'));
    header.z = 'deflate-raw';
  }
  const headerB64 = toB64url(te.encode(JSON.stringify(header)));
  const key = await deriveKey(rawKey, password, header.kdf);
  const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: te.encode(headerB64) }, key, data);
  return { envelope: `${headerB64}.${toB64url(new Uint8Array(ct))}`, key: toB64url(rawKey) };
}

// parseEnvelope validates the envelope shape without decrypting it.
export function parseEnvelope(envelope) {
  const parts = typeof envelope === 'string' ? envelope.split('.') : [];
  if (parts.length !== 2 || !parts[0] || !parts[1]) throw new DecryptError('malformed paste');
  let header;
  try {
    header = JSON.parse(td.decode(fromB64url(parts[0])));
  } catch {
    throw new DecryptError('malformed paste header');
  }
  if (header?.v !== 1 || typeof header.iv !== 'string') throw new DecryptError('unsupported paste format');
  if (header.z !== undefined && header.z !== 'deflate-raw') throw new DecryptError('unsupported compression');
  if (header.kdf !== undefined) {
    const { name, salt, iter } = header.kdf;
    // Bound the iteration count so a crafted paste can't hang the browser.
    if (name !== 'PBKDF2-SHA256' || typeof salt !== 'string' || !Number.isInteger(iter) || iter < 100_000 || iter > 10_000_000) {
      throw new DecryptError('unsupported key derivation');
    }
  }
  return { header, headerB64: parts[0], ciphertext: fromB64url(parts[1]), needsPassword: !!header.kdf };
}

// decrypt returns the paste text, or throws DecryptError on a wrong key or password.
export async function decrypt(parsed, keyB64, password = '') {
  const rawKey = fromB64url(keyB64);
  if (rawKey.length !== 32) throw new DecryptError('invalid key');
  const key = await deriveKey(rawKey, password, parsed.header.kdf);
  let data;
  try {
    data = new Uint8Array(
      await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv: fromB64url(parsed.header.iv), additionalData: te.encode(parsed.headerB64) },
        key,
        parsed.ciphertext,
      ),
    );
  } catch {
    throw new DecryptError('wrong key or password');
  }
  if (parsed.header.z === 'deflate-raw') {
    if (!canCompress) throw new DecryptError('this browser cannot decompress the paste');
    data = await pipe(data, new DecompressionStream('deflate-raw'), MAX_PLAINTEXT_BYTES);
  }
  const body = JSON.parse(td.decode(data));
  if (typeof body?.text !== 'string') throw new DecryptError('malformed paste body');
  return body.text;
}
