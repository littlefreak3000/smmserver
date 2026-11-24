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

## Confidence: thin levels are placed in a wider range

The score alone still gives every level one fixed place. But a level with few plays could belong to a neighbouring range as easily as to its own, and a level with many plays could not. So when the game asks for levels in a difficulty range, each level is offered with a **confidence**: the probability that its true clear rate lies in the requested range. It comes from the same Beta distribution the score is the mean of, with the level's own clears and failures as the evidence:

```
confidence = P( lo ≤ true clear rate ≤ hi )     where  rate ~ Beta(clears + mean × strength, failures + (1 − mean) × strength)
```

A level with 600 attempts at 12 % is, for practical purposes, certain to be in the range 5-19 and is never offered for 20-49. The same 12 % on 12 attempts is 66 % sure of 5-19, 22 % sure of 20-49 and 12 % sure of 0-4, so it turns up in all three. The less is known about a level, the more ranges it can turn up in, in proportion to the doubt. The numbers come from the data, not from a setting: more attempts narrow the distribution.

How a list is drawn:

- Candidates come from the range widened by 25 points on each side (levels further away have almost no chance of belonging), and each is accepted with its confidence. This is how the whole-archive index draws, and how the plain catalog draws a difficulty range (with a seed or without).
- A request for the whole scale (0-100) is not weighted. Levels that are not from the archive (made on this server) have no counts and are in or out by their value.
- A seeded list is still a pure function of the seed and the levels: the coin that decides each acceptance comes from the seed's own stream (index) or from the seed's hash of the level (catalog). A longer request still starts with exactly the levels of a shorter one.
- With `SMM_DIFFICULTY_PRIOR=off` there is no distribution to ask, and range membership is as sharp as before.

Measured on the sample of 300 (the default prior):

| | |
|---|---|
| Mean confidence of a level in its own band | 91 % (median 98 %) |
| Levels less than 90 % sure of their band | 87 of 300 |
| Levels less than 70 % sure | 31 of 300 |
| Levels less than 50 % sure | 2 of 300 |
| Share of a list that comes from a neighbouring band | 13 % for 0-4, 6 % for 5-19, 12 % for 20-49, 6 % for 50-100 |

So a list for a range is mostly its own levels, with a few levels from next door that could just as well belong there: a level with 4 clears in 8 attempts is 65 % sure of its band, one with 1 clear in 25 only 51 %.

An index built with a prior is drawn this way and an index built without is not, so **the prior is part of the index's fingerprint**, and with it of every challenge code: two players whose codes match drew from the same kind of index. An index without a prior keeps the fingerprint it always had.

## Filtering out the easiest levels

`SMM_DIFFICULTY_CEILING` (percent, 1 to 100, default 100) sets the highest difficulty score a level may have. Below 100 it does two things:

- the pool's top band ends there instead of at 100 %, so a level whose score is above it belongs to no band and is never downloaded;
- every difficulty request is cut off at the ceiling: a range that reaches past it stops there, "all difficulties" becomes 0 to the ceiling, and a range wholly above it (Easy, 66-100 %, under a ceiling of 60) gets the easiest levels that are allowed. The neighbouring-range rule above cannot reach past it either. Levels already in the catalog above the ceiling stay in the files but are not offered in difficulty lists.

It applies to the difficulty lists only (the 100 Mario Challenge and the difficulty filter). The newest and most-starred lists, and the whole-archive index (where the neighbouring-range rule can still reach a few points above), are not filtered. The ceiling must lie above the start of the pool's top band (50 % by default, see `SMM_POOL_BANDS` in the README).

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

- **Correcting for retries.** Attempts include every retry by the same player, so a hard level that people keep trying has a lower clear rate than the share of players who finish it. The archive has no per-player counts, so this cannot be corrected from the metadata.
- Level length is not treated as a measure of difficulty and plays no part in any of this.
