// Package pool keeps the catalog stocked with archived levels, fetched from the public archive as needed.
//
//   - Fill samples random levels and imports them until every clear-rate band holds TargetPerBand levels.
//     The catalog itself is the state, so a restart resumes where it stopped and a full pool costs nothing.
//     Only levels for bands that still need them are downloaded.
//   - Ensure / OnMissing fetch one specific level when the game asks for an ID the catalog does not have
//     (typing a course code, opening a shared level). Misses are remembered for a while so unknown IDs do not
//     hammer the upstream services.
//
// Levels are small (tens of KB), so a lookup takes a few seconds, dominated by the upstream pacing.
package pool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// Source is what the pool needs from the upstream services (*upstream.Client implements it).
type Source interface {
	RandomLevel(ctx context.Context, n int) (*upstream.Level, error)
	Level(ctx context.Context, id int64) (*upstream.Level, error)
	FetchBin(ctx context.Context, originalURL string) ([]byte, error)
}

// Band is a clear-rate range [Min, Max) as a fraction (0.05 = 5%).
type Band struct {
	Name     string
	Min, Max float64
}

// DefaultBands splits levels from "almost nobody clears it" to "most players clear it".
var DefaultBands = []Band{
	{"0-5%", 0, 0.05},
	{"5-20%", 0.05, 0.20},
	{"20-50%", 0.20, 0.50},
	{"50-100%", 0.50, 1.0000001},
}

// ErrMaxDraws means Fill used up its draw budget before every band was full.
var ErrMaxDraws = errors.New("pool: draw budget used up before every band was full")

type Pool struct {
	DB            *sql.DB
	Src           Source
	Import        levelimport.Options
	Bands         []Band
	TargetPerBand int
	MaxDraws      int           // safety cap per Fill (default 40 draws per wanted level)
	RandomSpace   int           // random index bound (default upstream.RandomSpace)
	Timeout       time.Duration // per on-demand lookup (default 20 s)
	ErrorBackoff  time.Duration // pause after a burst of upstream errors (default 30 s)
	MissTTL       time.Duration // how long a "not in the archive" answer is remembered (default 30 min)
	Rand          func(n int) int
	Logf          func(format string, args ...any)

	// Lookup returns the metadata of a level from the whole-archive index (nil, nil = not in it). Set in index mode;
	// it lets EnsureFetched list a level before its file is downloaded.
	Lookup func(id int64) (*upstream.Level, error)

	boosting atomic.Bool // a Boost is running

	mu     sync.Mutex // serialises on-demand imports
	missMu sync.Mutex
	misses map[int64]time.Time

	recordMu      sync.Mutex          // one per-level lookup at a time
	profileMisses map[int64]time.Time // players the archive knows nothing about (guarded by missMu)

	flightMu sync.Mutex
	flights  map[int64]*flight
	queue    chan int64
	queuedMu sync.Mutex
	queued   map[int64]bool
}

// Stats describe one Fill run.
type Stats struct {
	Draws, Empty, NoAttempts, Duplicates, BandFull, Imported, FetchFailed, Invalid, APIErrors int
	PerBand                                                                                   []int
}

func (p *Pool) logf(f string, a ...any) {
	if p.Logf != nil {
		p.Logf(f, a...)
	}
}

func (p *Pool) bands() []Band {
	if len(p.Bands) == 0 {
		return DefaultBands
	}

	return p.Bands
}

func (p *Pool) bandOf(rate float64) int {
	if rate < 0 {
		rate = 0
	}

	if rate > 1 {
		rate = 1
	}

	for i, b := range p.bands() {
		if rate >= b.Min && rate < b.Max {
			return i
		}
	}

	return -1
}

// Counts returns how many archived levels (with at least one attempt) each band holds.
func (p *Pool) Counts() ([]int, error) {
	out := make([]int, len(p.bands()))

	for i, b := range p.bands() {
		if err := p.DB.QueryRow(`SELECT COUNT(*) FROM datastore.level_sources WHERE attempts > 0 AND clear_rate >= $1 AND clear_rate < $2`,
			b.Min, b.Max).Scan(&out[i]); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (p *Pool) known(id int64) bool {
	var one int

	return p.DB.QueryRow(`SELECT 1 FROM datastore.objects WHERE data_id = $1`, id).Scan(&one) == nil
}

func (p *Pool) randIndex() int {
	space := p.RandomSpace
	if space <= 0 {
		space = upstream.RandomSpace
	}

	if p.Rand != nil {
		return p.Rand(space)
	}

	return rand.IntN(space)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Fill imports random levels until every band holds TargetPerBand of them (counting what is already there).
func (p *Pool) Fill(ctx context.Context) (Stats, error) {
	return p.fill(ctx, p.TargetPerBand, p.MaxDraws)
}

// fill imports random levels until every band holds target of them, giving up after maxDraws API draws
// (0 = 40 draws per wanted level).
func (p *Pool) fill(ctx context.Context, target, maxDraws int) (Stats, error) {
	targets := make([]int, len(p.bands()))
	for i := range targets {
		targets[i] = target
	}

	return p.fillTo(ctx, targets, maxDraws)
}

// fillTo imports random levels until band i holds targets[i] of them (a band with target 0 is left alone), giving up
// after maxDraws API draws (0 = 40 draws per wanted level).
func (p *Pool) fillTo(ctx context.Context, targets []int, maxDraws int) (Stats, error) {
	var st Stats

	counts, err := p.Counts()
	if err != nil {
		return st, err
	}

	st.PerBand = counts

	full := func() bool {
		for i, c := range counts {
			if c < targets[i] {
				return false
			}
		}

		return true
	}

	if maxDraws <= 0 {
		for i, c := range counts {
			maxDraws += 40 * max(targets[i]-c, 0)
		}
	}

	backoff := p.ErrorBackoff
	if backoff == 0 {
		backoff = 30 * time.Second
	}

	consecutive, cycles := 0, 0

	for !full() {
		if st.Draws >= maxDraws {
			return st, ErrMaxDraws
		}

		if err := ctx.Err(); err != nil {
			return st, err
		}

		st.Draws++

		lvl, err := p.Src.RandomLevel(ctx, p.randIndex())
		if err != nil {
			if ctx.Err() != nil {
				return st, ctx.Err()
			}

			st.APIErrors++
			consecutive++

			if consecutive >= 6 {
				cycles++
				if cycles > 3 {
					return st, fmt.Errorf("pool: upstream keeps failing: %w", err)
				}

				p.logf("pool: %d upstream errors in a row (%v); pausing %s", consecutive, err, backoff)

				if err := sleep(ctx, backoff); err != nil {
					return st, err
				}

				consecutive = 0
			}

			continue
		}

		consecutive = 0

		switch {
		case lvl == nil:
			st.Empty++
			continue
		case lvl.TotalAttempts <= 0:
			st.NoAttempts++ // no win rate to sort it by
			continue
		}

		b := p.bandOf(lvl.ClearRate)
		if b < 0 {
			continue
		}

		if counts[b] >= targets[b] {
			st.BandFull++
			continue
		}

		if p.known(lvl.LevelID) {
			st.Duplicates++
			continue
		}

		bin, err := p.Src.FetchBin(ctx, lvl.URL)
		if err != nil {
			if ctx.Err() != nil {
				return st, ctx.Err()
			}

			st.FetchFailed++
			p.logf("pool: level %d: no usable capture (%v)", lvl.LevelID, err)

			continue
		}

		if err := levelimport.Import(p.DB, *lvl, bin, p.Import); err != nil {
			st.Invalid++
			p.logf("pool: level %d skipped: %v", lvl.LevelID, err)

			continue
		}

		counts[b]++
		st.Imported++

		if st.Imported%10 == 0 {
			p.logf("pool: %d imported so far; bands %v of %v (%d draws)", st.Imported, counts, targets, st.Draws)
		}
	}

	st.PerBand = counts

	return st, nil
}

// BoostBy is how many levels one Boost adds to each band it covers.
const BoostBy = 12

// Boost fetches more levels, in the background, for the bands that overlap the clear-rate range [minPct, maxPct]
// (percent). The game's lists ran low on levels nobody played recently there, so the next lists have fresh ones.
// One boost runs at a time; a call while another runs does nothing, and a band that cannot be filled right now is
// simply tried again at the next call. It returns at once.
func (p *Pool) Boost(ctx context.Context, minPct, maxPct int64) {
	if !p.boosting.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer p.boosting.Store(false)

		counts, err := p.Counts()
		if err != nil {
			p.logf("pool: cannot count levels: %v", err)

			return
		}

		targets := make([]int, len(counts))
		wanted := false

		for i, b := range p.bands() {
			if b.Min*100 <= float64(maxPct)+0.999 && b.Max*100 > float64(minPct) {
				targets[i] = counts[i] + BoostBy
				wanted = true
			}
		}

		if !wanted {
			return
		}

		st, err := p.fillTo(ctx, targets, 60*BoostBy*len(counts)+60)
		if ctx.Err() != nil {
			return
		}

		if st.Imported > 0 {
			p.logf("pool: lists were running out of unplayed levels in %d-%d%%; added %d, bands now %v", minPct, maxPct, st.Imported, st.PerBand)
		}

		if err != nil && !errors.Is(err, ErrMaxDraws) {
			p.logf("pool: boost failed: %v", err)
		}
	}()
}

// Growth makes the pool grow slowly while the server runs: every Every, each band gets By more levels, until
// every band holds Max. Zero Every or By disables growth.
type Growth struct {
	Every time.Duration
	By    int
	Max   int
}

// Maintain stocks the pool to TargetPerBand, then (if g asks for it) keeps growing it gently until ctx is cancelled
// or every band reaches g.Max. A growth step has a small draw budget, so a rare band that cannot be filled right now
// never turns into a long burst of requests; it simply gets another chance at the next step.
func (p *Pool) Maintain(ctx context.Context, g Growth) {
	if p.TargetPerBand > 0 {
		st, err := p.Fill(ctx)

		switch {
		case err == nil:
			p.logf("pool: stocked with %d levels per band %v (%d imported, %d draws)", p.TargetPerBand, st.PerBand, st.Imported, st.Draws)
		case ctx.Err() != nil:
			return
		default:
			p.logf("pool: initial fill stopped early: %v (%+v)", err, st)
		}
	}

	if g.Every <= 0 || g.By <= 0 {
		return
	}

	t := time.NewTicker(g.Every)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		counts, err := p.Counts()
		if err != nil {
			p.logf("pool: cannot count levels: %v", err)
			continue
		}

		lowest := counts[0]
		for _, c := range counts {
			lowest = min(lowest, c)
		}

		if g.Max > 0 && lowest >= g.Max {
			p.logf("pool: every band holds %d levels (the maximum); growth stops", g.Max)
			return
		}

		target := lowest + g.By
		if g.Max > 0 {
			target = min(target, g.Max)
		}

		st, err := p.fill(ctx, target, 60*g.By*len(counts)+60)
		if ctx.Err() != nil {
			return
		}

		if st.Imported > 0 {
			p.logf("pool: grew by %d levels, bands now %v (%d draws)", st.Imported, st.PerBand, st.Draws)
		}

		if err != nil && !errors.Is(err, ErrMaxDraws) {
			p.logf("pool: growth step failed: %v", err)
		}
	}
}

// Ensure makes sure level id is in the catalog, fetching it from the archive if necessary. It reports whether
// the level is available afterwards.
func (p *Pool) Ensure(ctx context.Context, id int64) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.known(id) {
		return true, nil
	}

	ttl := p.MissTTL
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	p.missMu.Lock()
	if t, ok := p.misses[id]; ok && time.Since(t) < ttl {
		p.missMu.Unlock()
		return false, nil
	}
	p.missMu.Unlock()

	lvl, err := p.Src.Level(ctx, id)
	if err != nil {
		return false, err // transient: do not remember it as a miss
	}

	remember := func() {
		p.missMu.Lock()
		defer p.missMu.Unlock()

		if p.misses == nil {
			p.misses = map[int64]time.Time{}
		}

		p.misses[id] = time.Now()
	}

	if lvl == nil || lvl.LevelID != id {
		remember()
		return false, nil
	}

	bin, err := p.Src.FetchBin(ctx, lvl.URL)
	if err != nil {
		if errors.Is(err, upstream.ErrNotFound) {
			remember()
		}

		return false, err
	}

	if err := levelimport.Import(p.DB, *lvl, bin, p.Import); err != nil {
		if !errors.Is(err, levelimport.ErrLocalObject) {
			remember()
		}

		return false, err
	}

	p.logf("pool: fetched level %d %q on demand", id, lvl.Name)

	return true, nil
}

// OnMissing is the database.OnMissing hook: a bounded on-demand lookup that never panics or blocks the game
// for long.
func (p *Pool) OnMissing(dataID uint64) bool {
	// * level IDs in the archive are small positive numbers; ignore anything that cannot be one
	if dataID == 0 || dataID >= 1<<31 {
		return false
	}

	timeout := p.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ok, err := p.Ensure(ctx, int64(dataID))
	if err != nil {
		p.logf("pool: on-demand lookup of level %d failed: %v", dataID, err)
		return false
	}

	return ok
}
