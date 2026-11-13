#!/usr/bin/env node
// Compare a real uploaded .bin with the meta_binary the client sent for it.
//   node inspect-meta.mjs <course.bin> <meta file | hex string>
// Get both from a real upload (Cemu -> this server): the .bin lands in fake-s3/data/uploads/<id>.bin and
// the meta_binary is in Postgres: SELECT encode(meta_binary,'hex') FROM datastore.objects WHERE data_id = <id>;
import fs from 'node:fs';
import { checkMeta } from './meta.mjs';

const [binPath, metaArg] = process.argv.slice(2);
if (!binPath || !metaArg) { console.error('usage: inspect-meta.mjs <course.bin> <meta file | hex>'); process.exit(2); }
const metaBuf = fs.existsSync(metaArg) ? fs.readFileSync(metaArg) : Buffer.from(metaArg.replace(/^\\x|\s/g, ''), 'hex');
const r = checkMeta(fs.readFileSync(binPath), metaBuf);
const hex = (n) => '0x' + n.toString(16);
console.log(`unk1=${r.meta.unk1} unk2=${r.meta.unk2} unk3=${r.meta.unk3}`);
console.log('sizes (cdt, sub, t0, t1):', Object.values(r.meta.sizes).join(', '));
console.log('course:', JSON.stringify(r.info));
for (const n of r.notes) console.log(' -', n);
console.log(r.ok ? 'OK' : 'MISMATCH');
process.exit(r.ok ? 0 : 1);
