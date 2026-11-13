// ASH0 codec (Nintendo Wii/Wii U "ASH0" compression), derived from the decoder in
// SMMDB/ASH0/Ash.cpp (itself a transliteration of the console routine).
//
// Layout of one ASH0 chunk (all integers big-endian):
//   0x00  "ASH0"
//   0x04  u8 flags(0x30) | u24 uncompressed size
//   0x08  u32 byte offset of bit stream 2 (from chunk start)
//   0x0C  bit stream 1: symbol tree, then symbol codes (literals 0..0xFF, match lengths 0x100..0x1FF)
//   ....  bit stream 2: distance tree, then one distance code per match
// Bits are read MSB-first from 32-bit big-endian words.
// Tree serialisation (pre-order): bit 1 = internal node (left subtree, then right subtree);
//   bit 0 = leaf followed by its symbol (9 bits for stream 1, 11 bits for stream 2).
// A match symbol s copies (s - 0xFD) bytes (3..258) from distance d+1 back, d in 0..0x7FF.

const SYM_BITS = 9, DIST_BITS = 11;
const MIN_MATCH = 3, MAX_MATCH = 0x102, MAX_DIST = 0x800;

// ---------------------------------------------------------------- bit I/O
class BitWriter {
  constructor() { this.bytes = []; this.cur = 0; this.n = 0; }
  bit(b) { this.cur = ((this.cur << 1) | b) >>> 0; if (++this.n === 32) this.flushWord(); }
  bits(v, count) { for (let i = count - 1; i >= 0; i--) this.bit((v >>> i) & 1); }
  flushWord() {
    this.bytes.push((this.cur >>> 24) & 255, (this.cur >>> 16) & 255, (this.cur >>> 8) & 255, this.cur & 255);
    this.cur = 0; this.n = 0;
  }
  finish() { if (this.n > 0) { this.cur = (this.cur << (32 - this.n)) >>> 0; this.flushWord(); } return Buffer.from(this.bytes); }
}

class BitReader {
  constructor(buf, start) { this.buf = buf; this.pos = start; this.cur = 0; this.left = 0; }
  bit() {
    if (this.left === 0) {
      // reading past the end yields zeros (the reference decoder prefetches one word)
      this.cur = this.pos + 4 <= this.buf.length ? this.buf.readUInt32BE(this.pos) : 0;
      this.pos += 4; this.left = 32;
    }
    this.left--;
    return (this.cur >>> this.left) & 1;
  }
  bits(n) { let v = 0; for (let i = 0; i < n; i++) v = (v << 1) | this.bit(); return v; }
}

// ---------------------------------------------------------------- huffman
function buildTree(freq, minLeaves = 2) {
  // freq: Map symbol -> count. Guarantees >= 2 leaves (decoder needs an internal root).
  const f = new Map(freq);
  for (let s = 0; f.size < minLeaves; s++) if (!f.has(s)) f.set(s, 1);
  let nodes = [...f].map(([sym, w]) => ({ sym, w, order: sym }));
  let order = 1 << 20;
  while (nodes.length > 1) {
    nodes.sort((a, b) => a.w - b.w || a.order - b.order);
    const [a, b] = nodes.splice(0, 2);
    nodes.push({ left: a, right: b, w: a.w + b.w, order: order++ });
  }
  return nodes[0];
}

function writeTree(root, w, symBits) {
  const codes = new Map();
  (function walk(n, code, len) {
    if (n.left) {
      w.bit(1);
      walk(n.left, code * 2, len + 1);
      walk(n.right, code * 2 + 1, len + 1);
    } else {
      w.bit(0); w.bits(n.sym, symBits);
      codes.set(n.sym, { code, len });
    }
  })(root, 0, 0);
  return codes;
}

function putCode(w, c) { for (let i = c.len - 1; i >= 0; i--) w.bit(Math.floor(c.code / 2 ** i) % 2); }

// ---------------------------------------------------------------- LZ77
function lz(input) {
  const ops = []; // {lit} | {len, dist}  (dist is 1-based offset)
  const head = new Map(); const prev = new Int32Array(input.length).fill(-1);
  const key = (i) => input[i] | (input[i + 1] << 8) | (input[i + 2] << 16);
  const insert = (i) => { if (i + 2 < input.length) { const k = key(i); prev[i] = head.has(k) ? head.get(k) : -1; head.set(k, i); } };
  const longest = (i) => {
    let bestLen = 0, bestDist = 0;
    if (i + 2 >= input.length) return { bestLen, bestDist };
    let cand = head.has(key(i)) ? head.get(key(i)) : -1, tries = 256;
    const maxLen = Math.min(MAX_MATCH, input.length - i);
    while (cand >= 0 && i - cand <= MAX_DIST && tries-- > 0) {
      let l = 0; while (l < maxLen && input[cand + l] === input[i + l]) l++;
      if (l > bestLen) { bestLen = l; bestDist = i - cand; if (l === maxLen) break; }
      cand = prev[cand];
    }
    return { bestLen, bestDist };
  };
  let i = 0;
  while (i < input.length) {
    const m = longest(i);
    if (m.bestLen >= MIN_MATCH) {
      // one-step lazy matching
      insert(i);
      const n = i + 1 < input.length ? longest(i + 1) : { bestLen: 0 };
      if (n.bestLen > m.bestLen + 1) { ops.push({ lit: input[i] }); i++; continue; }
      ops.push({ len: m.bestLen, dist: m.bestDist });
      for (let k = 1; k < m.bestLen; k++) insert(i + k);
      i += m.bestLen;
    } else { insert(i); ops.push({ lit: input[i] }); i++; }
  }
  return ops;
}

// ---------------------------------------------------------------- public API
export function compress(input) {
  input = Buffer.from(input);
  if (input.length === 0 || input.length > 0xFFFFFF) throw new Error('ASH0: size must be 1..16777215');
  const ops = lz(input);
  const sf = new Map(), df = new Map();
  for (const o of ops) {
    if (o.lit !== undefined) sf.set(o.lit, (sf.get(o.lit) || 0) + 1);
    else {
      const s = 0xFD + o.len, d = o.dist - 1;
      sf.set(s, (sf.get(s) || 0) + 1); df.set(d, (df.get(d) || 0) + 1);
    }
  }
  const w1 = new BitWriter(), w2 = new BitWriter();
  const c1 = writeTree(buildTree(sf), w1, SYM_BITS);
  const c2 = writeTree(buildTree(df), w2, DIST_BITS);
  for (const o of ops) {
    if (o.lit !== undefined) putCode(w1, c1.get(o.lit));
    else { putCode(w1, c1.get(0xFD + o.len)); putCode(w2, c2.get(o.dist - 1)); }
  }
  const s1 = w1.finish();
  const s2 = Buffer.concat([w2.finish(), Buffer.alloc(4)]); // spare word: the console decoder prefetches one word
  const head = Buffer.alloc(0x0C);
  head.write('ASH0', 0, 'latin1');
  head.writeUInt32BE(((0x30 << 24) | input.length) >>> 0, 4);
  head.writeUInt32BE(0x0C + s1.length, 8);
  return Buffer.concat([head, s1, s2]);
}

function readTree(r, symBits, firstId) {
  // returns { root, left: Map, right: Map }  (internal ids start at firstId)
  const left = new Map(), right = new Map();
  let next = firstId;
  function node() {
    if (r.bit() === 0) return r.bits(symBits);
    const id = next++;
    left.set(id, node());
    right.set(id, node());
    return id;
  }
  return { root: node(), left, right, firstId };
}

export function isAsh0(buf) { return buf.length > 0x10 && buf.toString('latin1', 0, 3) === 'ASH'; }

export function decompress(buf) {
  if (!isAsh0(buf)) throw new Error('not ASH0');
  const size = buf.readUInt32BE(4) & 0xFFFFFF;
  const r1 = new BitReader(buf, 0x0C), r2 = new BitReader(buf, buf.readUInt32BE(8));
  const t1 = readTree(r1, SYM_BITS, 0x200), t2 = readTree(r2, DIST_BITS, 0x800);
  const out = Buffer.alloc(size); let o = 0;
  while (o < size) {
    let s = t1.root;
    while (s >= t1.firstId) s = r1.bit() ? t1.right.get(s) : t1.left.get(s);
    if (s < 0x100) { out[o++] = s; continue; }
    let d = t2.root;
    while (d >= t2.firstId) d = r2.bit() ? t2.right.get(d) : t2.left.get(d);
    let len = s - 0xFD; const from = o - (d + 1);
    if (from < 0) throw new Error('ASH0: bad distance');
    for (let k = 0; k < len && o < size; k++) { out[o] = out[from + k]; o++; }
  }
  return out;
}
