import { test } from 'node:test';
import assert from 'node:assert/strict';
import { rowsOf, longestPrefix } from '../js/paper.js';

// The same cases as backend/internal/doc TestRowsOf: both ends must agree.
test('rowsOf matches the server', () => {
  const cases = [
    ['', 1], ['hello', 1], ['a'.repeat(32), 1], ['a'.repeat(33), 2],
    ['a\nb\nc', 3], ['a\n\nb', 3], ['a'.repeat(65) + '\nb', 4],
  ];
  for (const [text, want] of cases) assert.equal(rowsOf(text, 32), want, JSON.stringify(text));
});

test('longestPrefix finds the boundary', () => {
  assert.equal(longestPrefix('abcdef', (p) => p.length <= 3), 'abc');
  assert.equal(longestPrefix('abcdef', () => true), 'abcdef');
  assert.equal(longestPrefix('abcdef', (p) => p === ''), '');
});
