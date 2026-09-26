import { test } from 'node:test';
import assert from 'node:assert/strict';
import { inflateSync } from 'node:zlib';
import { fit, dither, unpack, encodePNG, crc32 } from '../js/photo.js';

test('crc32 matches the standard check value', () => {
  assert.equal(crc32(new TextEncoder().encode('123456789')), 0xcbf43926);
});

test('fit scales to the width and crops a tall picture to a square', () => {
  assert.deepEqual(fit(768, 384, 384, 384), { sy: 0, sh: 384, h: 192 });
  assert.deepEqual(fit(384, 1000, 384, 384), { sy: 308, sh: 384, h: 384 });
});

// Reads the PNG back with a real inflater, so a wrong length, checksum or
// stored-block header fails here rather than on the server.
test('encodePNG writes a one-bit PNG that inflates to the same rows', async () => {
  const w = 20, h = 3, stride = 3;
  const bits = Uint8Array.from({ length: stride * h }, (_, i) => (i * 37) & 0xff);
  const png = new Uint8Array(await encodePNG(bits, w, h).arrayBuffer());

  assert.deepEqual([...png.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

  const view = new DataView(png.buffer);
  const chunks = {};
  for (let o = 8; o < png.length;) {
    const len = view.getUint32(o), type = String.fromCharCode(...png.subarray(o + 4, o + 8));
    const body = png.subarray(o + 4, o + 8 + len);
    assert.equal(view.getUint32(o + 8 + len), crc32(body), `${type} crc`);
    chunks[type] = png.subarray(o + 8, o + 8 + len);
    o += 12 + len;
  }

  const ihdr = new DataView(chunks.IHDR.buffer, chunks.IHDR.byteOffset);
  assert.equal(ihdr.getUint32(0), w);
  assert.equal(ihdr.getUint32(4), h);
  assert.equal(chunks.IHDR[8], 1, 'bit depth');

  const raw = inflateSync(chunks.IDAT);
  for (let y = 0; y < h; y++) {
    assert.equal(raw[y * (stride + 1)], 0, 'filter byte');
    assert.deepEqual([...raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1))],
      [...bits.subarray(y * stride, (y + 1) * stride)]);
  }
});

test('dither keeps a flat picture\'s tone', () => {
  const w = 16, h = 2;
  const flat = (v) => new Uint8ClampedArray(w * h * 4).fill(v);
  assert.ok(dither(flat(255), w, h).every(b => b === 0xff), 'white stays white');
  assert.ok(dither(flat(0), w, h).every(b => b === 0), 'black stays black');
});

test('unpack is the inverse of the packing', () => {
  const rgba = unpack(new Uint8Array([0b10100000]), 3, 1, new Uint8ClampedArray(12));
  assert.deepEqual([...rgba], [255, 255, 255, 255, 0, 0, 0, 255, 255, 255, 255, 255]);
});
