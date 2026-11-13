# Getting the whole archive

The archive holds roughly 10 million levels (about 470 GB of level files). "The whole archive" can mean three different things, and they are very different jobs. Pick the one you want.

| You want | Route | Disk | Status |
|---|---|---|---|
| Lists and challenges that can draw from **every** level, with a level downloaded only when it is played | **A. The index** | about 10 GB while building, 4-4.5 GB afterwards, plus the levels you play | The program is built and tested on a synthetic index and a 404-level real one. **Not yet run on the real dump**, and not tried in the game. |
| **Every level file** on your own disk, playable offline | **B. The Archive Team files** | 556 GB | **Not supported yet:** the files are packed in WARC archives and the program has no importer for them. |
| The same, fetched level by level from the public services | **C. `cache build` with a huge `--levels`** | 470 GB | Works, but would take about 230 days of constant requests. **Please do not.** |

Most people want **A**. If you want **B**, read the end of this page and say so: the importer is the missing piece.

## Where the data is

The Archive Team saved the game's levels in 2021. It is one item on archive.org:

- Item: `super_mario_maker_courses_202105` (<https://archive.org/details/super_mario_maker_courses_202105>), in the collections `archiveteam` and `archiveteam-fire`.
- **556 GB in 4,007 files:** 1,000 chunks (`chunk000-00000.warc.gz` to `chunk999-00000.warc.gz`, about 540 MB each, plus small `-meta` files and indexes), and one file for route A: **`courses.jsonl.zst`** (about 5.5 GB), a metadata record per level (name, creator, clears, attempts, stars, upload time, original download address).
- The item is marked **access-restricted**: its files answer HTTP 401 without an archive.org login. Whether a free account is enough is not verified. It is not a torrent either.

Everything below that involves your archive.org login is for you to do yourself. Never give the login to this program or to anyone else.

## Route A: the index

1. **Make room.** About 6 GB for the download, about 4.5 GB for the index, and the program's own data folder.
2. **Get an archive.org account** (<https://archive.org/account/signup>) and sign in on the site.
3. **Download only `courses.jsonl.zst`.** Do **not** download the whole item: it is 556 GB. With the `ia` tool (needs Python):

   ```
   pip install internetarchive
   ia configure
   ia download super_mario_maker_courses_202105 courses.jsonl.zst
   ```

   `ia configure` asks for your archive.org e-mail and password and stores them on your computer. You can also save the one file from the item's page in a browser, signed in. If both give "401", the file needs permission your account does not have; ask in the Archive Team channel on hackint IRC (`#archiveteam`) or the item's uploader.
4. **Try the file on a small piece first.** The program was written to fit the metadata API's record shape, and the schema of this file is **not verified**. This reads the first 1,000 records and writes a small index:

   ```
   smm-server index build --from super_mario_maker_courses_202105/courses.jsonl.zst --out try.sqlite --limit 1000
   ```

   If the records do not look like level records it stops and lists the field names of the first record. Send that list (or open an issue): it is a small fix in `upstream.ParseRecord`.
5. **Build the whole index** (streams the file, so memory stays modest; expect minutes to a few tens of minutes):

   ```
   smm-server index build --from super_mario_maker_courses_202105/courses.jsonl.zst --out data/index.sqlite
   ```

   It prints how many levels it indexed, how many it skipped (levels nobody ever attempted have no win rate to sort by), and the index's **fingerprint**.
6. **Restart the server.** With `data/index.sqlite` present (or `SMM_INDEX=<path>`) it draws lists from every indexed level, and the log says so. The levels a list picks are downloaded in the background and when the game opens them, one request per second, as in the Cemu guide.
7. **Challenges.** Players who want the same levels from a seed need the same index, so compare the **fingerprint**. The seed page shows the challenge code (the seed plus the fingerprint). One person can build the index and share `index.sqlite` (4-4.5 GB); nobody else has to download the 5.5 GB file.

`SMM_CACHE_MAX_MB` is useful here: the levels you play pile up, and the limit removes the oldest.

An index and a [cache](cache.md) do not combine: while a data folder's cache is locked the server fetches nothing and ignores the index. Use the index in a data folder that is not a locked cache.

## Route B: all level files on disk

The 1,000 `chunk*.warc.gz` files (about 556 GB) are what the Archive Team crawled. Judging by their size they hold the level files, but **that is not verified**: the item has no description, and the program cannot open them. What route B needs is a tool that reads the WARC files, pulls out each level file and puts it where the program expects it (`courses/<id>.bin` with a catalog row). That does not exist yet. With it the whole archive could be played offline with no requests to the public services at all, which is kinder to them than any other route.

It would also need the 556 GB downloaded, which is the larger of the two practical problems: archive.org throttles large downloads and the item is restricted. If you have the item and want this, tell the maintainer; the first step is to look at one chunk (`ia download super_mario_maker_courses_202105 chunk000-00000.warc.gz`, 540 MB) and see what is in it.

## Route C: why not simply `--levels 10000000`

`cache build` is paced at the rate the two volunteer services can bear: one metadata request every 0.4 s and one download every second. Ten million levels at about two seconds each is about 230 days, and the services would see that as a constant stream from one address for most of a year. The pace is fixed in the program on purpose. For anything beyond a few tens of thousands of levels, use route A or B, or build a cache and share it as a zip ([cache.md](cache.md)).

## Summary of what is and is not verified

- Verified: the index builder and the list picking on synthetic data and on 404 real records; seeds across two simulated players; the item's contents as listed above (checked on 2026-11-13).
- Not verified: that `courses.jsonl.zst` matches the builder's record shape, that an ordinary archive.org account can download it, how the game reacts to lists made of not-yet-downloaded levels, and what the WARC chunks contain.
- [whole-archive-index.md](whole-archive-index.md) is the design note behind route A.
