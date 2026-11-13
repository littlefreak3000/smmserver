package pool

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/index"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// A cache is a data folder filled on purpose with archived levels (`smm-server cache build`), as opposed to the pool, which
// fills itself while the server runs. A cache built with a seed is repeatable: the seed alone decides which levels are
// fetched, so everybody who builds the same seed to the same size gets the same levels, and a server that uses it can
// give everybody the same lists (see docs/cache.md).

const (
	cacheFormat  = 1
	manifestName = "cache.json"
	// maxTries is how often one draw is retried after a network problem before a build stops (the build can be started again
	// and carries on at the same draw).
	maxTries = 8
)

// CacheManifest is what `cache.json` in the data folder says about the cache.
type CacheManifest struct {
	Format      int       `json:"format"`
	Seed        string    `json:"seed,omitempty"`      // "" = a random cache, not repeatable
	Space       int       `json:"space,omitempty"`     // the random-index space the draws were taken from
	Wanted      int       `json:"wanted"`              // levels asked for (a multiple of the number of bands)
	NextDraw    int       `json:"next_draw,omitempty"` // seeded builds: the number of the next draw nobody looked at yet
	Cursor      []int     `json:"cursor,omitempty"`    // seeded builds, per band: the first draw that band has not settled yet
	PerBand     []int     `json:"per_band"`
	Levels      int       `json:"levels"`
	Fingerprint string    `json:"fingerprint"` // of the level IDs in the folder (see CacheFingerprint)
	Locked      bool      `json:"locked"`      // the server must not add or fetch levels (keeps the set identical everywhere)
	PlaySeed    string    `json:"play_seed,omitempty"`
	Built       time.Time `json:"built"`
}

// ReadManifest reads cache.json from a data folder; (nil, nil) when there is none.
func ReadManifest(dir string) (*CacheManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if os.IsNotExist(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var m CacheManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s is not readable: %w", manifestName, err)
	}

	return &m, nil
}

// Write stores the manifest in a data folder.
func (m *CacheManifest) Write(dir string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	tmp := filepath.Join(dir, manifestName+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}

	return os.Rename(tmp, filepath.Join(dir, manifestName))
}

// CacheFingerprint is a short code for the set of archived levels a catalog holds: the first 8 hex digits of a SHA-256 over
// the sorted level IDs. Two data folders with the same fingerprint hold the same levels. n is the number of levels.
func CacheFingerprint(db *sql.DB) (fingerprint string, n int, err error) {
	rows, err := db.Query(`SELECT data_id FROM datastore.level_sources WHERE fetched IS NOT NULL ORDER BY data_id`)
	if err != nil {
		return "", 0, err
	}

	defer rows.Close()

	h := sha256.New()

	var buf [8]byte

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return "", 0, err
		}

		binary.BigEndian.PutUint64(buf[:], uint64(id))
		h.Write(buf[:])

		n++
	}

	if err := rows.Err(); err != nil {
		return "", 0, err
	}

	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)[:4])), n, nil
}

// drawIndex is the k-th random index of a seeded build: the same for everyone, whatever machine or time.
func drawIndex(seed string, k, space int) int {
	sum := sha256.Sum256([]byte("smm-cache-v1\x00" + index.NormalizeSeed(seed) + "\x00" + strconv.Itoa(k)))

	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(space))
}

// BuildOptions say what BuildCache builds.
type BuildOptions struct {
	Seed   string // "" builds a random cache
	Levels int    // how many levels (rounded up to a multiple of the number of bands, which are filled evenly)
	Dir    string // the data folder (cache.json lives there)
	Force  bool   // allow a seeded build in a folder that already holds levels (the result is then not repeatable)
}

// BuildCache downloads levels into the catalog until every clear-rate band holds its share, and records what it did in
// cache.json.
//
// A seeded build takes its draws from a fixed sequence (draw 0, 1, 2, ...), so everyone who builds the same seed gets the
// same levels. The rule that makes it extendable is per band: a band keeps the first `per band` valid levels of the
// sequence that fall into it. A later, larger build therefore only adds levels (it continues each band where it stopped)
// and ends exactly like a single large build would. A network problem retries the same draw instead of skipping it, because
// skipping would change the result. Levels that have no usable capture or do not validate are skipped, as on every machine.
func (p *Pool) BuildCache(ctx context.Context, opt BuildOptions) (*CacheManifest, error) {
	seeded := index.NormalizeSeed(opt.Seed) != ""
	bands := len(p.bands())

	if opt.Levels <= 0 {
		return nil, errors.New("pool: the cache must hold at least one level")
	}

	perBand := (opt.Levels + bands - 1) / bands

	space := p.RandomSpace
	if space <= 0 {
		space = upstream.RandomSpace
	}

	m, err := ReadManifest(opt.Dir)
	if err != nil {
		return nil, err
	}

	_, existing, err := CacheFingerprint(p.DB)
	if err != nil {
		return nil, err
	}

	switch {
	case m != nil && index.NormalizeSeed(m.Seed) != index.NormalizeSeed(opt.Seed):
		if m.Seed == "" {
			return nil, errors.New("this data folder holds a random cache; use another folder for a seeded one")
		}

		return nil, fmt.Errorf("this data folder holds the cache of seed %q; use --seed %q to extend it, or another folder", m.Seed, m.Seed)
	case m == nil && seeded && existing > 0 && !opt.Force:
		return nil, fmt.Errorf("this data folder already holds %d levels, so a seeded build here would not be repeatable. Use an empty folder (SMM_DATA_DIR), or --force", existing)
	case m != nil && m.Space != 0 && m.Space != space:
		return nil, fmt.Errorf("the cache was built for index space %d, but this program uses %d", m.Space, space)
	}

	if m == nil {
		words := strings.Join(strings.Fields(opt.Seed), " ")
		m = &CacheManifest{Format: cacheFormat, Seed: words, Space: space, Locked: seeded, PlaySeed: words}
	}

	if len(m.Cursor) != bands {
		m.Cursor = make([]int, bands)
	}

	m.Wanted = perBand * bands
	cursor := m.Cursor

	counts, err := p.Counts()
	if err != nil {
		return nil, err
	}

	save := func() error {
		fp, n, err := CacheFingerprint(p.DB)
		if err != nil {
			return err
		}

		m.Fingerprint, m.Levels, m.PerBand, m.Built = fp, n, append([]int(nil), counts...), time.Now().UTC()

		return m.Write(opt.Dir)
	}

	full := func(i int) bool { return counts[i] >= perBand }

	allFull := func() bool {
		for i := range counts {
			if !full(i) {
				return false
			}
		}

		return true
	}

	backoff := p.ErrorBackoff
	if backoff == 0 {
		backoff = 20 * time.Second
	}

	// * a seeded build starts at the earliest draw some band has not settled; a random one just carries on
	draw := m.NextDraw
	if seeded {
		draw = cursor[0]
		for _, c := range cursor {
			draw = min(draw, c)
		}
	}

	imported, savedAt, tries := 0, 0, 0

	for !allFull() {
		if err := ctx.Err(); err != nil {
			_ = save()

			return m, err
		}

		idx := 0
		if seeded {
			idx = drawIndex(m.Seed, draw, space)
		} else {
			idx = p.randIndex()
		}

		open := make([]bool, bands) // bands that still wanted levels when this draw started
		for i := range open {
			open[i] = !full(i)
		}

		lvl, err := p.Src.RandomLevel(ctx, idx)
		if err == nil {
			var kept bool

			kept, err = p.consider(ctx, lvl, counts, perBand, cursor, draw, seeded)
			if kept {
				imported++
			}
		}

		if err != nil {
			if ctx.Err() != nil {
				_ = save()

				return m, ctx.Err()
			}

			if tries++; tries >= maxTries {
				_ = save()

				return m, fmt.Errorf("pool: the archive services keep failing (%w); run the same command again to carry on", err)
			}

			p.logf("cache: draw %d failed (%v); trying again in %s", draw, err, backoff)

			if err := sleep(ctx, backoff); err != nil {
				_ = save()

				return m, err
			}

			continue
		}

		tries = 0

		// * every band that was still open has now settled this draw
		for i := range cursor {
			if open[i] {
				cursor[i] = max(cursor[i], draw+1)
			}
		}

		draw++
		m.NextDraw = max(m.NextDraw, draw)

		if imported-savedAt >= 25 {
			p.logf("cache: %d levels so far; bands %v of %d each (draw %d)", imported, counts, perBand, draw)

			if err := save(); err != nil {
				return m, err
			}

			savedAt = imported
		}
	}

	if err := save(); err != nil {
		return m, err
	}

	return m, nil
}

// consider decides one draw. kept reports that a level was imported. An error is a network problem: the caller tries the
// same draw again; every other outcome is final.
func (p *Pool) consider(ctx context.Context, lvl *upstream.Level, counts []int, perBand int, cursor []int, draw int, seeded bool) (kept bool, err error) {
	if lvl == nil || lvl.TotalAttempts <= 0 {
		return false, nil // nothing at that index, or no win rate to sort it by
	}

	b := p.bandOf(lvl.ClearRate)
	if b < 0 || counts[b] >= perBand {
		return false, nil
	}

	if seeded && draw < cursor[b] {
		return false, nil // this band settled this draw in an earlier, smaller build
	}

	if p.known(lvl.LevelID) {
		return false, nil
	}

	bin, err := p.Src.FetchBin(ctx, lvl.URL)
	if err != nil {
		if errors.Is(err, upstream.ErrNotFound) {
			p.logf("cache: level %d has no usable capture; skipped", lvl.LevelID)

			return false, nil
		}

		return false, err
	}

	if err := levelimport.Import(p.DB, *lvl, bin, p.Import); err != nil {
		p.logf("cache: level %d skipped: %v", lvl.LevelID, err)

		return false, nil
	}

	counts[b]++

	return true, nil
}
