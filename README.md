# smmserver

A home server for **Super Mario Maker (Wii U)** with an archive of old levels. It stands in for the game's online service on your own network, so Course World and the 100 Mario Challenge work again, using levels that volunteers saved before the official servers shut down.

It runs as one self-contained program: no database, object store or other service to install. The game connects through [Cemu](https://cemu.info) today; a modded Wii U is described but has not been tried yet.

- Archived levels are fetched from public services while you play, so the server starts small and grows.
- Creator names, Miis and clear screens work.
- It is for a home network. There are no passwords (see [Security](#security)).

Guides:

- **[Playing on Cemu](docs/setup-cemu.md)**
- **[Playing on a modded Wii U](docs/setup-wiiu.md)** (not tested on a console) and [its tester checklist](docs/wiiu-tester-checklist.md)

## Contents

- [Quick start](#quick-start)
- [Configuration](#configuration)
- [How archived levels work](#how-archived-levels-work)
- [Caches: the same levels on every computer](#caches-the-same-levels-on-every-computer)
- [The whole archive and challenge seeds](#the-whole-archive-and-challenge-seeds)
- [Building and tests](#building-and-tests)
- [Status and known limits](#status-and-known-limits)
- [Security](#security)
- [Repository layout and upstream](#repository-layout-and-upstream)
- [Licence](#licence)

## Quick start

1. Get the program: build it (see [Building and tests](#building-and-tests)) or use a release zip for your platform if one is published. Unzip it anywhere and start `smm-server` (double-click on Windows; `chmod +x` first on Linux and macOS).
   - Windows shows a SmartScreen warning because the program is not code-signed: choose "More info", then "Run anyway".
   - Windows Firewall asks for permission: allow **Private networks**, or the console cannot connect.
2. Leave the window open. It prints a box with this computer's address. The first start downloads about 100 levels in the background (roughly five minutes) and more arrive slowly while it runs. It creates a `data` folder next to the program with its own keys, a commented `settings.env` and the level archive.
3. Connect Cemu: follow [docs/setup-cemu.md](docs/setup-cemu.md). In short, copy `data/network_services.xml` next to Cemu's `settings.xml` and set the account's Network Service to **Custom**.
4. Stop with Ctrl+C, which saves the catalog. Copy the `data` folder only while the server is stopped.

`smm-server --help` summarises this. Everything that can be changed is in `data/settings.env`; environment variables override it.

An archive is a folder: `catalog.sqlite`, `courses/<id>.bin` (and optional `.jpg`), and `uploads/` for levels uploaded to this server. The catalog uses SQLite's WAL mode, so while the server runs recent changes live in `catalog.sqlite-wal`. **Never delete the `-wal` or `-shm` files by hand**; that silently drops the newest changes.

### What needs what

| Task | Needs |
|---|---|
| Running the server | The one executable. Nothing else. |
| Building the executable | Go 1.26+ (`node tools/build-release.mjs` builds every platform; the builds are static, `CGO_ENABLED=0`) |
| Importing SMMDB zips (`tools/import-archive.mjs`) | Node 22+; optional, only for offline archives |
| Running the tests | Go; Node for `tools/`; Python 3.12 and `nintendoclients` for `tools/nex-smoke` |

### Platforms

Release builds exist for Windows x64, Linux x64 and arm64 (Raspberry Pi 4/5 and many NAS boxes), and macOS on Apple silicon and Intel. Each is about 22-24 MB (8-9 MB zipped) and uses only portable code (pure-Go SQLite, no C).

| Platform | Built | Run and tested |
|---|---|---|
| Windows x64 | yes | **yes**: first start from an empty folder, banner, clean shutdown, NEX smoke test |
| Linux x64 and arm64 | yes | no |
| macOS Apple silicon and Intel | yes | no |

A small always-on machine is a good home for the server: it only has to be on the same network as the console or Cemu.

## Configuration

`data/settings.env` documents every setting. The ones most people need:

| Setting | Meaning | Default |
|---|---|---|
| `SMM_PUBLIC_HOST` | Address the console or Cemu uses to reach the server (an IPv4 address; Cemu allows at most 15 characters) | detected |
| `SMM_FETCH` | `0` runs the server fully offline, with only the levels already downloaded | on |
| `SMM_POOL_TARGET` | Levels kept ready in each of four clear-rate bands | 25 |
| `SMM_POOL_GROW_EVERY`, `SMM_POOL_GROW_BY`, `SMM_POOL_MAX` | Slow growth of the pool (see below) | 10m, 1, 250 |
| `SMM_CACHE_MAX_MB` | Size limit for downloaded levels; the oldest are removed beyond it | 0 (off) |
| `SMM_TRACE` | `1` logs every request the game makes; only for tracking down problems | off |
| `SMM_DATA_DIR` | A different data folder | `./data` |

Ports: account service 8080 (`SMM_ACCOUNT_PORT`, 0 = off), Wii U proxy 8888 (`SMM_PROXY_PORT`, 0 = off), course downloads 9000 (`SMM_S3_PORT`), NEX authentication 60000 and secure 60001 (UDP). Allow them in the firewall if the console or Cemu cannot connect. `SMM_PUBLIC_HOST` is also the address the console uses to download courses. Setting `PN_SMM_CONFIG_S3_ENDPOINT` switches back to an external S3, and any `PN_*` variable you set yourself wins over the generated defaults.

### Accounts

The server includes a small account service (`server/homeaccount`) that stands in for Nintendo's account server. On first start it writes `data/secrets.json`, which holds the server's keys (keep it with the archive; without it everyone has to log in again), and `data/network_services.xml` for Cemu.

The game logs in with the player ID stored in its account file, and the server decides which ID belongs to which account name, so the two must match. `smm-server account` handles it:

```
smm-server account <name>                  # print the player ID for a name
smm-server account <path/account.dat>      # set PrincipalId in an account file to that ID
smm-server account new <name> <mlc01>      # create a matching Cemu account
smm-server account adopt <account.dat>     # use the ID already in an account file (for a Wii U)
```

To play as the creator of an imported level, point your name at their ID: `UPDATE accounts SET pid = <creator pid> WHERE nnid = '<you>'` in `catalog.sqlite`, after the first login created the row.

## How archived levels work

The official servers are gone, but volunteers saved the levels. The server uses two public services, at a low request rate:

- the metadata API behind [HerobrineTV's SMM1 Level Downloader](https://github.com/HerobrineTV/SMM1-Level-Downloader) (`api.bobac-analytics.com`): per level the name, creator PID, clears, failures and attempts, stars, upload time and the original download URL. **Clear rate = clears / attempts**, the win rate;
- the Internet Archive's Wayback Machine (`web.archive.org`), which captured those download URLs in 2021: the original course files, roughly 12-100 KB each.

Two behaviours, both on by default:

- **A level the game asks for by ID** that the catalog lacks is looked up, downloaded, validated and imported before the answer is sent (about 1.4 s in a live test). An ID the archive does not have answers NotFound in about 0.3 s and is remembered for 30 minutes. Levels that players uploaded to this server are never overwritten by archive levels.
- **A pool for lists and difficulty.** The game needs candidates before it can list anything, and the API returns only one random level per call. A background task therefore samples levels and keeps `SMM_POOL_TARGET` levels in each of four clear-rate bands (0-5 %, 5-20 %, 20-50 %, 50-100 %). It downloads only what an unfilled band needs, resumes after a restart, and a full pool costs nothing. In a live test it built 200 levels (50 per band) in 6 min 15 s, with 419 draws and no invalid files or errors.

**Difficulty.** Each level gets a ranking value under application ID `300000000`: its clear rate in percent. The Mario Challenge request (`ScoreRangeCascadedSearchObject`) picks random levels whose value lies in the requested range. With a live pool, the ranges 1-49, 0-5, 20-50 and 50-100 returned only levels inside the range, and a different selection each time.

**Growth.** After the first fill the pool grows slowly while the server runs: every `SMM_POOL_GROW_EVERY` (`0` stops growth) each band gets `SMM_POOL_GROW_BY` more levels, until every band holds `SMM_POOL_MAX` (about 1,000 levels, 48 MB). A growth step has a small draw budget and the target follows the emptiest band, so a band the archive cannot fill right now never causes a burst of requests or lets the other bands balloon. For a one-time bigger pool, start once with a larger `SMM_POOL_TARGET`; it uses the same code path at the same request rate.

**Repeats.** Without the whole-archive index a round of random levels can only come from the pool, so it would repeat quickly (a 50-level list out of about 120 levels). Levels that were just opened (for 12 hours) go to the back of the random lists, and when the unplayed levels of a difficulty run low the pool fetches 12 more for the bands of that difficulty in the background. A seeded challenge list is not affected. The "played" memory is in RAM, so a restart forgets it, and it is shared by everyone on the server.

**Size limit (optional, off by default).** Growth by timer stops at `SMM_POOL_MAX`, but the levels fetched while playing have no cap, so a heavy player's data folder keeps growing. With `SMM_CACHE_MAX_MB` set (for example `500`), the server checks every five minutes how much the downloaded level files take. Over the limit it removes the oldest levels, by when they were fetched, down to 90% of the limit, together with their ratings, rankings and records. Levels a local account starred or made are never removed, and the game can fetch a removed level again if it asks for it. Values from 1 to 19 are raised to 20 MB, `0` means no limit, and the limit applies while fetching is on. New levels keep arriving, so the pool becomes a rolling window of the most recently fetched levels.

**Politeness.** Requests are paced (API 400 ms apart, Wayback 1 s apart, shared by all callers), `Retry-After` is honoured, and the API's analytics endpoints are never called. Anyone who runs the server contacts those two services from their own IP address. For a bigger pool, prefer asking the API's maintainers for a dump over raising `SMM_POOL_TARGET` a lot.

## Caches: the same levels on every computer

A **cache** is a data folder filled on purpose. `smm-server cache build --seed "spring contest" --levels 400` downloads 400 levels (rounded up to a multiple of 4, filled evenly across the four difficulty groups). With `--seed`, the seed alone decides which levels they are, so everyone who builds the same seed to the same size gets the same levels, and the server then gives all players the same lists. Without a seed the levels are random. A build can be stopped and carried on, and a larger `--levels` with the same seed only adds levels.

```
smm-server cache build --levels N [--seed WORDS]   # download into the data folder (about 50 KB and 2 s per level)
smm-server cache info                              # level count, size, fingerprint, seed
smm-server cache export my-cache.zip               # pack it (without any player's data)
smm-server cache import my-cache.zip               # unpack it into an empty data folder
smm-server cache lock | unlock                     # keep the set of levels fixed, or let the server fetch again
```

The **fingerprint** (eight characters) identifies the exact set of levels; the same fingerprint means the same levels. A seeded cache is locked, so the server fetches nothing and the set stays as built. Anyone with enough disk space can build a large cache (4,000 levels is about 200 MB and 2.5 hours), and one person can build it and share the zip. The literal whole archive (about 10 million levels, around 470 GB) cannot be built this way; see [docs/cache.md](docs/cache.md) for the sizes, sharing, and what can still make two copies differ.

## The whole archive and challenge seeds

Without an index the server can only offer the few hundred levels it has downloaded. With an **index** (one small read-only file, about 385 bytes per level, 3.85 GB for 10 million levels) every archived level can come up in a list, and only the levels that are used are downloaded:

```
smm-server index build --from levels.jsonl.zst --out data/index.sqlite   # also reads .jsonl and .jsonl.gz
smm-server index info data/index.sqlite                                  # count, clear-rate histogram, fingerprint
```

Put the file at `data/index.sqlite` (or set `SMM_INDEX`) and restart. Lists then draw from all indexed levels, filtered by clear rate. The chosen levels are listed straight away as "stubs" and downloaded in the background, or at the latest when the game opens one. A level with no usable capture is marked dead and never offered again. `SMM_LIST_WAIT=<seconds>` makes a list wait that long for its levels to finish downloading.

What the index is built from is still open: the Archive Team dump needs an archive.org login and the metadata API gives one level per call, so a dump has to come from the API's maintainer. [docs/whole-archive-index.md](docs/whole-archive-index.md) has the design, sizes and risks.

**Challenge seeds.** With a seed set, the "random" levels of a list are a fixed function of the seed and of the list that was asked for (the difficulty range), so players who use the same seed get the same levels in the same order. Open `http://<server address>:8080/seed`, type a seed or let it pick one, and apply it to everyone or to one player. A player's own seed wins over the shared one; clearing the seeds makes levels random again. Spelling, case and extra spaces do not matter. The game has no seed field, so seeds are set on the server, and the page has no password.

Players on different servers get the same levels only if their **challenge codes** match (the seed plus the index fingerprint, shown on the page). Without an index, seeds still work but pick from whatever levels each server has downloaded, so they match only on servers with identical level sets. Seeded draws are pinned by a known-answer test, so a seed keeps its meaning across releases; a changed algorithm has to bump the version in the code, which changes every code.

## Building and tests

```
node tools/build-release.mjs                       # all platforms into dist/
node tools/build-release.mjs windows-amd64         # or just one
cd server && go build -o ../smm-server.exe .       # plain developer build
```

An archive can also be built from SMMDB downloads instead of fetched levels (optional, needs Node):

```
node tools/import-archive.mjs --archive my-archive --meta levels.json "[942697] Some Level.zip" ...
SMM_DATA_DIR=my-archive ./smm-server.exe
```

Tests:

```
cd server && go test ./...                       # SQL against SQLite; built-in S3 against the real minio client
cd tools  && node --test                         # ASH0 codec vs a port of the reference decoder, meta_binary
SMM_IMPORT_ARCHIVE=<archive> go test ./database/... -run ImportedArchive   # Node-written catalog read by Go
node tools/nex-smoke/run-smoke.mjs --archive <archive> --level <id> --name "<level name>"   # real NEX client, end to end, offline (see tools/README.md)
node tools/nex-smoke/run-seeds.mjs --index <index.sqlite>                                   # index and seeds, two players, real downloads
# against a running server with fetching on (talks to the public services):
python tools/nex-smoke/nex_live.py --account http://127.0.0.1:8080 --ondemand <level id> --difficulty 300000000:1:49
```

No level files are committed. The few tests that need a real course file are skipped unless one is added (see `server/levelimport/testdata/README.md`).

## Status and known limits

**Works.** In Cemu 2.6 (USA, v240) the game logs in, lists levels from Course World and 100 Mario Challenge, downloads and plays them, and shows creator Miis and names in lists and on clear screens. A NEX client test (`tools/nex-smoke`) also covers login and downloads without the game.

**Not tried.**

- A real Wii U. [docs/setup-wiiu.md](docs/setup-wiiu.md) describes the route (Inkay plus the built-in proxy on port 8888 that stands in for `account.pretendo.cc`) and what to report if it fails. It needs an account that already exists on the console (`smm-server account adopt`); creating or linking accounts, SSSL, the shop, Miiverse and friends are not built.
- Other game regions, and the 3DS.
- `smm-server account new` has not been run against Cemu yet. It writes the same fields as a hand-made account that worked.

**Flags are a guess.** The game shows a maker's country from the first tag of their profile. The archive has no country, so the server derives one from the region/language code stored with each Mii (0 Japan, 1 USA, 2 Canada, 3 Mexico, 4 UK, 5 France, 6 Spain, 7 Germany, 8 Italy, 9 Netherlands, 10 Russia, 11 Portugal). An Australian maker therefore shows the UK flag, for instance. The country numbers follow the console's country list as remembered and were checked in the game for Japan only. The 62 oldest profiles have no region code and keep Japan.

**What the game needs from the data** (found by trial in Cemu):

- A maker is shown only if their profile object is in the custom rankings `300000000` and `300001200`. Starring a level adds it; the server adds every profile.
- Every level has rating slots 0 to 5. Slot 5 is created by starring; adding it did not change what is shown, but it matches what the game writes.

**Open questions and guesses.**

- The three unknown `meta_binary` fields, the courses' `data_type` (3), which custom-ranking application ID means "stars" (the importer uses 0), and the exact bands the game uses for Easy, Normal, Expert and Super Expert. Difficulty uses `300000000` in percent; the only sample seen is `300000000 1 49 0`. With `SMM_TRACE=1` the server logs `GetCustomRanking app=<id>`, which shows what the game asks for.
- Whether the game accepts a listed level whose size and `meta_binary` are still empty (set `SMM_LIST_WAIT` if it does not), whether it reshuffles a list on the console, and how the Course World filter maps to clear rate (it is read as a failure rate: Easy 0-34 becomes clear rate 66-100).
- `900000.bin` (the event-course metadata Course World needs) is not available. The server creates an empty placeholder and warns.

**Lost in the archive.**

- Who gave each star (placeholder entries, capped at 500 per level; the true count is kept in the star ranking) and the number of players (the attempt count is used instead).
- A maker's country and profile statistics.
- The world record and the first clear are *not* lost: the per-level API answer has both with real PIDs, and the server fetches them the first time the game asks for a level's course record (one small API call, remembered). A level you clear is therefore no longer a "first clear" and a "new record" every time, and a record you set yourself stays if it is better than the archived one.

**Names and Miis.** The same answer has the NNID and Mii of the creator, the record holder and the first clearer. The game shows a maker through their *profile object* (a DataStore object: owner = the maker's PID, persistence slot 0, type 1, named after the NNID, the Mii as its meta binary), so the server makes that object on the spot when the game asks for a maker the archive knows. Archived levels used to sit in persistence slot 0 too, which made the game read a level where it wanted a Mii; they are in "no slot" (65535) now, and an existing catalog is fixed on start.

**Fetched levels are the original files, byte for byte** (no re-compression), unlike levels rebuilt from SMMDB zips. All 200 live-test files passed validation (four ASH0 chunks, decoded sizes, JPEG thumbnails), so the `meta_binary` chunk split holds on real data. Only the three unknown fields and the CRC rule are unverified.

Upstream quirk left as is: `InitializeObjectByPreparePostParam` builds `extraData` by iterating `Tags`.

## Security

There are **no passwords**. The server never checks one, so anyone who can reach port 8080 can log in as any account, and the challenge-seed page has no password either. Run it on a home network only and never forward its ports on your router.

## Repository layout and upstream

```
server/   the server: a fork of Protarium-Network/super-mario-maker, ported to SQLite with a built-in S3, an account
          service, a proxy for a Wii U, and on-demand fetching of archived levels
tools/    ASH0 codec, .bin and meta_binary builder, archive importer, release packaging, NEX test clients
docs/     setup guides, design notes, and the Mii face fix for Cemu
fake-s3/  the earlier standalone Node S3 stand-in (superseded by server/s3local; kept for reference)
```

`server/` is a fork of [Protarium-Network/super-mario-maker](https://github.com/Protarium-Network/super-mario-maker), which comes from [PretendoNetwork/super-mario-maker](https://github.com/PretendoNetwork/super-mario-maker). This repository keeps that history: the upstream commit `6d49108` was imported into `server/` with `git subtree`, and the home-server changes sit in a commit on top, so `git log server/` and `git blame` show which lines are changed and which are upstream's.

The remotes `upstream` (Protarium, the one `server/` follows) and `pretendo` (the original) can be fetched but not pushed to. To bring in upstream changes:

```
git fetch upstream pretendo
git log --oneline HEAD..upstream/main       # what is new
git subtree pull --prefix=server upstream main -m "Merge upstream"
```

Conflicts can only be in files that were changed here (mostly `server/database/`, `server/nex/register_common_secure_server_protocols.go` and `server/init.go`) and are resolved like any merge. `tools/`, `docs/` and `fake-s3/` exist only in this repository.

Not in the repository (see `.gitignore`): server data (`archive-live/`, `data/`, `secrets.json`), built programs and zips (`dist/`), logs, downloaded reference copies, and level files.

## Licence

AGPL-3.0, like the project it is forked from (see [LICENSE](LICENSE)). If you run a changed version for other people, you must offer them its source code.
