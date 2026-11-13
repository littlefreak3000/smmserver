# Caches: building a set of levels on purpose

A **cache** is a data folder that was filled with archived levels on purpose, instead of letting the server fill it while you play. There are three reasons to build one:

- **Everyone plays the same levels.** Built with a *seed*, a cache holds the same levels on every computer. That suits a contest, or friends who want the same "random" levels on different PCs.
- **A big collection.** If you have the disk space you can build thousands of levels ahead of time and play offline.
- **Sharing.** One person builds a cache, packs it into a zip, and the others unpack it instead of downloading every level themselves.

A cache is made with `smm-server cache ...`. Close the server first: the commands change the data folder.

## Build one

```
smm-server cache build --seed "spring contest" --levels 400
```

- `--levels N` is how many levels. It is rounded up to a multiple of 4: the server keeps four difficulty groups (by clear rate, as in the game's Mario Challenge) and fills them evenly.
- `--seed WORDS` makes the build **repeatable**. The seed alone decides which levels are downloaded, so everyone who builds the same seed to the same size gets the same levels. Spelling, case and extra spaces do not matter. Without `--seed` the levels are random and cannot be reproduced.
- Each level takes about 50 KB of disk and roughly two seconds to download, at the polite request rate of the public archive services. 400 levels is about 20 MB and 15 minutes.
- Ctrl+C stops it. Running the same command again carries on where it stopped, and the result is the same as if it had never stopped. Network problems are retried and never change which levels you end up with.
- Run it again with a bigger `--levels` (same seed) to **extend** the cache. This only adds levels. The result is the same as building the larger size in one go.
- `SMM_DATA_DIR` chooses the data folder (default `./data`). A seeded build needs a folder that holds no levels yet, because levels already there would make the result different from other people's. Use a fresh folder, for example `SMM_DATA_DIR=./contest smm-server cache build ...`. `--force` overrides that, and the result is then no longer repeatable.

When it finishes it prints the **fingerprint**: eight characters that identify the exact set of levels (for example `3DCAD00B`). Two data folders with the same fingerprint hold the same levels. For a contest, announce the seed, the size and the fingerprint; every player checks theirs against it.

```
smm-server cache info
```

shows the folder's level count, size, fingerprint, seed and whether it is locked.

## Play on a cache

A seeded cache is **locked** automatically. Start the server in that data folder as usual:

- The server **does not fetch or add levels**, so the set stays exactly as built. (The size limit `SMM_CACHE_MAX_MB` also does nothing while a cache is locked, so it never removes a cache's levels.)
- The seed is switched on for everyone, so "random" lists draw the same levels in the same order for every player. The **challenge code** (the seed plus the fingerprint) is shown in the log and on the server's `/seed` page.
- If the levels in the folder no longer match what the cache says, the server warns at start-up.

`smm-server cache unlock` lets the server fetch levels again, and `smm-server cache lock` fixes the set again. After an unlock the folder stops matching other copies.

## Share one

```
smm-server cache export my-cache.zip
```

packs the levels, their files and the manifest into one zip. **Players' data is not included:** accounts, the profiles and records of players on this server and challenge seeds are left out. What players did to the levels (stars, ratings) stays, so build a cache you intend to share in a folder nobody has played in.

On another computer, with the server closed:

```
smm-server cache import my-cache.zip
```

This unpacks into an empty data folder (`--force` replaces what is there) and checks the result against the fingerprint in the zip. The same fingerprint is the proof that both computers hold the same levels. The zip only accepts the files of a cache, nothing else.

A contest organiser can build the cache once, share the zip, and everyone then plays the same levels without downloading anything from the public services.

## How big can a cache be?

There is no limit in the program other than disk space and time. Roughly:

| Levels | Disk | Download time |
|---|---|---|
| 400 | 20 MB | 15 minutes |
| 4,000 | 200 MB | 2.5 hours |
| 40,000 | 2 GB | 1 day |
| 400,000 | 20 GB | 10 days |

The pace is set by politeness towards two volunteer-run services (a metadata API and the Internet Archive), not by your connection, so for a large cache **build it once and share the zip** instead of everyone downloading the same levels.

**The whole archive** (about 10 million levels, around 470 GB) is not something `--levels` can build: at this pace it would take months of constant requests, which the services should not be asked to handle. That needs a list of all levels, which the program can use as an *index*, and ideally a copy of the level files from the people who keep them. [whole-archive-index.md](whole-archive-index.md) describes that plan. Until then the practical route is large seeded caches shared as zips.

## What can still make two copies differ

- A level that the archive services serve today but not tomorrow (or the reverse) can make a build skip a level. The fingerprint shows it: if two people's fingerprints differ, compare with `cache info` and share one person's zip.
- Seeded builds depend on the archive's index space staying as it is (`18118278` levels). The cache records it and the program refuses to extend a cache built for another one.
- **What is checked and what is not.** Checked by tests and by live runs: that the same seed gives the same levels on separate runs, that an interrupted or extended build ends the same as a single build, that a zip carries levels but not players, and that a locked cache starts the server with the seed active. Not checked: that the game plays the lists of a seeded cache in the same order on two machines in the 100 Mario Challenge.
