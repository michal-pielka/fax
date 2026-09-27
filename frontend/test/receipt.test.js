import { test } from 'node:test';
import assert from 'node:assert/strict';
import { stylesOf } from '../js/receipt.js';
import { toDocument } from '../js/styles.js';

test('stylesOf reverses toDocument', () => {
  const text = 'hi bold x', styles = new Uint8Array([0, 0, 0, 1, 1, 1, 5, 0, 2]);
  const doc = toDocument(text, styles);
  assert.deepEqual(stylesOf(doc.text, doc.spans), styles);
});

test('stylesOf without spans is plain', () => {
  assert.deepEqual(stylesOf('abc'), new Uint8Array(3));
});

test('stylesOf merges overlapping spans and stays in the text', () => {
  const spans = [
    { start: 0, end: 2, style: { bold: true } },
    { start: 1, end: 9, style: { underline: true } },
  ];
  assert.deepEqual(stylesOf('abc', spans), new Uint8Array([1, 3, 2]));
});
