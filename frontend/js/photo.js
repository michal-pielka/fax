/* A photo as the printer takes it: one bit a dot, packed in rows, eight dots
   a byte, leftmost in the high bit, 1 for white as PNG has it. Everything
   that decides how the photo looks happens here, on the sender's device, and
   the dots shown are the dots sent. */

/* The source rectangle and output height for a picture `srcW` x `srcH`
   scaled to `w` wide: width to the paper, height to match, and a picture
   taller than `maxH` cropped about its middle rather than shrunk to a strip. */
export function fit(srcW, srcH, w, maxH) {
  const scale = w / srcW;
  let h = Math.round(srcH * scale), sy = 0, sh = srcH;
  if (h > maxH) { sh = maxH / scale; sy = (srcH - sh) / 2; h = maxH; }
  return { sy, sh, h: Math.max(1, h) };
}

/* Luminance, levels, then Floyd-Steinberg. Levels stretch the picture so
   its darkest percent is black and its lightest white: a phone photo of a
   room is otherwise a grey smear at one bit. */
export function dither(rgba, w, h) {
  const n = w * h, lum = new Float32Array(n);
  for (let i = 0; i < n; i++) {
    const r = rgba[i * 4], g = rgba[i * 4 + 1], b = rgba[i * 4 + 2];
    lum[i] = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  }

  const hist = new Uint32Array(256);
  for (let i = 0; i < n; i++) hist[lum[i] | 0]++;
  let lo = 0, hi = 255, acc = 0;
  for (; lo < 255 && acc + hist[lo] < n * 0.01; lo++) acc += hist[lo];
  acc = 0;
  for (; hi > lo && acc + hist[hi] < n * 0.01; hi--) acc += hist[hi];
  const span = Math.max(1, hi - lo);

  const stride = (w + 7) >> 3, bits = new Uint8Array(stride * h);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = y * w + x;
      const old = Math.min(255, Math.max(0, (lum[i] - lo) * 255 / span));
      const white = old >= 128;
      if (white) bits[y * stride + (x >> 3)] |= 0x80 >> (x & 7);
      const err = old - (white ? 255 : 0);
      if (x + 1 < w) lum[i + 1] += err * 7 / 16;
      if (y + 1 < h) {
        if (x > 0) lum[i + w - 1] += err * 3 / 16;
        lum[i + w] += err * 5 / 16;
        if (x + 1 < w) lum[i + w + 1] += err * 1 / 16;
      }
    }
  }

  return bits;
}

/* The packed dots back out as opaque RGBA, for showing them on a canvas. */
export function unpack(bits, w, h, rgba) {
  const stride = (w + 7) >> 3;
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const v = bits[y * stride + (x >> 3)] & (0x80 >> (x & 7)) ? 255 : 0;
      const o = (y * w + x) * 4;
      rgba[o] = rgba[o + 1] = rgba[o + 2] = v;
      rgba[o + 3] = 255;
    }
  }
  return rgba;
}

/* The picture as the server wants it: a one-bit greyscale PNG, written by
   hand. The dots have no redundancy to compress, so the deflate stream is
   stored blocks, which keeps this to a few dozen lines and no library. */
export function encodePNG(bits, w, h) {
  const stride = (w + 7) >> 3;
  const raw = new Uint8Array((stride + 1) * h);
  for (let y = 0; y < h; y++) raw.set(bits.subarray(y * stride, (y + 1) * stride), y * (stride + 1) + 1);

  /* zlib: header, stored blocks of up to 65535 bytes, adler32. */
  const blocks = Math.max(1, Math.ceil(raw.length / 65535));
  const z = new Uint8Array(2 + raw.length + blocks * 5 + 4);
  let o = 0;
  z[o++] = 0x78; z[o++] = 0x01;
  for (let i = 0; i < blocks; i++) {
    const start = i * 65535, len = Math.min(65535, raw.length - start);
    z[o++] = i === blocks - 1 ? 1 : 0;
    z[o++] = len & 255; z[o++] = len >> 8; z[o++] = ~len & 255; z[o++] = (~len >> 8) & 255;
    z.set(raw.subarray(start, start + len), o); o += len;
  }
  let a = 1, b = 0;
  for (let i = 0; i < raw.length; i++) { a = (a + raw[i]) % 65521; b = (b + a) % 65521; }
  z[o++] = b >> 8; z[o++] = b & 255; z[o++] = a >> 8; z[o++] = a & 255;

  const be32 = (v) => [v >>> 24 & 255, v >>> 16 & 255, v >>> 8 & 255, v & 255];
  const chunk = (type, data) => {
    const t = [...type].map(c => c.charCodeAt(0));
    const body = new Uint8Array([...t, ...data]);
    return [...be32(data.length), ...body, ...be32(crc32(body))];
  };
  const ihdr = [...be32(w), ...be32(h), 1, 0, 0, 0, 0]; // depth 1, greyscale

  return new Blob([new Uint8Array([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
    ...chunk('IHDR', ihdr), ...chunk('IDAT', z), ...chunk('IEND', []),
  ])], { type: 'image/png' });
}

const crcTable = new Int32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c;
});

export function crc32(bytes) {
  let c = -1;
  for (let i = 0; i < bytes.length; i++) c = crcTable[(c ^ bytes[i]) & 255] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}
