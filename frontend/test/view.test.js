import { test } from 'node:test';
import assert from 'node:assert/strict';
import { zoomAt, fitBox, columnsFor, slotAt, visibleSlots, slotUnder } from '../js/view.js';

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

const g = { pad: 10, pitchX: 100, pitchY: 200, w: 80 };

test('slotAt walks the grid row by row', () => {
  assert.deepEqual(slotAt(0, 3, g), [10, 10]);
  assert.deepEqual(slotAt(4, 3, g), [110, 210]);
});

test('visibleSlots covers exactly the slots on screen', () => {
  // Unzoomed at the origin, a 250 x 300 window sees columns 0-2, rows 0-1.
  assert.deepEqual(visibleSlots({ x: 0, y: 0, s: 1 }, 250, 300, 10, 10, g), { c0: 0, c1: 2, r0: 0, r1: 1 });
  // Clamped to the wall, and widened by the margin.
  assert.deepEqual(visibleSlots({ x: 0, y: 0, s: 1 }, 250, 300, 2, 1, g, 1), { c0: 0, c1: 1, r0: 0, r1: 0 });
  // Panned past the wall entirely: nothing.
  const off = visibleSlots({ x: -5000, y: 0, s: 1 }, 250, 300, 3, 3, g);
  assert.ok(off.c0 > off.c1);
});

test('slotUnder finds the receipt, not the gap', () => {
  const v = { x: 0, y: 0, s: 1 };
  assert.deepEqual(slotUnder(v, 150, 250, 3, g), { index: 4, dy: 40 });
  assert.equal(slotUnder(v, 100, 50, 3, g), null); // the gap after column 0
  assert.equal(slotUnder(v, 5, 5, 3, g), null);    // the margin
  // Zoomed: the same wall point at half size.
  assert.deepEqual(slotUnder({ x: 0, y: 0, s: 0.5 }, 75, 125, 3, g), { index: 4, dy: 40 });
});
