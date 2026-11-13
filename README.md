# smmserver: a home Super Mario Maker server with an archive of old levels

```
server/   the SMM server (fork of Protarium-Network/super-mario-maker), ported to SQLite with a built-in S3,
          an account service for Cemu, and on-demand fetching of archived levels (upstream/, levelimport/, pool/, ash0/)
tools/    ASH0 codec, .bin + meta_binary builder, archive importer
fake-s3/  the earlier standalone Node S3 stand-in (superseded by server/s3local; kept for reference)
repos/    upstream clones used for reference (smm-server, SMMDB)   samples/  one downloaded level + test output
archive/  a one-level archive (the SMMDB sample)      archive-live/  404 levels fetched by the server (101 per clear-rate band, 19.5 MB; ready to run)
```

## Where the code comes from, and updating it

`server/` is a fork of [Protarium-Network/super-mario-maker](https://github.com/Protarium-Network/super-mario-maker), which comes
from [PretendoNetwork/super-mario-maker](https://github.com/PretendoNetwork/super-mario-maker) (AGPL-3.0). This repository keeps that
history: the upstream commit `6d49108` was imported into `server/` with `git subtree`, and the home-server changes sit in a commit on
top, so `git log server/` and `git blame` show which lines are ours and which are theirs.

Remotes: `upstream` (Protarium, the one `server/` follows) and `pretendo` (the original). Pushing to both is disabled.

To bring in upstream changes:

```
git fetch upstream pretendo
git log --oneline HEAD..upstream/main       # what is new
git subtree pull --prefix=server upstream main -m "Merge upstream"
```

Conflicts can only be in files we changed (mostly `server/database/`, `server/nex/register_common_secure_server_protocols.go`
and `server/init.go`); fix them as in any merge. `tools/`, `docs/` and `fake-s3/` are ours alone.

Not in the repository (see `.gitignore`): server data (`archive-live/`, `data/`, `secrets.json`), built programs and zips
(`dist/`), logs, and downloaded reference copies. No level files are committed: the few tests that need a real course file
are skipped unless you add one (see `server/levelimport/testdata/README.md`).

## Why it looks like this

Goal: run old levels at home from a portable archive, without Postgres or an S3 service.

* **No Postgres.** The catalog is one SQLite file. The original Postgres schema is emulated by attaching that
  file as schema `datastore`, so almost all upstream SQL is unchanged (see `server/database/sqlite.go`,
  `schema.sql`). Arrays are JSON text (`database.Array` replaces `pq.Array`).
* **No S3 service.** The server embeds a tiny S3 (`server/s3local`). Consoles get presigned URLs pointing at it;
  signatures are not checked. Only the last path segment of a key matters, which also hides an upstream
  key-prefix inconsistency.
* **An archive is a folder:** `catalog.sqlite` + `courses/<id>.bin` (+ optional `.jpg`), plus `uploads/` for new levels.
  The catalog runs in SQLite WAL mode: while the server runs, recent changes live in `catalog.sqlite-wal`. **Copy an archive only
  while the server is stopped, and never delete the `-wal` / `-shm` files by hand** (that silently drops the newest changes).

## Run it (what a friend does)

The server is **one self-contained program**: no Go, Python, Node, database or S3 service is needed to run it.

1. Unzip `dist/smm-server-<your platform>.zip` anywhere and start `smm-server` (double-click on Windows; `chmod +x` first on
   Linux/macOS). Windows will show a SmartScreen warning (the program is not code-signed: "More info" > "Run anyway") and a firewall
   prompt (allow **Private networks**, or consoles cannot connect).
2. Leave the window open. It prints a box with this computer's address and the Cemu steps. The first start downloads about 100 levels
   in the background (roughly 5 minutes); more arrive slowly while it runs. Nothing else is configured: it creates a `data` folder
   next to the program with its own keys, a commented `settings.env`, and the level archive.
3. In Cemu: copy `data/network_services.xml` next to Cemu's `settings.xml` and choose Network Service: **Custom** (details below,
   including the Wii U account files Cemu insists on).
4. Stop with Ctrl+C (it saves the catalog). Copy the `data` folder only while the server is stopped.

To skip the first download, put a ready-made `data` folder next to the program: `dist/smm-starter-levels.zip` holds 404 levels
(101 per clear-rate band, 19.5 MB zipped). Unzip it next to `smm-server` so you get `data/catalog.sqlite` and `data/courses/`.
The server writes its own keys and Cemu config on start, so those files are not in the starter pack.

`smm-server --help` explains the same things. Everything that can be changed is in `data/settings.env` (address, ports, offline mode,
pool size); environment variables override it.

### What needs what

| Thing | Needs |
|---|---|
| **Running the server** | the one executable. Nothing else. |
| Building the executable | Go 1.26+ (`node tools/build-release.mjs` builds every platform; the builds are static, `CGO_ENABLED=0`) |
| Importing SMMDB zips (`tools/import-archive.mjs`) | Node 22+; optional, only for offline archives |
| Running the tests | Go; Node for `tools/`; Python 3.12 + `nintendoclients` for `tools/nex-smoke` |

### Portability

`node tools/build-release.mjs` produced all of these (about 22-24 MB each, 8-9 MB zipped). **Only Windows was actually run**; the others
compiled cleanly and use only portable code (pure-Go SQLite, no C), but I could not run them here.

| Platform | Built | Run here |
|---|---|---|
| Windows x64 | yes | **yes**: first start from an empty folder with only Windows on `PATH`, banner, clean shutdown, NEX smoke test |
| Linux x64 | yes | no |
| Linux arm64 (Raspberry Pi 4/5, many NAS boxes) | yes | no |
| macOS Apple silicon / Intel | yes | no |

A small always-on box is a good home for it: the server only needs to be on the same network as the console or Cemu.

### Build it yourself

```
node tools/build-release.mjs                       # all platforms into dist/
node tools/build-release.mjs windows-amd64         # or just one
cd server && go build -o ../smm-server.exe .       # plain developer build
```

An archive built from SMMDB downloads instead of fetched levels (optional, needs Node):

```
node tools/import-archive.mjs --archive my-archive --meta levels.json "[942697] Some Level.zip" ...
SMM_DATA_DIR=my-archive ./smm-server.exe
```

## Logging in from Cemu

The server includes a small account service (`server/homeaccount`) that stands in for Nintendo's account server.
On first start it writes `data/secrets.json` (keep it with the archive) and `data/network_services.xml`.

The full walk-through is in **[docs/setup-cemu.md](docs/setup-cemu.md)** (a modded Wii U: [docs/setup-wiiu.md](docs/setup-wiiu.md), untested). In short:

1. Copy `network_services.xml` next to Cemu's `settings.xml`.
2. Cemu: Options > General settings > Account > Network Service: **Custom**.
3. Use an account whose `PrincipalId` equals the player ID this server derives from its name. `smm-server account <name | account.dat>`
   prints or patches it, and `smm-server account new <name> <mlc01>` creates a Cemu account that matches.
4. Install the Mii face fix graphic pack from `docs/cemu/SMM_MiiFaceFix` (Cemu 2.6 draws Miis without faces otherwise).

Cemu only attempts online login if it has `otp.bin` and `seeprom.bin` (it refuses otherwise, see its `napi_act.cpp`), and it needs
the console's certificates and Mii resources too; all of that comes from your own Wii U (Dumpling). This service never checks
the device certificate Cemu sends.

**There are no passwords: anyone who can reach port 8080 can log in as anyone. LAN only, never expose it.**
To own an imported creator's levels, point your NNID at their PID: `UPDATE accounts SET pid = <creator pid> WHERE nnid = '<you>'`
in `catalog.sqlite` (after the first login created the row).

Ports: account 8080 (`SMM_ACCOUNT_PORT`, 0 = off), Wii U proxy 8888 (`SMM_PROXY_PORT`, 0 = off), S3 9000, NEX authentication 60000, secure 60001 (UDP).
Cemu limits the NEX host to 15 characters, so use an IPv4 address for `SMM_PUBLIC_HOST`.

`SMM_PUBLIC_HOST` is the address the **console** uses to download courses. Optional: `SMM_S3_PORT` (default 9000).
`SMM_TRACE=1` logs every request the game makes, which is only useful when something misbehaves.
Setting `PN_SMM_CONFIG_S3_ENDPOINT` switches back to an external S3. Any `PN_*` variable you set yourself wins over the generated defaults.

## Archived levels on demand

The old servers are gone, but volunteers saved the levels. The server uses two public services, at a low request rate:

* the metadata API behind [HerobrineTV's SMM1 Level Downloader](https://github.com/HerobrineTV/SMM1-Level-Downloader)
  (`api.bobac-analytics.com`): per level the name, creator PID, clears / failures / attempts, stars, upload time and the
  original download URL. **Clear rate = clears / attempts**, the win rate;
* the Internet Archive's Wayback Machine (`web.archive.org`), which captured those download URLs in 2021: the original course
  files, ~12-100 KB each.

Two behaviours, both on by default (`SMM_FETCH=0` makes the server fully offline):

* **A level the game asks for by ID** that the catalog lacks is looked up, downloaded, validated and imported before
  the answer is sent: about 1.4 s in the live test. An ID the archive does not have answers NotFound in ~0.3 s and is
  remembered for 30 minutes. Levels that players uploaded to this server are never overwritten by archive levels.
* **A pool for lists and difficulty.** The game needs candidates before it can list anything, and the API only returns one
  random level per call, so a background task samples levels and keeps **`SMM_POOL_TARGET`** (default 25) levels in each of four
  clear-rate bands (0-5 %, 5-20 %, 20-50 %, 50-100 %). It downloads only what an unfilled band needs, resumes after a restart,
  and a full pool costs nothing. Live test: 200 levels (50 per band) in 6 min 15 s, 419 draws, no invalid files or errors.

Difficulty: each level gets a ranking value under application ID `300000000` = its clear rate in percent. The Mario Challenge
request (ScoreRangeCascadedSearchObject) picks random levels whose value lies in the requested range; with the live pool,
ranges 1-49, 0-5, 20-50 and 50-100 returned only levels inside the range and a different selection each time.
**Whether the game really uses that application ID and a percent scale is a guess** (see below).

**Growth.** After the first fill the pool keeps growing slowly while the server runs: every `SMM_POOL_GROW_EVERY` (default `10m`,
`0` stops growth) each band gets `SMM_POOL_GROW_BY` (default 1) more levels, until every band holds `SMM_POOL_MAX` (default 250,
about 1,000 levels / 48 MB). A growth step has a small draw budget, and the target follows the emptiest band, so a band the archive
cannot fill right now never causes a burst of requests or lets the other bands balloon. For a one-time bigger pool just start
once with a larger `SMM_POOL_TARGET`; it is the same code path, at the same request rate.

**Repeats.** Without the whole-archive index a round of random levels can only come from the pool, so it would repeat quickly
(a 50-level list out of ~120 levels). Levels you just opened (for 12 hours) go to the back of the random lists, and when the unplayed
levels of a difficulty run low the pool fetches 12 more for the bands of that difficulty in the background. A seeded challenge list
is not affected. The "played" memory is in RAM (a restart forgets it) and shared by everyone on the server.

**Size limit (optional, off by default).** The timer growth stops at `SMM_POOL_MAX`, but the extra levels fetched while you play have no
cap, so a heavy player's data folder keeps growing. Set `SMM_CACHE_MAX_MB` (for example `500`) and every five minutes the server
checks how much the downloaded level files take; over the limit it removes the oldest levels (by when they were fetched) down to
90% of it, with their ratings, rankings and records. Levels a local account starred or made are never removed, and the game
can fetch a removed level again if it asks for it. Values from 1 to 19 are raised to 20 MB; `0` (the default) means no limit. It
works while fetching is on. New levels keep arriving, so the pool becomes a rolling window of the most recently fetched levels.

Politeness: requests are paced (API 400 ms, Wayback 1 s apart, shared by all callers), `Retry-After` is honored, and the metadata
API's analytics endpoints are never called. Anyone who runs this server contacts those two services from their IP address.
For a bigger pool prefer asking the maintainers for a dump over raising `SMM_POOL_TARGET` a lot.

## The whole archive, and challenge seeds

Without an index the server can only offer the few hundred levels it has downloaded. With an **index** (one small read-only file,
about 385 bytes per level, 3.85 GB for 10 million) every archived level can come up in a list, and only the levels that are actually
used are downloaded:

```
smm-server index build --from levels.jsonl.zst --out data/index.sqlite   # also reads .jsonl and .jsonl.gz
smm-server index info data/index.sqlite                                  # count, clear-rate histogram, fingerprint
```

Put the file at `data/index.sqlite` (or set `SMM_INDEX`) and restart. Lists then draw from all indexed levels, filtered by clear rate; the
chosen levels are listed straight away as "stubs" and downloaded in the background, or at the latest when the game opens one. A level with no
usable capture is marked dead and never offered again. `SMM_LIST_WAIT=<seconds>` makes a list wait that long for its levels to finish
downloading. **What the index is built from is still open**: the Archive Team dump needs an archive.org login and the metadata API gives one
level per call, so a dump has to come from the API maintainer. `docs/whole-archive-index.md` has the design, sizes and risks.

**Challenge seeds.** With a seed set, the "random" levels of a list are a fixed function of the seed and the list that was asked for
(difficulty range), so friends who use the same seed play the same levels, in the same order. Open `http://<this PC>:8080/seed` (the account
port), type a seed or let it pick one, for everyone or for one player. A player's own seed wins over the shared one; clearing the seeds makes
levels random again. Spelling, case and extra spaces do not matter. The game has no seed field, so seeds are set on the server, and the page has
no password (LAN only). Players on different servers get the same levels only if their **challenge codes** match (seed plus index fingerprint,
shown on the page). Without an index, seeds still work but pick from whatever levels each server has downloaded, so they match only on servers
with identical level sets. Seeded draws are pinned by a known-answer test, so a seed keeps its meaning across releases (a changed algorithm has to
bump the version in the code, which changes every code).

Unverified: whether the game accepts a listed level whose size and `meta_binary` are still empty (set `SMM_LIST_WAIT` if it does not), whether the
game reshuffles a list on the console, and which ranking and scale the game's difficulty filters use (the Course World filter is read as a
*failure* rate: Easy 0-34 becomes clear rate 66-100).

## Tests

```
cd server && go test ./...                       # SQL against SQLite; built-in S3 against the real minio client
cd tools  && node --test                         # ASH0 codec vs a port of the reference decoder, meta_binary
SMM_IMPORT_ARCHIVE=<archive> go test ./database/... -run ImportedArchive   # Node-written catalog read by Go
node tools/nex-smoke/run-smoke.mjs --archive archive --level 942697 --name "<level name>"   # real NEX client end to end, offline (see tools/README.md)
node tools/nex-smoke/run-seeds.mjs --index samples/live-index.sqlite   # index + seeds, two players, real downloads
# against a running server with fetching on (talks to the public services):
python tools/nex-smoke/nex_live.py --account http://127.0.0.1:8080 --ondemand 1829603 --difficulty 300000000:1:49
```

## Not verified / open (read this)

* **It has run in Cemu 2.6, not on a console.** In Cemu (USA, v240) the game logs in, lists levels from Course World and
  100 Mario Challenge, downloads and plays them, and shows creator Miis and names on lists and clear screens. Untested: other
  regions, a real Wii U or 3DS. A NEX client test (`tools/nex-smoke`) still covers login and downloads without the game.
* **Flags are a guess.** The game shows a maker's country from the first tag of their profile. The archive has no country; the
  server derives one from the region/language code stored with each Mii (0 Japan, 1 USA, 2 Canada, 3 Mexico, 4 UK, 5 France,
  6 Spain, 7 Germany, 8 Italy, 9 Netherlands, 10 Russia, 11 Portugal). The country numbers are from the console's country list
  as I remember it and were checked for Japan only. The 62 oldest profiles have no region code and keep Japan.
* **What the game needs from the data (found by trial in Cemu):** a maker is only shown if their profile object is in the
  custom rankings 300000000 and 300001200 (starring a level adds it; the server now adds every profile). The server also gives
  every level rating slot 5, which starring creates; that alone did not change what is shown, but it matches what the game writes.
* **Guessed values:** the three unknown `meta_binary` fields, courses' `data_type` (3), which custom-ranking
  application IDs mean "stars" (the importer uses 0) and "difficulty" (300000000, in percent; the only sample seen is
  `300000000 1 49 0`, and the bands the game uses for Easy / Normal / Expert / Super Expert are unknown). The server logs `GetCustomRanking app=<id>`: use
  Cemu to learn the real IDs, then re-import with `--ranking-apps`.
* **`900000.bin`** (event-course metadata, needed for Course World) is not available; the server creates an
  empty placeholder and warns.
* **Login works in Cemu** with an account from a Wii U dump or a hand-made one whose `PrincipalId` matches. `smm-server account new`
  writes the same fields as the hand-made account that worked, but that command has not been run against Cemu yet. Not covered: Cemu's
  other services (shop, Miiverse, BOSS), which stay on Nintendo's dead URLs. The PRUDP + Kerberos login also runs against the
  test client in `tools/nex-smoke` (PRUDP minor version 0 only, see tools/README.md).
* **Real Wii U hardware has not been tried.** [docs/setup-wiiu.md](docs/setup-wiiu.md) describes the route (Inkay, plus the
  built-in proxy on port 8888 that stands in for `account.pretendo.cc`) and what to report if it fails. It needs an account that
  already exists on the console (`smm-server account adopt`); creating or linking accounts, SSSL, shop, Miiverse and friends
  are not built. The 3DS is not covered.
* **Lost in the archive:** who gave each star (placeholder entries, capped at 500 per level; the true count is kept in the
  star ranking) and the number of players (the attempt count is used instead). The world record and the first clear are NOT
  lost: the per-level API answer has both with real PIDs, and the server fetches them the first time the game asks for a
  level's course record (one small API call, remembered), so a level you clear is no longer a "first clear" and a "new
  record" every time. A record you set yourself stays if it is better than the archived one.
  **Names and Miis:** the same answer has the NNID and Mii of the creator, the record holder and the first clearer. The game
  shows a maker through their *profile object* (a DataStore object: owner = the maker's PID, persistence slot 0, type 1, named
  after the NNID, the Mii as its meta binary), so the server makes that object on the spot when the game asks for a maker the
  archive knows. **Not available: country** (the API has none; profiles get the same tag the game writes for its own player)
  and a maker's profile statistics. Archived levels used to sit in persistence slot 0 too, which made the game read a
  level where it wanted a Mii; they are in "no slot" (65535) now, and an existing catalog is fixed on start.
* **Fetched levels are the original files, byte for byte** (no re-compression), unlike levels rebuilt from SMMDB zips.
  All 200 live-test files passed validation (four ASH0 chunks, decoded sizes, JPEG thumbnails), so the `meta_binary` chunk
  split holds on real data; what is unverified is only the three unknown fields and the CRC rule.
* Upstream quirk left as is: `InitializeObjectByPreparePostParam` builds `extraData` by iterating `Tags`.

## Licence

AGPL-3.0, like the project it is forked from (see [LICENSE](LICENSE)). If you run a changed version for other people, you must offer them its source code.
