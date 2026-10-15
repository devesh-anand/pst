// Tests for the browser crypto module. Node's WebCrypto and CompressionStream
// match the browser APIs, so this runs the shipped file unmodified:
//   node --test web/test/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DecryptError, decrypt, encrypt, fromB64url, parseEnvelope, toB64url } from '../static/js/crypto.js';

const b64json = (obj) => Buffer.from(JSON.stringify(obj)).toString('base64url');

test('base64url round-trips arbitrary bytes', () => {
  for (const len of [0, 1, 2, 3, 31, 32, 33, 100_000]) {
    const bytes = crypto.getRandomValues(new Uint8Array(Math.min(len, 65536)));
    assert.deepEqual(fromB64url(toB64url(bytes)), bytes);
  }
  assert.equal(toB64url(new Uint8Array([251, 255])), '-_8');
  assert.throws(() => fromB64url('not valid!'), DecryptError);
});

test('encrypt then decrypt returns the original text', async () => {
  for (const text of ['x', 'hello 🔐 wörld', '\0\n\t', 'a'.repeat(200_000), '<script>alert(1)</script>']) {
    const { envelope, key } = await encrypt(text);
    assert.equal(await decrypt(parseEnvelope(envelope), key), text);
  }
});

test('key is 256 bits and every paste gets a fresh key and IV', async () => {
  const a = await encrypt('same');
  const b = await encrypt('same');
  assert.equal(fromB64url(a.key).length, 32);
  assert.notEqual(a.key, b.key);
  assert.notEqual(a.envelope, b.envelope);
  assert.notEqual(parseEnvelope(a.envelope).header.iv, parseEnvelope(b.envelope).header.iv);
});

test('envelope does not contain the plaintext or the key', async () => {
  const text = 'super secret api key sk_live_123';
  const { envelope, key } = await encrypt(text);
  assert.ok(!envelope.includes(key));
  assert.ok(!Buffer.from(envelope).toString().includes('sk_live'));
  for (const part of envelope.split('.')) {
    assert.ok(!Buffer.from(part, 'base64url').toString().includes('sk_live'));
  }
});

test('text is compressed before encryption', async () => {
  const text = 'the same line again\n'.repeat(5000);
  const { envelope } = await encrypt(text);
  assert.equal(parseEnvelope(envelope).header.z, 'deflate-raw');
  assert.ok(envelope.length < text.length / 20, `envelope ${envelope.length} bytes`);
});

test('wrong key fails with DecryptError', async () => {
  const { envelope } = await encrypt('hi');
  const other = toB64url(crypto.getRandomValues(new Uint8Array(32)));
  await assert.rejects(decrypt(parseEnvelope(envelope), other), DecryptError);
  await assert.rejects(decrypt(parseEnvelope(envelope), 'short'), DecryptError);
});

test('ciphertext tampering is detected', async () => {
  const { envelope, key } = await encrypt('hi there');
  const [h, c] = envelope.split('.');
  const bytes = fromB64url(c);
  bytes[0] ^= 1;
  await assert.rejects(decrypt(parseEnvelope(`${h}.${toB64url(bytes)}`), key), DecryptError);
});

test('header tampering is detected via additional data', async () => {
  const { envelope, key } = await encrypt('hi there');
  const [h, c] = envelope.split('.');
  const header = JSON.parse(Buffer.from(h, 'base64url').toString());
  delete header.z; // e.g. a server trying to change how the client interprets the data
  await assert.rejects(decrypt(parseEnvelope(`${b64json(header)}.${c}`), key), DecryptError);
});

test('password-protected pastes need the password', async () => {
  const { envelope, key } = await encrypt('guarded', 'p@ssw0rd');
  const parsed = parseEnvelope(envelope);
  assert.equal(parsed.needsPassword, true);
  assert.equal(parsed.header.kdf.name, 'PBKDF2-SHA256');
  assert.equal(parsed.header.kdf.iter, 600_000);
  await assert.rejects(decrypt(parsed, key, ''), DecryptError);
  await assert.rejects(decrypt(parsed, key, 'P@ssw0rd'), DecryptError);
  // A retry with the right password works on the same parsed envelope, which
  // is what the viewer relies on after a burn-after-read paste is consumed.
  assert.equal(await decrypt(parsed, key, 'p@ssw0rd'), 'guarded');
});

test('password alone is not enough without the link key', async () => {
  const { envelope } = await encrypt('guarded', 'p@ssw0rd');
  const other = toB64url(crypto.getRandomValues(new Uint8Array(32)));
  await assert.rejects(decrypt(parseEnvelope(envelope), other, 'p@ssw0rd'), DecryptError);
});

test('malformed envelopes are rejected before decryption', () => {
  const iv = toB64url(new Uint8Array(12));
  const bad = [
    undefined,
    '',
    'abc',
    'a.b.c',
    '.abc',
    'abc.',
    '!!.AAAA',
    `${b64json({ v: 2, iv })}.AAAA`,
    `${b64json({ v: 1 })}.AAAA`,
    `${b64json({ v: 1, iv, z: 'gzip' })}.AAAA`,
    `${b64json({ v: 1, iv, kdf: { name: 'scrypt', salt: 'AA', iter: 600000 } })}.AAAA`,
  ];
  for (const env of bad) assert.throws(() => parseEnvelope(env), DecryptError, String(env));
});

test('absurd KDF iteration counts are rejected so a paste cannot hang the tab', () => {
  for (const iter of [1, 99_999, 10_000_001, 1e12, 1.5e5 + 0.5]) {
    const env = `${b64json({ v: 1, iv: 'AAAAAAAAAAAAAAAA', kdf: { name: 'PBKDF2-SHA256', salt: 'AA', iter } })}.AAAA`;
    assert.throws(() => parseEnvelope(env), DecryptError, String(iter));
  }
});
