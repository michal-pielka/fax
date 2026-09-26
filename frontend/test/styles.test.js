import { test } from 'node:test';
import assert from 'node:assert/strict';
import { effective, reconcile, rangeHas, toDocument } from '../js/styles.js';

const B = 1, U = 2;
const bytes = (...a) => new Uint8Array(a);

test('a space keeps its style only between two styled characters', () => {
  //                      a  _  b  _
  assert.deepEqual(effective('a b ', bytes(B, B, B, B)), bytes(B, B, B, 0));
  assert.deepEqual(effective('a b', bytes(B, B, 0)), bytes(B, 0, 0));
});

test('a newline never carries a style', () => {
  assert.deepEqual(effective('a\nb', bytes(B, B, B)), bytes(B, 0, B));
});

test('typing inside a styled word inherits its style', () => {
  // "bld" -> "bold", caret after the inserted "o"
  const next = reconcile('bld', 'bold', bytes(B, B, B), 0, 2);
  assert.deepEqual(next, bytes(B, B, B, B));
});

test('typing after a styled word starts plain', () => {
  const next = reconcile('hi', 'hix', bytes(B, B), 0, 3);
  assert.deepEqual(next, bytes(B, B, 0));
});

test('the typing mode wins over the neighbours', () => {
  const next = reconcile('ab', 'axb', bytes(0, 0), U, 2);
  assert.deepEqual(next, bytes(0, U, 0));
});

test('deleting drops the deleted characters\' styles', () => {
  const next = reconcile('abc', 'ac', bytes(0, B, U), 0, 1);
  assert.deepEqual(next, bytes(0, U));
});

test('a repeated character is placed by the caret', () => {
  // "aa" -> "aaa" with the caret at 1: the new "a" is the first one.
  const next = reconcile('aa', 'aaa', bytes(0, B), U, 1);
  assert.deepEqual(next, bytes(U, 0, B));
});

test('rangeHas ignores spaces and says null for only spaces', () => {
  assert.equal(rangeHas('a b', bytes(B, 0, B), 0, 3, B), true);
  assert.equal(rangeHas('a b', bytes(B, 0, 0), 0, 3, B), false);
  assert.equal(rangeHas('a  ', bytes(B, 0, 0), 1, 3, B), null);
});

test('toDocument emits one span per printed run', () => {
  assert.deepEqual(toDocument('hi bold x', bytes(U, U, 0, B, B, B, B, 0, 0)), {
    text: 'hi bold x',
    spans: [
      { start: 0, end: 2, style: { underline: true } },
      { start: 3, end: 7, style: { bold: true } },
    ],
  });
  assert.deepEqual(toDocument('plain', new Uint8Array(5)), { text: 'plain' });
});
