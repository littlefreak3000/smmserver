import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { compress } from './ash0.mjs';
import { buildMetaBinary, parseMetaBinary, checkMeta, crc32, META_SIZE } from './meta.mjs';

// synthetic course-ish payloads: gamestyle "MW" at 0x6A, theme at 0x6D
function fakeCdt(theme) { const b = crypto.randomBytes(0x15000).fill(0, 0x100); b.write('MW', 0x6A, 'latin1'); b[0x6D] = theme; b.writeUInt16BE(300, 0x70); return b; }
const parts = { t0: crypto.randomBytes(2000), cdt: fakeCdt(2), sub: fakeCdt(5), t1: crypto.randomBytes(3000) };
const chunks = Object.fromEntries(Object.entries(parts).map(([k, v]) => [k, compress(v)]));
const bin = Buffer.concat([chunks.t0, chunks.cdt, chunks.sub, chunks.t1]);

test('crc32 known vector', () => assert.equal(crc32(Buffer.from('123456789')), 0xCBF43926));

test('build/parse round trip, field order is cdt,sub,t0,t1 and file order is t0,cdt,sub,t1', () => {
  const m = buildMetaBinary(chunks, { unk1: 2, unk2: 4, unk3: 3 });
  assert.equal(m.length, META_SIZE);
  assert.equal(m.readUInt32BE(8), chunks.cdt.length);     // 3rd word = cdt size
  assert.equal(m.readUInt32BE(16), chunks.t0.length);     // 5th word = thumbnail0 size
  const p = parseMetaBinary(m);
  assert.deepEqual([p.unk1, p.unk2, p.unk3], [2, 4, 3]);
  assert.equal(p.crcs.sub, crc32(chunks.sub));
});

test('checkMeta accepts a matching meta and reports course info', () => {
  const r = checkMeta(bin, buildMetaBinary(chunks));
  assert.equal(r.ok, true, r.notes.join('\n'));
  assert.deepEqual(r.info, { gamestyle: 'MW', theme: 2, subtheme: 5, autoscroll: parts.cdt[0x72], timelimit: 300 });
});

test('checkMeta detects wrong sizes and corrupted data', () => {
  const m = buildMetaBinary(chunks);
  m.writeUInt32BE(chunks.cdt.length + 1, 8);
  assert.equal(checkMeta(bin, m).ok, false);
  const bad = Buffer.from(bin); bad[bad.length - 9] ^= 0xFF;
  assert.equal(checkMeta(bad, buildMetaBinary(chunks)).ok, false);
});

test('checkMeta distinguishes decompressed-data CRCs from compressed-chunk CRCs', () => {
  const m = buildMetaBinary(chunks);
  m.writeUInt32BE(crc32(parts.cdt), 28);                  // 8th word = cdt crc, now of decompressed data
  const r = checkMeta(bin, m);
  assert.equal(r.ok, true);
  assert.ok(r.notes.some((n) => /cdt: .*DECOMPRESSED/.test(n)));
});

test('parseMetaBinary rejects wrong length', () => assert.throws(() => parseMetaBinary(Buffer.alloc(40))));
