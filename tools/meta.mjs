// MetaBinary for SMM course objects (the `meta_binary` column / DataStoreMetaInfo.metaBinary).
//
// Layout from mrexodia/SMMNEX smm_metabinary.bt (big-endian, 44 bytes):
//   u32 unk1            observed 1,2,3
//   u32 unk2            observed 0..5
//   u32 size  course_data.cdt   chunk (compressed size)
//   u32 size  course_data_sub   chunk
//   u32 size  thumbnail0        chunk
//   u32 size  thumbnail1        chunk
//   u32 unk3            observed 1,2,3
//   u32 crc32 course_data.cdt   chunk   (template says: of the COMPRESSED chunk; see checkMeta)
//   u32 crc32 course_data_sub   chunk
//   u32 crc32 thumbnail0        chunk
//   u32 crc32 thumbnail1        chunk
//
// The .bin on S3 is the four chunks concatenated in the order thumbnail0, cdt, cdt_sub, thumbnail1,
// so the sizes here are what lets a client slice it.

import { decompress, isAsh0 } from './ash0.mjs';

export const META_SIZE = 44;
export const FILE_ORDER = ['t0', 'cdt', 'sub', 't1'];   // order of chunks inside the .bin
export const META_ORDER = ['cdt', 'sub', 't0', 't1'];   // order of the size/crc fields in the meta

export function crc32(b) {
  let t = crc32.t;
  if (!t) t = crc32.t = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xEDB88320 ^ (c >>> 1) : c >>> 1; return c >>> 0; });
  let c = ~0; for (const x of b) c = t[(c ^ x) & 255] ^ (c >>> 8); return (~c) >>> 0;
}

/** chunks: { t0, cdt, sub, t1 } compressed ASH0 buffers. unk: { unk1, unk2, unk3 } (UNVERIFIED defaults). */
export function buildMetaBinary(chunks, unk = {}) {
  const b = Buffer.alloc(META_SIZE);
  let o = 0; const put = (v) => { b.writeUInt32BE(v >>> 0, o); o += 4; };
  put(unk.unk1 ?? 1); put(unk.unk2 ?? 0);
  for (const k of META_ORDER) put(chunks[k].length);
  put(unk.unk3 ?? 1);
  for (const k of META_ORDER) put(crc32(chunks[k]));
  return b;
}

export function parseMetaBinary(buf) {
  if (buf.length !== META_SIZE) throw new Error(`meta_binary is ${buf.length} bytes, expected ${META_SIZE}`);
  let o = 0; const get = () => { const v = buf.readUInt32BE(o); o += 4; return v; };
  const m = { unk1: get(), unk2: get(), sizes: {}, unk3: 0, crcs: {} };
  for (const k of META_ORDER) m.sizes[k] = get();
  m.unk3 = get();
  for (const k of META_ORDER) m.crcs[k] = get();
  return m;
}

/**
 * Check a meta_binary against the .bin it describes. Reports (not assumes) which CRC hypothesis holds:
 * CRC of the compressed chunk, or of the decompressed content.
 */
export function checkMeta(bin, metaBuf) {
  const m = parseMetaBinary(metaBuf);
  const notes = []; let ok = true;
  const total = META_ORDER.reduce((s, k) => s + m.sizes[k], 0);
  if (total !== bin.length) { ok = false; notes.push(`sizes sum to ${total}, .bin is ${bin.length} bytes`); }
  const chunks = {}; let off = 0;
  for (const k of FILE_ORDER) {
    const c = bin.subarray(off, off + m.sizes[k]); off += m.sizes[k]; chunks[k] = c;
    if (!isAsh0(c)) { ok = false; notes.push(`${k}: slice at ${off - m.sizes[k]} does not start with ASH0`); continue; }
    const comp = crc32(c);
    if (comp === m.crcs[k]) { notes.push(`${k}: CRC32 matches the compressed chunk`); continue; }
    let dec = null; try { dec = crc32(decompress(c)); } catch { /* ignore */ }
    if (dec === m.crcs[k]) notes.push(`${k}: CRC32 matches the DECOMPRESSED data (not the compressed chunk)`);
    else { ok = false; notes.push(`${k}: CRC32 ${m.crcs[k].toString(16)} matches neither compressed (${comp.toString(16)}) nor decompressed`); }
  }
  // context for decoding the unknown fields
  let info = {};
  try {
    const cdt = decompress(chunks.cdt), sub = decompress(chunks.sub);
    info = { gamestyle: cdt.toString('latin1', 0x6A, 0x6C), theme: cdt[0x6D], subtheme: sub[0x6D], autoscroll: cdt[0x72], timelimit: cdt.readUInt16BE(0x70) };
  } catch { /* ignore */ }
  return { ok, notes, meta: m, info };
}
