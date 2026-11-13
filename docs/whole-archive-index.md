# Whole-archive index mode

Status: **built and tested against a fake upstream and a 404-level real index; not tested with the full archive (no dump yet) and not in the game.**
Built: index builder and sampler (`server/index`), stubs and lazy completion (`levelimport`, `pool/lazy.go`), prefetch queue, dead marking,
list selection (`server/selection`), seeded draws and the seed page (`server/challenge`), and the wiring (`selection_local.go`).
Not built: cache eviction (`SMM_CACHE_MAX_MB`, section 6). The size and speed numbers below were measured on a synthetic index (2 million
rows of realistic size); everything about the full dump and about the game is marked as unverified.

## 1. Goal

Today the server can only pick random levels from a small local *pool* (about 100 levels on a new install, 404 in the live archive, at most
about 1,000). The real archive holds roughly 10 million levels. Goal: lists such as the 100 Mario Challenge and "random level of this
difficulty" should be drawn from **all** of them, while the server keeps downloading only the few levels a player actually opens.

Non-goals: storing all level files (about 470 GB), bulk-scraping the volunteer services, supporting real consoles.

## 2. Today, and why it cannot just be "bigger"

| | Pool mode (built) | Index mode (this design) |
|---|---|---|
| Levels a random list can contain | those in the local catalog (about 100-1,000) | all ~10 million |
| Filling it | the API returns 1 level per call, about half empty; about 1.5 s per kept level | nothing to fill: the index is loaded once |
| Extra disk | about 2 KB per level (catalog) | about 4-4.5 GB once, then about 2 KB per level actually used |
| Needs | nothing | a metadata dump (see section 7) |

## 3. Core idea: two stores

* **Index** (`data/index.sqlite`, read-only, lean): one small row per level in the whole archive. It is the *universe*. It holds only what is
  needed to list a level and to fetch it later.
* **Catalog** (`data/catalog.sqlite`, exists today): the levels the server has actually shown or served. Rows here are complete (size,
  `meta_binary`, ratings, rankings) and the file is on disk. Everything the game queries today keeps working unchanged.

A level moves through four states:

```
indexed ──list picks it──▶ stub ──file fetched──▶ complete ──cache full──▶ stub (file evicted)
(index only)               (catalog row, no file)  (row + file)
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

The capture URL is rebuilt from the constants (`https://d2sno3mhmk1ekx.cloudfront.net/10.WUP_AMAJ_datastore/ds/1/data/<id, 11 digits>-<rev, 5
digits>?Expires=<expires>&Signature=<sig>&Key-Pair-Id=<key pair>`), with the key pair id kept in a one-row `meta` table.

**Measured on 2,000,000 synthetic rows:** 385 bytes per level in total (337 table, 49 for the three indexes), so **3.85 GB for 10 million**.
Real names are longer than my synthetic ones (Japanese names are 3 bytes per character), so budget **4-4.5 GB**.

Variant: drop `sig` (about 256 of the 385 bytes) and look the capture URL up in the Wayback Machine's CDX search at fetch time. That makes the
index about 1-1.3 GB, at the price of one extra request per fetch to a service that timed out for me earlier. Keep it as an option, not the default.

## 5. Choosing levels ("how random is decided")

The trick is the dense `seq`: a random level in a clear-rate range is **one primary-key lookup**, not a scan.

1. **Difficulty request** (`ScoreRangeCascadedSearchObject`, extra data `[app, min, max, flag]`). Buckets `min..max` are the range (assuming the
   difficulty ranking is clear-rate percent, which is still a guess). Pick bucket *b* with probability proportional to its level count
   (counts are 101 numbers kept in memory), pick `seq` uniformly in `[0, n_b)`, read the row. Repeat until the requested number of
   distinct levels is reached. Result: **uniform over every level in the range**, so the mix follows the real archive instead of the equal-sized
   bands the pool uses.
2. **Recommended list:** the same with the range 0-100 (uniform over everything).
3. **Newest / top stars:** the two secondary indexes (0.1 ms for 100 rows).
4. **Open by ID:** `levels_id` (25 microseconds), then materialize.
5. **Dead levels:** a pick with `dead = 1` is skipped and redrawn (tombstone, so `seq` stays dense).
6. **No repeats (optional):** a small `served(pid, id)` table in the catalog, trimmed to the last few thousand ids per player, makes a repeat
   unlikely until the archive is exhausted, which at 10 million levels never happens.
7. **Empty range:** falls back to any level, as today.

Measured (2 million rows): about **31 microseconds per pick**, 3.5 ms for 100 picks, in every range tested. The obvious `ORDER BY RANDOM()
LIMIT 100` over a range took 300 ms at 2 million rows, so about 1.5 s at 10 million: not acceptable inside a game request.

## 6. From pick to playable level

**Stub.** The game needs a `DataStoreMetaInfo` for each listed level. For picked levels the server inserts a *stub* into the catalog (about 2 KB:
objects, ratings, rankings, `level_sources` with `fetched = NULL`), using the same importer code as today minus the file. Lists are then
answered by the existing functions. `size` is 0 and `meta_binary` empty until the file is fetched.

**Prefetch.** As soon as a list is returned, a background worker fetches the listed levels' files in list order, paced as today (1 Wayback
request per second), and completes each stub: writes `courses/<id>.bin`, sets `size` and `meta_binary` (both need the file), validates the four
ASH0 chunks. A 100-level list is fully fetched in about 100 s, long before a player finishes the first few levels.

**Ensure before download (the safety net).** Any request that needs the exact `size` or `meta_binary` (`GetMeta*`, `PrepareGetObject`, our
`GetObjectInfos`) first calls `ensureFetched(id)`: a single-flight, queue-jumping synchronous fetch (about 1.4 s measured in pool mode).
Implementation: wrap the function pointers assigned in `register_common_secure_server_protocols.go` and call it in
`get_object_infos.go`; list code keeps calling the plain functions, so a list never triggers a download by itself.

**Failure.** A level with no usable capture is marked `dead` in the index (it is a writable column, or a small side table if the index file
should stay read-only), is removed from the catalog, and the pick is redrawn.

**Cache.** `SMM_CACHE_MAX_MB` (default, say, 2,000 = about 40,000 levels). When it is exceeded the least recently served files are deleted and
their rows go back to stubs. Levels players uploaded to this server are never evicted or overwritten (the importer already refuses to).

## 7. Where the index comes from (the hard part)

| Source | Status |
|---|---|
| The public API | 1 level per call; about 18 million calls for everything, months at a polite rate. **Not acceptable.** |
| Archive Team `courses.jsonl.zst` (5.5 GB compressed, in item `super_mario_maker_courses_202105`) | Right size for 10 million records (about 550 bytes each compressed), but its files return HTTP 401 without an archive.org login. Its schema is **unknown to me**. |
| A dump from the API's maintainer | Probably the same data they serve; just needs asking. The records I have seen carry every field the index needs. |

Builder: `smm-server index build --from <file.jsonl[.zst|.gz]> --out data/index.sqlite`.
Streaming decode (the zstd library is already in the module graph), field mapping with validation (it must refuse files whose records lack
a level id or URL), levels with no attempts skipped (no win rate to sort by), two passes: stream into a temporary table, then
`INSERT ... SELECT ... ORDER BY bucket, id` with `row_number()` to assign the dense `seq`, then build the three indexes. The synthetic load took 3 s
per 2 million rows, so the cost is JSON parsing and zstd, minutes for 10 million.

Distribution: the index is built once and copied (4-4.5 GB, or about 1 GB in the signature-less variant), never rebuilt by friends.

## 8. Configuration and compatibility

* `SMM_INDEX=data/index.sqlite` (default: use it if the file exists). With an index the background pool and its growth are off; the catalog
  becomes the cache of levels actually used.
* The index is attached under its own schema name like the catalog is today (`ATTACH ... AS archive`), read-only.
* Without an index nothing changes: pool mode stays the default, so a fresh install still works with no dump.
* The `level_sources.fetched` column already exists and distinguishes stubs from complete rows.

## 9. Unverified or risky

1. **Dump access** (section 7). Without it this stays a design.
2. **Game behaviour with stubs.** Unknown: whether listing a level with `size` 0 is harmless; whether the game requests metadata for a whole
   list at once (which would turn into a burst of synchronous fetches, the prefetch queue is there to absorb it). Needs a real session.
3. **Difficulty semantics.** Which ranking ID and scale the game really uses (section 5 assumes clear-rate percent).
4. **Capture availability.** In 420 pool draws I saw 0 levels without a usable capture, but a full dump will list some. Dead-marking covers it.
5. **Load on the volunteer services.** Prefetch makes about 1 request per second while a list is being worked through, more steadily than the
   pool does. Many friends sharing one fetch path would multiply it; cache sizing and pacing matter, and a shared level cache is the polite fix.
6. **Wrong clear rate in the dump.** Buckets come from `clears / attempts`; records with a stored rate that disagrees should be recomputed.

## 10. Build order (status)

Steps 2-4 are done except cache eviction. A live run (`tools/nex-smoke/run-seeds.mjs`: real server process, two players over the real NEX
protocol, real Wayback downloads) passed 14 checks: same seed gives the same levels to both players, the same player again, spelling of the seed
ignored, a different seed differs, a per-player seed wins and clearing returns to the shared one, unseeded lists are random, a listed stub downloads
in about 2 s with a `meta_binary` that matches the file. Seeded draws are pinned by a known-answer test (`index/seed_vector_test.go`),
cross-checked with a second implementation.

1. **Get the data and read its schema** (ask the maintainer, or find an authorized way to the Archive Team file). Nothing below is worth much without it.
2. **Index builder + sampler as a Go package** with tests against a synthetic index and a small real sample: exactly the benchmark above, as tests.
3. **Stubs and the lazy fetch hooks**, tested with the fake upstream like the pool.
4. **Prefetch worker, dead marking, cache eviction.**
5. **Validate in Cemu** (needs the Wii U account files): list with stubs, burst behaviour, difficulty ranges.

Steps 2-4 can be built and tested without the real dump and without Cemu; step 5 decides whether the stub idea works.
