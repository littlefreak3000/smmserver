import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { compress, decompress } from './ash0.mjs';
import { refDecompress } from './ash-ref/ref-decode.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));

function check(name, data) {
  test(`ASH0 round trip: ${name}`, () => {
    const c = compress(data);
    assert.equal(c.toString('latin1', 0, 4), 'ASH0');
    assert.equal(c.readUInt32BE(4) & 0xFFFFFF, data.length);
    assert.equal(c.readUInt32BE(8) % 4, 0, 'stream 2 offset is word aligned');
    assert.deepEqual(decompress(c), data, 'own decoder');
    assert.deepEqual(Buffer.from(refDecompress(c)), data, 'reference decoder (port of Ash.cpp)');
  });
}

check('1 byte', Buffer.from([7]));
check('2 identical bytes', Buffer.from([9, 9]));
check('single repeated symbol, 100k zeros', Buffer.alloc(100000));
check('short text', Buffer.from('hello hello hello world, hello ASH0 world'));
check('random 5000 (incompressible, no matches)', crypto.randomBytes(5000));
check('random 300k', crypto.randomBytes(300000));
check('long period pattern (max length matches)', Buffer.from(Array.from({ length: 70000 }, (_, i) => (i * 7) % 13)));
check('every byte value', Buffer.from(Array.from({ length: 256 * 3 }, (_, i) => i % 256)));
{
  // skewed distribution -> deep Huffman tree (codes longer than 32 bits would break shifts)
  const skew = []; let n = 1;
  for (let s = 0; s < 40; s++) { for (let k = 0; k < Math.min(n, 60000); k++) skew.push(s); n *= 2; }
  check('fibonacci-ish skew, deep tree', Buffer.from(skew));
}

const sample = path.join(here, '..', 'samples', '942697', 'x');
for (const f of ['course_data.cdt', 'course_data_sub.cdt', 'thumbnail0.tnl', 'thumbnail1.tnl']) {
  const p = path.join(sample, f);
  if (fs.existsSync(p)) check(`real SMMDB sample ${f}`, fs.readFileSync(p));
}
