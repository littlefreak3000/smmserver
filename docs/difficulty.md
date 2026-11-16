# How levels are sorted by difficulty

The game's difficulty lists (the 100 Mario Challenge and the difficulty filter) ask the server for levels whose **difficulty value** lies in a range. In the original game the value was the clear rate in percent (clears divided by attempts). This server keeps that contract (a whole number from 0 to 100 under ranking 300000000) but computes the number with more care.

## The problem

A clear rate is only as good as the number of attempts behind it. 3 clears in 3 attempts is "100 %" and 0 clears in 5 is "0 %", but neither says much about the level. A new level, or one almost nobody played, would land in the hardest or the easiest list by luck.

## What the archive data looks like

A uniform sample of 300 archived levels (taken on 2026-11-15 from the public metadata API, over random positions of its index; 234 of 534 positions were empty) shows:

| | |
|---|---|
| Attempts, median | 197 |
| 5th percentile | 49 |
| 20 attempts or fewer | 1 level in 300 |
| 50 attempts or fewer | 15 in 300 (5 %) |
| Attempts = clears + failures | 300 of 300 |
| Clear rate, mean of per-level rates | about 0.2 (median 0.135, all attempts pooled 0.148) |

So very thin data is rare in this archive: the problem you would expect from new levels mostly does not occur. A newer level does have fewer attempts (median 130 for the newest third by upload date, 272 for the oldest). Two things are a larger effect than thin data:

- **Sampling noise at normal sizes.** With a few hundred attempts a clear rate is still uncertain by a few points. For 113 of the 300 levels the 90 % interval of the true rate crosses one of the band edges (5, 20 and 50 %). No scoring rule removes that.
- **Retries count as attempts.** The clear rate falls as attempts rise (rank correlation -0.35), because a hard level is retried many times by the same player. It also falls with the world record time (-0.53), which tracks how long the level is. The archive has no per-player counts, so neither can be corrected from the metadata alone.

## What is built

The score is the posterior mean of the clear rate under a Beta prior, which works like adding a few attempts at a typical level's clear rate:

```
score = (clears + mean × strength) / (attempts + strength)
```

With many attempts the score is the raw rate; with few, it moves towards the typical level. A level without attempts has no score and is left out of difficulty lists, as before.

The default is `mean 0.2, strength 3`. The mean is the typical level's clear rate from the sample above. The strength comes from fitting a Beta distribution to the levels with 50 or more attempts, which gives a prior worth about two attempts (the levels differ so much that the prior can say little), rounded up to three. Effect on the sample of 300:

- 88 levels get a different whole-percent value, most by a point or two; the largest shift is 10 points, for the thinnest levels (21 clears in 21 attempts becomes 90, 4 in 8 becomes 42).
- 8 levels (2.7 %) change which of the four bands (0-5, 5-20, 20-50, 50-100 %) they are in, for example 1 clear in 25 attempts goes from 4 % to 6 %.
- A level with 400 attempts or more moves less than a point.

The score is used in four places:

- the difficulty ranking value stored for every level (what the game's difficulty requests filter on);
- the bands of the pool, which keeps each band stocked;
- the buckets of the whole-archive index;
- the bands of `smm-server cache build`.

## Settings

`SMM_DIFFICULTY_PRIOR` in `data/settings.env`:

| Value | Meaning |
|---|---|
| (not set) | the default, `0.2,3` |
| `MEAN,STRENGTH` | for example `0.2,10` for a stronger pull towards the typical level |
| `off` | the raw clear rate, as before |

A stronger prior stabilises thin levels more and moves well-played levels more too. At start-up the server recomputes the difficulty value of the levels already in the catalog from the raw counts it kept, so changing the setting needs only a restart.

**Caches.** Which band a level falls in depends on the prior, so it is part of what a seed means. A cache records the prior it was built with in `cache.json`, `cache build` continues a cache with the cache's own prior and refuses another, and a locked cache keeps its prior whatever the setting says. Caches built before this setting existed used raw clear rates and keep doing so.

**The whole-archive index.** Its buckets are scores, so an index records its prior in its metadata (`smm-server index build --prior`, default from the setting) and a server that uses it adopts that prior. Rebuild an index when you change the prior.

## Not built

These were considered and left out; each needs a decision about what "difficulty" should mean, or data this archive does not have.

- **Soft band edges.** Accept a level for a range with the probability that its true rate lies in the range, so borderline levels appear in both neighbouring lists. It needs rejection sampling in the index and the catalog queries.
- **Correcting for retries or length.** The world record time is in the metadata and correlates with the clear rate, so a long level with a low rate could be told apart from a short, cruel one. Whether a long level should rank as harder than a short one with the same rate is a design choice. The level file itself (length, enemies, time limit) holds more signal but is only available after download.
- **Excluding thin levels from the hardest and easiest lists** instead of moving them. Easy to add, but the archive has so few of them that it hardly matters.
