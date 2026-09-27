import { test } from 'node:test';
import assert from 'node:assert/strict';
import { zoomAt, fitBox, columnsFor } from '../js/view.js';

test('zoomAt keeps the point under the cursor still', () => {
  const v = { x: 10, y: 20, s: 1 };
  const z = zoomAt(v, 110, 220, 2, 0.1, 4);
  // World point under (110, 220) before and after.
  assert.deepEqual([(110 - v.x) / v.s, (220 - v.y) / v.s], [(110 - z.x) / z.s, (220 - z.y) / z.s]);
  assert.equal(z.s, 2);
});

test('zoomAt clamps the scale', () => {
  assert.equal(zoomAt({ x: 0, y: 0, s: 1 }, 0, 0, 100, 0.1, 1.5).s, 1.5);
  assert.equal(zoomAt({ x: 0, y: 0, s: 1 }, 0, 0, 0.001, 0.1, 1.5).s, 0.1);
});

test('fitBox centres the box and respects max', () => {
  const v = fitBox(100, 50, 200, 100, 1000, 500, 10, 1);
  assert.equal(v.s, 5);
  // The box's centre lands on the window's centre.
  assert.deepEqual([v.x + 200 * v.s, v.y + 100 * v.s], [500, 250]);
  assert.equal(fitBox(0, 0, 10, 10, 1000, 1000, 1.5).s, 1.5);
});

test('columnsFor lays cells out in the window shape', () => {
  assert.equal(columnsFor(100, 1, 1, 1000, 1000), 10);
  assert.equal(columnsFor(100, 1, 1, 4000, 1000), 20);
  assert.equal(columnsFor(0, 1, 1, 1000, 1000), 1);
});
