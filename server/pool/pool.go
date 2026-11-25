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
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/difficulty"
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

// ParseBands reads the top of each band, in percent clear rate, for example "10,30,60,85" for the bands 0-10%, 10-30%, 30-60%
// and 60-85%. The numbers rise and lie between 1 and 100; the last one is the top of the highest band, so a last number below
// 100 also keeps levels that are easier than that out of the pool (the same as SMM_DIFFICULTY_CEILING). A list of three
// numbers or fewer that does not end in 100 gets the top band up to 100 added ("5,20,50" is 0-5%, 5-20%, 20-50%, 50-100%).
// At most 9 bands. The pool keeps the same number of levels in every band.
func ParseBands(s string) ([]Band, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("no band edges given")
	}

	var tops []int

	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > 100 {
			return nil, fmt.Errorf("%q is not a whole number from 1 to 100", strings.TrimSpace(part))
		}

		if len(tops) > 0 && n <= tops[len(tops)-1] {
			return nil, fmt.Errorf("the numbers must rise, but %d follows %d", n, tops[len(tops)-1])
		}

		tops = append(tops, n)
	}

	if len(tops) <= 3 && tops[len(tops)-1] < 100 {
		tops = append(tops, 100)
	}

	if len(tops) > 9 {
		return nil, errors.New("at most 9 bands are allowed")
	}

	bands := make([]Band, 0, len(tops))
	lo := 0

	for i, t := range tops {
		max := float64(t) / 100
		if i == len(tops)-1 {
			// * the top band includes its last number: Max is a little above it so a score of exactly that belongs to it
			max += 0.0000001
		}

		bands = append(bands, Band{fmt.Sprintf("%d-%d%%", lo, t), float64(lo) / 100, max})
		lo = t
	}

	return bands, nil
}

// ChallengeRanges turns the bands into what the four difficulty buttons of the game ask for, as clear-rate ranges in percent
// (both ends inclusive), in the order Super Expert, Expert, Normal, Easy. The lowest bands are Super Expert and the highest
// are Easy. With exactly 4 bands each button is one band; with more, the bands are shared out as evenly as possible (the cut
// between two buttons is floor(k * bands / 4) for the k-th, so with 6 bands Expert and Easy get two bands each). There must be
// at least 4 bands, so that every button has a band of its own.
func ChallengeRanges(bands []Band) ([4][2]int64, error) {
	var out [4][2]int64

	n := len(bands)
	if n < 4 {
		return out, fmt.Errorf("the four difficulty buttons need at least 4 bands (3 edges), but there are %d", n)
	}

	for k := 0; k < 4; k++ {
		first, last := k*n/4, (k+1)*n/4-1

		lo := int64(math.Round(bands[first].Min * 100))
		edge := bands[last].Max * 100
		hi := int64(math.Round(edge))

		// * a band ends where the next one starts, so the edge belongs to the upper band; only the top band includes its edge
		// * (its Max is a little above a whole number)
		if edge-float64(hi) < 0.000001 {
			hi--
		}

		out[k] = [2]int64{lo, hi}
	}

	return out, nil
}

// WithCeiling returns the bands with the top one cut off at pct percent: levels whose difficulty score is above it belong to
// no band, so the pool never downloads them. 100 (or more) leaves the bands as they are. The ceiling is inclusive and has to
// lie above the bottom of the top band.
func WithCeiling(bands []Band, pct int) ([]Band, error) {
	out := append([]Band(nil), bands...)
	if pct >= 100 || len(out) == 0 {
		return out, nil
	}

	last := len(out) - 1
	if float64(pct)/100 <= out[last].Min {
		return nil, fmt.Errorf("a ceiling of %d%% is not above the start of the top band (%.0f%%)", pct, out[last].Min*100)
	}

	// * a little above pct so a score of exactly pct is still inside
	out[last] = Band{fmt.Sprintf("%.0f-%d%%", out[last].Min*100, pct), out[last].Min, float64(pct)/100 + 0.0000001}

	return out, nil
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

// bandOfLevel is the band a level belongs to by its difficulty score (see package difficulty), which for a level with few
// attempts is not its raw clear rate.
func (p *Pool) bandOfLevel(lvl *upstream.Level) int {
	return p.bandOf(difficulty.Current.Score(lvl.ClearRate, float64(lvl.TotalAttempts)))
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
		score := difficulty.Current.SQL("clear_rate", "attempts") // the band is decided by the difficulty score, not the raw rate

		if err := p.DB.QueryRow(`SELECT COUNT(*) FROM datastore.level_sources WHERE attempts > 0 AND `+score+` >= $1 AND `+score+` < $2`,
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
	space, low := p.RandomSpace, 0
	if space <= 0 {
		// * the real archive: skip the first part of the index, which holds no levels (see upstream.RandomLow)
		space, low = upstream.RandomSpace, upstream.RandomLow
	}

	if p.Rand != nil {
		return low + p.Rand(space-low)
	}

	return low + rand.IntN(space-low)
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
