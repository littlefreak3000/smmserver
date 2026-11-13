#!/usr/bin/env node
// Export the archived levels of a catalog as JSON Lines shaped like the metadata API's records, the input that
// `smm-server index build` reads. Handy for trying the index on real data without a full dump.
//
//   node tools/export-catalog-jsonl.mjs <data folder or catalog.sqlite> <out.jsonl>
import fs from 'node:fs';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';

const [src, out] = process.argv.slice(2);
if (!src || !out) { console.error('usage: export-catalog-jsonl.mjs <data folder | catalog.sqlite> <out.jsonl>'); process.exit(2); }

const file = fs.statSync(src).isDirectory() ? path.join(src, 'catalog.sqlite') : src;
const db = new DatabaseSync(file, { readOnly: true });

const rows = db.prepare(`
  SELECT s.data_id id, s.url, s.stars, o.name, o.owner, o.creation_date created,
         (SELECT total_value FROM object_ratings r WHERE r.data_id = s.data_id AND r.slot = 2) clears,
         (SELECT total_value FROM object_ratings r WHERE r.data_id = s.data_id AND r.slot = 3) attempts,
         (SELECT total_value FROM object_ratings r WHERE r.data_id = s.data_id AND r.slot = 4) failures
  FROM level_sources s JOIN objects o ON o.data_id = s.data_id ORDER BY s.data_id`).all();

const lines = rows.map((r) => JSON.stringify({
  url: r.url, name: r.name, creator: '', levelid: r.id, creatorid: r.owner,
  clears: r.clears, failures: r.failures, total_attempts: r.attempts, clearrate: r.attempts ? r.clears / r.attempts : 0,
  uploadTime: new Date(r.created.replace(' ', 'T').replace('+00:00', 'Z')).toISOString(),
  world_record_ms: 0, stars: r.stars, downloads: 0,
}));

fs.writeFileSync(out, lines.join('\n') + '\n');
console.log(`${rows.length} levels written to ${out}`);
