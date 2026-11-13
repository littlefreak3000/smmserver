#!/usr/bin/env node
// Build the network course object (<id>.bin) from an SMMDB level download.
//
//   node build-bin.mjs <level.zip | dir> --out <dir> [--id N] [--jpg thumbnail0|thumbnail1]
//
// Input is what smmdb's /course/<id>/download returns: course_data.cdt, course_data_sub.cdt,
// thumbnail0.tnl, thumbnail1.tnl (optionally inside a folder). Output <id>.bin is the four files
// ASH0-compressed and concatenated in the order the server/indexer expects:
//   thumbnail0, course_data, course_data_sub, thumbnail1
// (see SMMDB indexer.py / mm1-unarchive). No header: the first chunk starts at offset 0.

import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { parseArgs } from 'node:util';
import { pathToFileURL } from 'node:url';
import { compress } from './ash0.mjs';
import { refDecompress } from './ash-ref/ref-decode.mjs';
import { crc32, buildMetaBinary } from './meta.mjs';

export const ORDER = ['thumbnail0.tnl', 'course_data.cdt', 'course_data_sub.cdt', 'thumbnail1.tnl'];

export { crc32 };

/** Minimal zip reader (central directory; stored + deflate). Returns Map(basename -> Buffer). */
export function readZip(buf) {
  let eocd = -1;
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 65557); i--) if (buf.readUInt32LE(i) === 0x06054b50) { eocd = i; break; }
  if (eocd < 0) throw new Error('not a zip file');
  const n = buf.readUInt16LE(eocd + 10); let p = buf.readUInt32LE(eocd + 16);
  const files = new Map();
  for (let i = 0; i < n; i++) {
    if (buf.readUInt32LE(p) !== 0x02014b50) throw new Error('bad central directory');
    const method = buf.readUInt16LE(p + 10), csize = buf.readUInt32LE(p + 20), usize = buf.readUInt32LE(p + 24);
    const nlen = buf.readUInt16LE(p + 28), xlen = buf.readUInt16LE(p + 30), clen = buf.readUInt16LE(p + 32);
    const lho = buf.readUInt32LE(p + 42);
    const name = buf.toString('utf8', p + 46, p + 46 + nlen);
    p += 46 + nlen + xlen + clen;
    if (name.endsWith('/')) continue;
    const ds = lho + 30 + buf.readUInt16LE(lho + 26) + buf.readUInt16LE(lho + 28);
    const raw = buf.subarray(ds, ds + csize);
    const data = method === 0 ? Buffer.from(raw) : method === 8 ? zlib.inflateRawSync(raw) : null;
    if (!data) throw new Error(`unsupported zip method ${method} for ${name}`);
    if (data.length !== usize) throw new Error(`size mismatch for ${name}`);
    files.set(path.posix.basename(name), data);
  }
  return files;
}

export function loadParts(input) {
  let files;
  if (fs.statSync(input).isDirectory()) {
    files = new Map(ORDER.filter((f) => fs.existsSync(path.join(input, f))).map((f) => [f, fs.readFileSync(path.join(input, f))]));
  } else files = readZip(fs.readFileSync(input));
  for (const f of ORDER) if (!files.has(f)) throw new Error(`missing ${f} in input`);
  return files;
}

/** Sanity checks on the payloads; returns a list of warnings. */
export function validate(files) {
  const warn = [];
  for (const f of ['course_data.cdt', 'course_data_sub.cdt']) {
    const d = files.get(f);
    if (d.length !== 0x15000) warn.push(`${f}: size ${d.length.toString(16)} != 0x15000`);
    else if (d.readUInt32BE(8) !== crc32(d.subarray(0x10))) warn.push(`${f}: header CRC32 mismatch`);
  }
  for (const f of ['thumbnail0.tnl', 'thumbnail1.tnl']) {
    const d = files.get(f);
    if (d.length < 8) { warn.push(`${f}: too short`); continue; }
    if (d.readUInt32BE(0) !== crc32(d.subarray(4))) warn.push(`${f}: CRC32 mismatch`);
    if (d.readUInt32BE(4) > d.length - 8) warn.push(`${f}: length field exceeds data`);
    if (d.subarray(8, 10).toString('hex') !== 'ffd8') warn.push(`${f}: payload is not a JPEG`);
  }
  return warn;
}

export function buildBin(files, unk = {}) {
  const chunks = ORDER.map((f) => compress(files.get(f)));
  const bin = Buffer.concat(chunks);
  const meta = buildMetaBinary({ t0: chunks[0], cdt: chunks[1], sub: chunks[2], t1: chunks[3] }, unk);
  // verify like a converter would: split on "ASH0", decode with the reference port, compare.
  const starts = []; for (let i = bin.indexOf('ASH0'); i >= 0; i = bin.indexOf('ASH0', i + 1)) starts.push(i);
  const problems = [];
  if (starts.length !== 4) problems.push(`found ${starts.length} "ASH0" markers instead of 4; naive splitters would mis-split`);
  let off = 0;
  ORDER.forEach((f, i) => {
    const got = Buffer.from(refDecompress(bin.subarray(off, off + chunks[i].length)));
    if (!got.equals(files.get(f))) problems.push(`${f}: reference decoder output differs`);
    off += chunks[i].length;
  });
  return { bin, meta, chunkSizes: chunks.map((c) => c.length), problems };
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  const { values, positionals } = parseArgs({ allowPositionals: true, options: { out: { type: 'string' }, id: { type: 'string' }, jpg: { type: 'string' }, unk1: { type: 'string' }, unk2: { type: 'string' }, unk3: { type: 'string' } } });
  if (positionals.length !== 1 || !values.out) { console.error('usage: build-bin.mjs <level.zip|dir> --out <dir> [--id N] [--jpg thumbnail0|thumbnail1]'); process.exit(2); }
  const input = positionals[0];
  const id = values.id || /\[(\d+)\]/.exec(path.basename(input))?.[1] || /^(\d+)$/.exec(path.basename(input))?.[1];
  if (!id || !/^\d+$/.test(id)) { console.error('cannot tell the level/data ID; pass --id N'); process.exit(2); }
  const files = loadParts(input);
  for (const w of validate(files)) console.warn('warning:', w);
  const unk = Object.fromEntries(['unk1', 'unk2', 'unk3'].filter((k) => values[k] !== undefined).map((k) => [k, Number(values[k])]));
  const { bin, meta, chunkSizes, problems } = buildBin(files, unk);
  fs.mkdirSync(values.out, { recursive: true });
  fs.writeFileSync(path.join(values.out, `${id}.bin`), bin);
  fs.writeFileSync(path.join(values.out, `${id}.meta`), meta);   // 44-byte meta_binary (unk fields are guesses)
  if (values.jpg) {
    const t = files.get(`${values.jpg}.tnl`); if (!t) throw new Error('--jpg must be thumbnail0 or thumbnail1');
    fs.writeFileSync(path.join(values.out, `${id}.jpg`), t.subarray(8, 8 + t.readUInt32BE(4)));
  }
  console.log(`${id}.bin  ${bin.length} bytes  (chunks: ${chunkSizes.join(', ')})`);
  if (problems.length) { for (const p of problems) console.error('PROBLEM:', p); process.exit(1); }
  console.log('verified: all 4 chunks decode (reference port) to the original files');
}
