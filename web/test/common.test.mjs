import { test } from 'node:test';
import assert from 'node:assert/strict';
import { expiryLabel, formatBytes, relativeTime } from '../static/js/common.js';

test('expiryLabel names every server expiry', () => {
  assert.equal(expiryLabel(0), 'Never');
  assert.equal(expiryLabel(600), '10 minutes');
  assert.equal(expiryLabel(604800), '1 week');
  assert.equal(expiryLabel(31536000), '1 year');
  assert.equal(expiryLabel(7200), '2 hours');
});

test('formatBytes picks a sensible unit', () => {
  assert.equal(formatBytes(0), '0 B');
  assert.equal(formatBytes(1023), '1023 B');
  assert.equal(formatBytes(1536), '1.5 KB');
  assert.equal(formatBytes(2 * 1024 * 1024), '2.00 MB');
});

test('relativeTime describes past and future', () => {
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  assert.equal(relativeTime(new Date(Date.now() + 7 * 86400e3 + 5e3)), rtf.format(1, 'week'));
  assert.equal(relativeTime(new Date(Date.now() - 3 * 3600e3 - 5e3)), rtf.format(-3, 'hour'));
});
