#!/usr/bin/env node
// Build / extend a home-server archive from SMMDB level downloads.
//
//   node import-archive.mjs --archive <dir> [--meta levels.json]... [options] <level.zip | dir>...
//
// Produces (or adds to):
//   <archive>/catalog.sqlite   level catalog (schema: server/database/schema.sql, shared with the Go server)
//   <archive>/courses/<id>.bin course objects (4 ASH0 chunks, see build-bin.mjs)
// Run the server with SMM_DATA_DIR=<archive>.
//
// --meta takes SMMDB API JSON (an array like /api/course/search/<n>, matched by levelid). Without metadata a
// level is imported with what the course file itself says (name, creation time) and owner 0.
//
// Options (UNVERIFIED defaults; see tools/README.md):
//   --data-type N        objects.data_type for courses              (default 3)
//   --ranking-apps A,B   custom-ranking application IDs that get the star count (default 0)
//   --default-owner PID  owner when no metadata                     (default 0)

import fs from 'node:fs';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { DatabaseSync } from 'node:sqlite';
import { loadParts, validate, buildBin } from './build-bin.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
export const SCHEMA_PATH = path.join(here, '..', 'server', 'database', 'schema.sql');

const I = (n) => BigInt(Math.trunc(Number(n) || 0));

/** UTC text in the format the Go driver reads/writes (_time_format=sqlite). */
export function sqliteTime(ms) {
  return new Date(ms).toISOString().replace('T', ' ').replace(/\.000Z$/, '+00:00').replace(/Z$/, '+00:00');
}

export function openArchive(dir) {
  fs.mkdirSync(path.join(dir, 'courses'), { recursive: true });
  const db = new DatabaseSync(path.join(dir, 'catalog.sqlite'));
  db.exec('PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL;');
  db.exec(fs.readFileSync(SCHEMA_PATH, 'utf8'));
  return db;
}

function cdtInfo(cdt) {
  const name = Buffer.from(cdt.subarray(0x28, 0x6A)).swap16().toString('utf16le').split('\0')[0];
  const y = cdt.readUInt16BE(0x10), mo = cdt[0x12], d = cdt[0x13], h = cdt[0x14], mi = cdt[0x15];
  const ms = Date.UTC(y, mo - 1, d, h, mi);
  return { name, created: y >= 2015 && y < 2100 && mo >= 1 && mo <= 12 ? ms : Date.now() };
}

/**
 * Import one level. `files` is the Map from loadParts(); `m` is the SMMDB metadata object or undefined.
 * Writes courses/<id>.bin and the catalog rows (replacing any previous import of the same id).
 */
export function importLevel(db, archiveDir, id, files, m, opts = {}) {
  const dataType = opts.dataType ?? 3, apps = opts.rankingApps ?? [0], defaultOwner = opts.defaultOwner ?? 0;
  const { bin, meta, problems } = buildBin(files);
  if (problems.length) throw new Error(`level ${id}: ${problems.join('; ')}`);

  const target = path.join(archiveDir, 'courses', `${id}.bin`);
  fs.writeFileSync(`${target}.tmp`, bin);
  fs.renameSync(`${target}.tmp`, target);

  const info = cdtInfo(files.get('course_data.cdt'));
  const name = m?.name ?? info.name;
  const created = m?.creation ? Number(m.creation) * 1000 : info.created;
  const updated = created; // SMMDB's last_updated is its crawler's refresh time, not a level edit
  const owner = m?.ownerid ?? defaultOwner;
  const stars = Number(m?.stars) || 0;
  const t = sqliteTime(created), u = sqliteTime(updated);
  const result = { id, size: bin.length, stars, record: false };

  db.exec('BEGIN IMMEDIATE');
  try {
    for (const tbl of ['objects', 'object_ratings', 'object_custom_rankings', 'buffer_queues', 'course_records']) {
      db.prepare(`DELETE FROM ${tbl} WHERE data_id = ?`).run(I(id));
    }

    db.prepare(`INSERT INTO objects (data_id, upload_completed, owner, size, name, data_type, meta_binary, permission,
        permission_recipients, delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags,
        persistence_slot_id, extra_data, creation_date, update_date)
      VALUES (?, 1, ?, ?, ?, ?, ?, 0, '[]', 3, '[]', 0, 0, 0, '[]', 0, '[]', ?, ?)`)
      .run(I(id), I(owner), I(bin.length), name, I(dataType), meta, t, u);

    // slots as the SMMDB indexer reads them: 0 plays, 2 clears, 3 attempts, 4 failures (1 unknown)
    const counters = [m?.user_plays, 0, m?.clears, m?.attempts, m?.failures].map((v) => Number(v) || 0);
    const rating = db.prepare(`INSERT INTO object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value,
        range_min, range_max, period_hour, period_duration, total_value, count) VALUES (?, ?, 0, 0, 0, 0, 0, 2147483647, 0, 0, ?, ?)`);
    counters.forEach((v, slot) => rating.run(I(id), I(slot), I(v), I(v)));

    if (stars > 0) {
      const rank = db.prepare('INSERT INTO object_custom_rankings (data_id, application_id, value) VALUES (?, ?, ?)');
      for (const a of apps) rank.run(I(id), I(a), I(stars));
      // slot 2 buffer queue = one entry per star (the indexer counts them); real entries are lost, so synthesize
      const buf = db.prepare('INSERT INTO buffer_queues (data_id, slot, creation_date, buffer) VALUES (?, 2, ?, ?)');
      for (let i = 0; i < stars; i++) { const b = Buffer.alloc(4); b.writeUInt32BE(i); buf.run(I(id), t, b); }
    }

    // course record only if the metadata carries an actual clear score
    if (m?.first_clear_pid && m?.best_clear_pid && m?.best_clear_score != null) {
      db.prepare(`INSERT INTO course_records (data_id, slot, first_pid, best_pid, best_score, creation_date, update_date)
        VALUES (?, 0, ?, ?, ?, ?, ?)`).run(I(id), I(m.first_clear_pid), I(m.best_clear_pid), I(m.best_clear_score),
        m.first_clear_time ? sqliteTime(Number(m.first_clear_time) * 1000) : t, m.best_clear_time ? sqliteTime(Number(m.best_clear_time) * 1000) : t);
      result.record = true;
    }
    db.exec('COMMIT');
  } catch (e) { db.exec('ROLLBACK'); throw e; }
  return result;
}

export function loadMeta(files) {
  const map = new Map();
  for (const f of files) {
    const j = JSON.parse(fs.readFileSync(f, 'utf8'));
    for (const lvl of Array.isArray(j) ? j : (j.levels ?? [j])) if (lvl?.levelid != null) map.set(String(lvl.levelid), lvl);
  }
  return map;
}

function levelId(input) {
  const base = path.basename(input);
  return /\[(\d+)\]/.exec(base)?.[1] ?? /^(\d+)(?:\.zip)?$/.exec(base)?.[1]
    ?? /^(\d+)$/.exec(path.basename(path.dirname(path.resolve(input))))?.[1];   // e.g. 942697/level.zip
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  const { values, positionals } = parseArgs({
    allowPositionals: true,
    options: { archive: { type: 'string' }, meta: { type: 'string', multiple: true }, 'data-type': { type: 'string' }, 'ranking-apps': { type: 'string' }, 'default-owner': { type: 'string' } },
  });
  if (!values.archive || positionals.length === 0) {
    console.error('usage: import-archive.mjs --archive <dir> [--meta levels.json]... [--data-type N] [--ranking-apps A,B] [--default-owner PID] <level.zip|dir>...');
    process.exit(2);
  }
  const opts = {
    dataType: values['data-type'] !== undefined ? Number(values['data-type']) : undefined,
    rankingApps: values['ranking-apps'] !== undefined ? values['ranking-apps'].split(',').map(Number) : undefined,
    defaultOwner: values['default-owner'] !== undefined ? Number(values['default-owner']) : undefined,
  };
  const metas = loadMeta(values.meta ?? []);
  const db = openArchive(values.archive);
  let ok = 0, failed = 0, noRecord = 0;
  for (const input of positionals) {
    const id = levelId(input);
    try {
      if (!id) throw new Error('cannot tell the level ID from the name (expected "[id] name.zip" or a folder named with the id)');
      const files = loadParts(input);
      for (const w of validate(files)) console.warn(`  warning (${id}): ${w}`);
      const m = metas.get(id);
      const r = importLevel(db, values.archive, id, files, m, opts);
      ok++; if (!r.record) noRecord++;
      console.log(`${id}: ${r.size} bytes, ${r.stars} stars${m ? '' : ' (no metadata: name/time from course file, owner ' + (opts.defaultOwner ?? 0) + ')'}`);
    } catch (e) { failed++; console.error(`${input}: ${e.message}`); }
  }
  db.close();
  console.log(`imported ${ok}, failed ${failed}; ${noRecord} without a course record (metadata had no clear score)`);
  process.exit(failed ? 1 : 0);
}
