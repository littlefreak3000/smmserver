# Whole-archive index mode

Design note and status for drawing lists from every archived level instead of a small local pool.

**Status.** Built and tested against a fake upstream and a 404-level real index. Not tested with the full archive (no metadata dump has been obtained yet) and not tried in the game.

Built: the index builder and sampler (`server/index`), stubs and lazy completion (`levelimport`, `pool/lazy.go`), the prefetch queue, dead marking, list selection (`server/selection`), seeded draws and the seed page (`server/challenge`), the wiring (`selection_local.go`), and the optional size limit for downloaded levels (`SMM_CACHE_MAX_MB`, see section 6). The size and speed figures below were measured on a synthetic index of 2 million rows of realistic size. Everything about the full dump and about the game is marked as unverified.

## 1. Goal

In pool mode the server can only pick random levels from a small local *pool*: about 100 levels on a new install, 404 in a larger test archive, at most about 1,000 by default. The real archive holds roughly 10 million levels. Index mode draws lists such as the 100 Mario Challenge and "random level of this difficulty" from **all** of them, while the server keeps downloading only the few levels a player opens.

Non-goals: storing every level file (about 470 GB), bulk-scraping the volunteer services, supporting real consoles.

## 2. Pool mode and index mode

| | Pool mode | Index mode (this design) |
|---|---|---|
| Levels a random list can contain | those in the local catalog (about 100-1,000) | all ~10 million |
| Filling it | the API returns one level per call, about half of them empty; about 1.5 s per kept level | nothing to fill: the index is loaded once |
| Extra disk | about 2 KB per level (catalog) | about 4-4.5 GB once, then about 2 KB per level actually used |
| Needs | nothing | a metadata dump (see section 7) |

## 3. Two stores

- **Index** (`data/index.sqlite`, read-only, lean): one small row per level in the whole archive. It is the *universe*, and holds only what is needed to list a level and fetch it later.
- **Catalog** (`data/catalog.sqlite`): the levels the server has shown or served. Rows here are complete (size, `meta_binary`, ratings, rankings) and the file is on disk. Everything the game queries keeps working unchanged.

A level moves through these states:

```
indexed ──a list picks it──▶ stub ──file fetched──▶ complete ──size limit reached──▶ removed
(index only)                 (catalog row, no file)  (row + file)                     (back to index only)
```

## 4. Index data model (measured)

```sql
CREATE TABLE levels (
  bucket   INTEGER NOT NULL,   -- clear rate in whole percent, 0..100  (clears / attempts)
  seq      INTEGER NOT NULL,   -- dense 0..n-1 inside the bucket
  id       INTEGER NOT NULL,   -- level / data ID
  name     TEXT    NOT NULL,
  creator  INTEGER NOT NULL,   -- creator PID
  clears INTEGER NOT NULL, attempts INTEGER NOT NULL, stars INTEGER NOT NULL,
  upload   INTEGER NOT NULL,   -- unix seconds
  expires  INTEGER NOT NULL, rev INTEGER NOT NULL,
  sig      BLOB    NOT NULL,   -- the signed part of the Wayback capture URL (256 bytes, random, incompressible)
  dead     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, seq)
) WITHOUT ROWID;
CREATE TABLE bucket_counts (bucket INTEGER PRIMARY KEY, n INTEGER NOT NULL);
CREATE UNIQUE INDEX levels_id     ON levels(id);                 -- GetMeta / "open this level code"
CREATE INDEX        levels_stars  ON levels(stars DESC, id);     -- top lists
CREATE INDEX        levels_upload ON levels(upload DESC, id);    -- newest list
```

The capture URL is rebuilt from constants (`https://d2sno3mhmk1ekx.cloudfront.net/10.WUP_AMAJ_datastore/ds/1/data/<id, 11 digits>-<rev, 5 digits>?Expires=<expires>&Signature=<sig>&Key-Pair-Id=<key pair>`), with the key pair id kept in a one-row `meta` table.

**Measured on 2,000,000 synthetic rows:** 385 bytes per level in total (337 for the table, 49 for the three indexes), so **3.85 GB for 10 million levels**. Real names are longer than the synthetic ones (Japanese names take 3 bytes per character), so budget **4-4.5 GB**.

Variant: drop `sig` (about 256 of the 385 bytes) and look the capture URL up in the Wayback Machine's CDX search at fetch time. That makes the index about 1-1.3 GB, at the price of one extra request per fetch to a service that timed out in an earlier test. It is an option, not the default.

## 5. Choosing levels

The key is the dense `seq`: a random level in a clear-rate range is **one primary-key lookup**, not a scan.

1. **Difficulty request** (`ScoreRangeCascadedSearchObject`, extra data `[app, min, max, flag]`). Buckets `min..max` are the range (assuming the difficulty ranking is clear-rate percent, which is still a guess). Pick bucket *b* with probability proportional to its level count (101 counts kept in memory), pick `seq` uniformly in `[0, n_b)`, and read the row. Repeat until the requested number of distinct levels is reached. The result is **uniform over every level in the range**, so the mix follows the real archive instead of the equal-sized bands the pool uses.
2. **Recommended list:** the same with the range 0-100 (uniform over everything).
3. **Newest and top stars:** the two secondary indexes (0.1 ms for 100 rows).
4. **Open by ID:** `levels_id` (25 microseconds), then materialise the level.
5. **Dead levels:** a pick with `dead = 1` is skipped and redrawn (a tombstone, so `seq` stays dense).
6. **No repeats (optional):** a small `served(pid, id)` table in the catalog, trimmed to the last few thousand ids per player, makes a repeat unlikely until the archive is exhausted, which at 10 million levels never happens.
7. **Empty range:** falls back to any level, as in pool mode.

Measured on 2 million rows: about **31 microseconds per pick**, 3.5 ms for 100 picks, in every range tested. The obvious `ORDER BY RANDOM() LIMIT 100` over a range took 300 ms at 2 million rows, so about 1.5 s at 10 million, which is too slow inside a game request.

## 6. From pick to playable level

**Stub.** The game needs a `DataStoreMetaInfo` for each listed level. For picked levels the server inserts a *stub* into the catalog (about 2 KB: objects, ratings, rankings and `level_sources` with `fetched = NULL`), using the same importer code as pool mode minus the file. Lists are then answered by the existing functions. `size` is 0 and `meta_binary` is empty until the file is fetched.

**Prefetch.** As soon as a list is returned, a background worker fetches the listed levels' files in list order, paced as in pool mode (one Wayback request per second), and completes each stub: it writes `courses/<id>.bin`, sets `size` and `meta_binary` (both need the file), and validates the four ASH0 chunks. A 100-level list is fully fetched in about 100 s, long before a player finishes the first few levels.

**Ensure before download (the safety net).** Any request that needs the exact `size` or `meta_binary` (`GetMeta*`, `PrepareGetObject`, `GetObjectInfos`) first calls `ensureFetched(id)`: a single-flight, queue-jumping synchronous fetch (about 1.4 s measured in pool mode). It is implemented by wrapping the function pointers assigned in `register_common_secure_server_protocols.go` and by a call in `get_object_infos.go`. List code keeps calling the plain functions, so a list never triggers a download by itself.

**Failure.** A level with no usable capture is marked `dead` (in the index, or in a small side table when the index file should stay read-only), removed from the catalog, and the pick is redrawn.

**Size limit.** `SMM_CACHE_MAX_MB` (off by default) removes the oldest downloaded levels, by fetch time, when the level files exceed the limit. Their rows and files go, and the level is only an index entry again until it is picked or requested. Levels a local account starred or made are kept, and levels players uploaded to this server are never overwritten by archive levels.

## 7. Where the index comes from

| Source | Status |
|---|---|
| The public API | One level per call; about 18 million calls for everything, which is months at a polite rate. **Not acceptable.** |
| Archive Team `courses.jsonl.zst` (5.5 GB compressed, in item `super_mario_maker_courses_202105`) | The right size for 10 million records (about 550 bytes each compressed), but its files return HTTP 401 without an archive.org login, and its schema is **not known**. The item (checked 2026-11-13) is 556 GB in 4,007 files, access-restricted, in the collections `archiveteam` and `archiveteam-fire`: 1,000 WARC chunks of about 540 MB, which probably hold the level files, plus `courses.jsonl.zst`. Step-by-step: [whole-archive.md](whole-archive.md). |
| A dump from the API's maintainer | Probably the same data the API serves; it only needs to be requested. The records seen so far carry every field the index needs. |

Builder: `smm-server index build --from <file.jsonl[.zst|.gz]> --out data/index.sqlite`. It decodes as a stream (the zstd library is already in the module graph), maps and validates the fields (it refuses files whose records lack a level id or URL) and skips levels with no attempts, since they have no win rate to sort by. It works in two passes: stream into a temporary table, then `INSERT ... SELECT ... ORDER BY bucket, id` with `row_number()` to assign the dense `seq`, then build the three indexes. The synthetic load took 3 s per 2 million rows, so the cost is JSON parsing and zstd, which is minutes for 10 million.

Distribution: the index is built once and copied (4-4.5 GB, or about 1 GB in the signature-less variant). Users do not rebuild it.

## 8. Configuration and compatibility

- `SMM_INDEX=data/index.sqlite` (by default the index is used if the file exists). With an index, the background pool and its growth are off, and the catalog becomes the cache of levels actually used.
- The index is attached under its own schema name, as the catalog is (`ATTACH ... AS archive`), read-only.
- Without an index nothing changes: pool mode stays the default, so a fresh install works with no dump.
- The `level_sources.fetched` column distinguishes stubs from complete rows.

## 9. Unverified or risky

1. **Dump access** (section 7). Without it this stays a design.
2. **Game behaviour with stubs.** It is unknown whether listing a level with `size` 0 is harmless, and whether the game requests metadata for a whole list at once. That would turn into a burst of synchronous fetches, which the prefetch queue is there to absorb. It needs a real session.
3. **Difficulty semantics.** Which ranking ID and scale the game really uses (section 5 assumes clear-rate percent).
4. **Capture availability.** In 420 pool draws no level lacked a usable capture, but a full dump will list some. Dead-marking covers it.
5. **Load on the volunteer services.** Prefetch makes about one request per second while a list is being worked through, more steadily than the pool does. Many users sharing one fetch path would multiply it, so cache sizing and pacing matter, and a shared level cache would be the polite fix.
6. **Wrong clear rate in the dump.** Buckets come from `clears / attempts`; records whose stored rate disagrees should be recomputed.

## 10. Build order and status

Steps 2-4 are done. A live run (`tools/nex-smoke/run-seeds.mjs`: a real server process, two players over the real NEX protocol, real Wayback downloads) passed 14 checks:

- the same seed gives the same levels to both players, and to the same player again;
- the spelling of the seed is ignored, and a different seed differs;
- a per-player seed wins, and clearing returns to the shared one;
- unseeded lists are random;
- a listed stub downloads in about 2 s with a `meta_binary` that matches the file.

Seeded draws are pinned by a known-answer test (`index/seed_vector_test.go`), cross-checked with a second implementation.

1. **Get the data and read its schema** (ask the maintainer, or find an authorised way to the Archive Team file). Nothing below is worth much without it.
2. **Index builder and sampler as a Go package**, with tests against a synthetic index and a small real sample: the benchmark above, as tests.
3. **Stubs and the lazy fetch hooks**, tested with the fake upstream like the pool.
4. **Prefetch worker, dead marking, size limit.**
5. **Validate in Cemu:** a list with stubs, burst behaviour, difficulty ranges.

Steps 2-4 can be built and tested without the real dump and without Cemu; step 5 decides whether the stub approach works.
