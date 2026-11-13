package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/challenge"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/index"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	nex_datastore_super_mario_maker "github.com/PretendoNetwork/super-mario-maker/nex/datastore/super-mario-maker"
	"github.com/PretendoNetwork/super-mario-maker/pool"
	"github.com/PretendoNetwork/super-mario-maker/selection"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// Whole-archive index and challenge seeds (see docs/whole-archive-index.md).
//
//	SMM_INDEX         path of the index file (default <data>/index.sqlite when it exists; 0 = do not use it).
//	                  Build one with: smm-server index build <levels.jsonl[.gz|.zst]>
//	SMM_LIST_WAIT     seconds a course list may wait for its levels to finish downloading before it is sent (default 0)
//
// With an index, every level of the archive can come up in a list; the chosen levels are listed straight away and downloaded
// in the background (or at the latest when the game opens one). Challenge seeds are set on the web page http://<host>:8080/seed.

var (
	fetchPool   *pool.Pool // nil when fetching is off
	archiveIdx  *index.Index
	seedStore   *challenge.Store
	seedInfoMu  sync.RWMutex
	seedMode    = "levels already downloaded to this server"
	seedFinger  string
	seedPageURL string
)

func ensureSeedStore() *challenge.Store {
	if seedStore == nil {
		seedStore = &challenge.Store{DB: database.DB}
	}

	return seedStore
}

// seedInfo is what the /seed page shows about the server.
func seedInfo() challenge.Info {
	seedInfoMu.RLock()
	info := challenge.Info{Mode: seedMode, Fingerprint: seedFinger}
	seedInfoMu.RUnlock()

	if rows, err := database.DB.Query(`SELECT nnid FROM accounts ORDER BY nnid`); err == nil {
		defer rows.Close()

		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				info.Players = append(info.Players, n)
			}
		}
	}

	return info
}

// mountSeedPage serves /seed and /seed.json next to the account service.
func mountSeedPage(account http.Handler) http.Handler {
	seed := ensureSeedStore().Handler(seedInfo)

	mux := http.NewServeMux()
	mux.Handle("/seed", seed)
	mux.Handle("/seed.json", seed)
	mux.Handle("/", account)

	return mux
}

// openIndex opens the whole-archive index if there is one. A missing default file is normal; a missing file the user named is not.
func openIndex() *index.Index {
	path, explicit := os.Getenv("SMM_INDEX"), os.Getenv("SMM_INDEX") != ""

	switch strings.ToLower(path) {
	case "0", "off", "no":
		return nil
	case "":
		path = filepath.Join(dataDirPath, "index.sqlite")
	}

	if _, err := os.Stat(path); err != nil {
		if explicit {
			globals.Logger.Warningf("SMM_INDEX=%s: %s. Continuing without the whole-archive index", path, err.Error())
		}

		return nil
	}

	x, err := index.Open(path)
	if err != nil {
		globals.Logger.Warningf("Cannot open the whole-archive index %s: %s. Continuing without it", path, err.Error())
		return nil
	}

	return x
}

// startIndexMode switches the pool to the whole-archive index: levels come from the index, stubs are completed on demand,
// and the background "stock the bands" pool is not needed any more.
func startIndexMode(p *pool.Pool, x *index.Index) {
	archiveIdx = x

	p.Lookup = func(id int64) (*upstream.Level, error) {
		l, err := x.ByID(id)
		if err != nil || l == nil {
			return nil, err
		}

		u := l.Upstream()

		return &u, nil
	}

	go p.RunPrefetch(context.Background())

	seedInfoMu.Lock()
	seedMode = fmt.Sprintf("the whole archive index (%d levels, fingerprint %s)", x.Total(), x.Fingerprint())
	seedFinger = x.Fingerprint()
	seedInfoMu.Unlock()

	globals.Logger.Successf("Whole-archive index: %d levels (fingerprint %s). Lists draw from all of them; levels are downloaded when needed (SMM_POOL_TARGET is not used)", x.Total(), x.Fingerprint())
}

// applyCacheToSeeds makes the seed page show the cache (its fingerprint is part of the challenge code, so players can see that
// their copies match) and, when the cache was built with a seed, uses that seed for everybody unless a seed is already set.
func applyCacheToSeeds(store *challenge.Store) {
	m := cacheManifest
	if m == nil || archiveIdx != nil {
		return
	}

	fp, n, err := pool.CacheFingerprint(database.DB)
	if err != nil {
		globals.Logger.Warningf("Cannot fingerprint the cache: %s", err.Error())

		return
	}

	seedInfoMu.Lock()
	seedMode = fmt.Sprintf("a cache of %d levels (fingerprint %s)", n, fp)
	seedFinger = fp
	seedInfoMu.Unlock()

	if m.Locked && fp != m.Fingerprint {
		globals.Logger.Warningf("The levels in this folder (fingerprint %s) are not the cache the manifest describes (%s): players with the original cache will see different levels", fp, m.Fingerprint)
	}

	if m.PlaySeed != "" && store.Effective("") == "" {
		if err := store.Set(challenge.Everyone, m.PlaySeed); err != nil {
			globals.Logger.Warningf("Cannot set the cache's seed %q: %s", m.PlaySeed, err.Error())

			return
		}

		globals.Logger.Infof("Cache seed %q is active for every player (the /seed page can change it). Challenge code: %s", m.PlaySeed, challenge.Code(m.PlaySeed, fp))
	}
}

// startSelection connects the course lists to the index and the challenge seeds. Needs the catalog open; runs after startFetching.
func startSelection() {
	store := ensureSeedStore()

	applyCacheToSeeds(store)

	sel := &selection.Selector{
		DB: database.DB, Index: archiveIdx, Seeds: store, DifficultyApp: 300000000,
		Logf: func(f string, a ...any) { globals.Logger.Infof(f, a...) },
	}

	// * pool mode: levels just played go to the back of random lists, and the pool grows where the unplayed ones run low
	if archiveIdx == nil {
		nex_datastore_super_mario_maker.Played = func(id uint64) { sel.MarkPlayed(int64(id)) }

		if fetchPool != nil {
			sel.NeedMore = func(lo, hi int64, unseen int) { fetchPool.Boost(context.Background(), lo, hi) }
		}
	}

	if fetchPool != nil && archiveIdx != nil {
		sel.Stub = fetchPool.Stub
		sel.Prefetch = fetchPool.Prefetch
		sel.Dead = fetchPool.IsDead

		timeout := 20 * time.Second

		nex_datastore_super_mario_maker.EnsureFetched = func(id uint64) bool {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			ok, err := fetchPool.EnsureFetched(ctx, int64(id))
			if err != nil {
				globals.Logger.Warningf("Downloading level %d failed: %s", id, err.Error())
			}

			return ok
		}

		wait, err := strconv.ParseFloat(getenv("SMM_LIST_WAIT", "0"), 64)
		if err != nil || wait < 0 {
			globals.Logger.Warningf("SMM_LIST_WAIT must be a number of seconds >= 0; using 0")
			wait = 0
		}

		if wait > 0 {
			budget := time.Duration(wait * float64(time.Second))

			nex_datastore_super_mario_maker.Settle = func(ids []int64) {
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()

				for _, id := range ids {
					if ctx.Err() != nil {
						return
					}

					fetchPool.EnsureFetched(ctx, id) //nolint:errcheck
				}
			}
		}
	}

	if fetchPool != nil {
		nex_datastore_super_mario_maker.EnsureRecord = func(id uint64) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if _, err := fetchPool.EnsureRecord(ctx, int64(id)); err != nil {
				globals.Logger.Warningf("Looking up the record of level %d failed: %s", id, err.Error())
			}
		}
	}

	// * profiles built earlier lack the rating slots the game gives its own profile
	if err := levelimport.AddProfileRatings(database.DB); err != nil {
		globals.Logger.Warningf("Could not complete the archive profiles: %s", err.Error())
	}

	if err := levelimport.FixProfileCountries(database.DB); err != nil {
		globals.Logger.Warningf("Could not set the profile countries: %s", err.Error())
	}

	if err := levelimport.AddProfileRankings(database.DB); err != nil {
		globals.Logger.Warningf("Could not add the profile rankings: %s", err.Error())
	}

	// * levels imported earlier lack rating slot 5, without which the game shows no creator Mii
	if err := levelimport.AddLevelRatings(database.DB); err != nil {
		globals.Logger.Warningf("Could not complete the archive levels: %s", err.Error())
	}

	nex_datastore_super_mario_maker.Selector = sel
	nex_datastore_super_mario_maker.NNIDForPID = func(pid uint32) string {
		var nnid string
		if err := database.DB.QueryRow(`SELECT nnid FROM accounts WHERE pid = $1`, pid).Scan(&nnid); err != nil {
			return ""
		}

		return nnid
	}

	if seedPageURL != "" {
		globals.Logger.Infof("Challenge seeds: open %s to make friends play the same \"random\" levels", seedPageURL)
	}
}
