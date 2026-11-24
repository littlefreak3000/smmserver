// Package selection decides WHICH levels a course list contains. The protocol handlers only render the answer.
//
// There are two sources of levels:
//
//   - the whole-archive index (package index), when the server has one: every archived level is a candidate, a random
//     pick is one lookup, and the chosen levels are listed as "stubs" and downloaded just before they are needed;
//   - the catalog, which holds the levels that were downloaded earlier (the "pool").
//
// Randomness is a plain random draw, unless a challenge seed is active for the requesting player. A seed turns the draw into a
// pure function of (seed, which list was asked for): the same seed gives the same levels to everyone and on every server that
// has the same level archive (the index fingerprint is part of the challenge code). See package challenge.
package selection

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/difficulty"
	"github.com/PretendoNetwork/super-mario-maker/index"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

type Kind int

const (
	// Random is a random (or seeded) set of levels in a clear-rate range.
	Random Kind = iota
	// Latest lists the newest levels first.
	Latest
	// Stars lists the most-starred levels first.
	Stars
)

// Query describes one list request.
type Query struct {
	Kind     Kind
	App      uint32 // Random: the custom ranking the range refers to
	Min, Max int64  // Random: the score range, inclusive (for the difficulty ranking: clear rate in percent)
	Offset   int
	Length   int
	NNID     string // who is asking ("" if unknown); picks a per-player seed
}

// Seeds says which seed is active for a player ("" = none). challenge.Store implements it.
type Seeds interface {
	Effective(nnid string) string
}

type Selector struct {
	DB            *sql.DB
	Index         *index.Index // nil: pool mode
	Seeds         Seeds        // nil: never seeded
	DifficultyApp uint32       // the ranking that carries the clear rate in percent (index mode answers only this one)
	Ceiling       int          // percent: difficulty lists never offer a level above it (0, or 100 and up, means no ceiling)

	Stub     func(upstream.Level) error // lists a level in the catalog before its file is downloaded
	Prefetch func(ids ...int64)         // queues background downloads, in order
	Dead     func(id int64) bool        // levels known to have no usable capture
	Logf     func(format string, args ...any)

	// NeedMore is told when a random list (no index, no seed) is drawn from a clear-rate range [min, max] (percent)
	// that has only `unseen` levels left that the player has not opened recently, so the catalog can fetch more of them
	// in the background. It must not block.
	NeedMore func(min, max int64, unseen int)

	// HistoryLevels is how many levels the server remembers per player (0 = DefaultHistory, negative = remember nothing).
	HistoryLevels int
}

// DefaultHistory is how many of a player's most recently opened levels are remembered. Each takes a few dozen bytes
// in the catalog, so a player's history stays around 30 KB however long they play.
const DefaultHistory = 1000

func (s *Selector) historyLevels() int {
	if s.HistoryLevels == 0 {
		return DefaultHistory
	}

	return s.HistoryLevels
}

// MarkPlayed remembers that a player opened a level, so their next random lists offer other levels first. The history is
// a rolling window of the player's last historyLevels() levels in the catalog file, so it survives restarts and the
// rows of older levels are deleted as new ones come in. Only the catalog picker (no index, no seed) consults it: with
// the whole archive a repeat is already unlikely, and a seeded list must not change. Ids that are not levels (profiles,
// the event list) are ignored.
func (s *Selector) MarkPlayed(nnid string, id int64) {
	limit := s.historyLevels()
	if s.DB == nil || limit < 0 {
		return
	}

	var one int

	err := s.DB.QueryRow(`SELECT 1 FROM datastore.objects WHERE data_id = $1 AND data_type > 2 AND data_type < 50`, id).Scan(&one)
	if err != nil {
		return
	}

	tx, err := s.DB.Begin()
	if err != nil {
		s.logf("selection: cannot save the play history: %v", err)

		return
	}

	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`INSERT INTO datastore.play_history (nnid, data_id, played_at) VALUES ($1, $2, $3)
		ON CONFLICT (nnid, data_id) DO UPDATE SET played_at = excluded.played_at`, nnid, id, time.Now().UnixMilli())
	if err == nil {
		// * the rolling window: everything after the newest `limit` entries of this player goes
		_, err = tx.Exec(`DELETE FROM datastore.play_history WHERE nnid = $1 AND data_id IN (
			SELECT data_id FROM datastore.play_history WHERE nnid = $1 ORDER BY played_at DESC, data_id DESC LIMIT -1 OFFSET $2)`, nnid, limit)
	}

	if err == nil {
		err = tx.Commit()
	}

	if err != nil {
		s.logf("selection: cannot save the play history: %v", err)
	}
}

// recentlyPlayed returns the levels in a player's history with the time (unix milliseconds) each was last opened.
func (s *Selector) recentlyPlayed(nnid string) map[int64]int64 {
	played := map[int64]int64{}

	if s.DB == nil || s.historyLevels() < 0 {
		return played
	}

	rows, err := s.DB.Query(`SELECT data_id, played_at FROM datastore.play_history WHERE nnid = $1`, nnid)
	if err != nil {
		s.logf("selection: cannot read the play history: %v", err)

		return played
	}

	defer rows.Close()

	for rows.Next() {
		var id, at int64
		if rows.Scan(&id, &at) == nil {
			played[id] = at
		}
	}

	return played
}

func (s *Selector) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Seed returns the seed that applies to the player ("" if none).
func (s *Selector) Seed(nnid string) string {
	if s.Seeds == nil {
		return ""
	}

	return s.Seeds.Effective(nnid)
}

// FailureToClear converts a failure-rate range, which is how the Course World difficulty filter is expressed
// (Easy 0-34, Super Expert 96-100), into the clear-rate range the index is sorted by.
func FailureToClear(minFailure, maxFailure int64) (int64, int64) {
	return 100 - maxFailure, 100 - minFailure
}

// IDs returns the IDs of the levels to list, in order. handled is false when this package has nothing to say and the
// caller should use its own database query (an unseeded request in pool mode, an empty range, a ranking the index does
// not cover).
func (s *Selector) IDs(q Query) (ids []int64, handled bool, err error) {
	// * a ceiling below 100 cuts every difficulty request off at that clear rate: a range wholly above it gets the easiest
	// * levels that are allowed
	if s.ceilingOn() && q.Kind == Random && q.App == s.DifficultyApp {
		q.Max = min(q.Max, int64(s.Ceiling))
		q.Min = min(q.Min, q.Max)
	}

	seed := s.Seed(q.NNID)

	switch q.Kind {
	case Random:
		switch {
		case s.Index != nil && q.App == s.DifficultyApp:
			return s.indexRandom(q, seed)
		case seed != "":
			return s.catalogSeeded(q, seed, false)
		case q.App == s.DifficultyApp && (q.Min > 0 || q.Max < 100):
			// * no index, no seed: the plain random query ignores the difficulty range completely (the first game
			// * test handed out hard levels for "Easy"), so pick at random from the levels in the range ourselves,
			// * levels that were not played recently first
			return s.catalogSeeded(q, randomSeed(), true)
		}
	case Latest, Stars:
		if s.Index != nil && q.Length > 0 {
			return s.indexList(q)
		}
	}

	return nil, false, nil
}

// context is what identifies "the same list": the kind of list and its range, never the offset or length, so a longer
// request starts with exactly the levels of a shorter one.
func (q Query) context(lo, hi int64) string {
	return fmt.Sprintf("random|%d|%d|%d", q.App, lo, hi)
}

func (s *Selector) indexRandom(q Query, seed string) ([]int64, bool, error) {
	lo, hi := min(max(q.Min, 0), 100), min(max(q.Max, 0), 100)
	n := q.Offset + q.Length

	var src index.Source = index.NewRandom()
	if seed != "" {
		src = index.NewSeeded(seed, q.context(lo, hi))
	}

	levels, err := s.Index.Sample(src, int(lo), int(hi), n, s.skip)
	if err != nil {
		return nil, false, err
	}

	if q.Offset >= len(levels) {
		return nil, false, nil // empty range: let the caller widen it
	}

	return s.register(levels[q.Offset:]), true, nil
}

func (s *Selector) indexList(q Query) ([]int64, bool, error) {
	var (
		levels []index.Level
		err    error
	)

	if q.Kind == Latest {
		levels, err = s.Index.Newest(q.Offset, q.Length)
	} else {
		levels, err = s.Index.TopStars(q.Offset, q.Length)
	}

	if err != nil {
		return nil, false, err
	}

	// * levels without a capture are not offered; a page may therefore be a little short
	kept := levels[:0]

	for _, l := range levels {
		if !s.skip(l.ID) {
			kept = append(kept, l)
		}
	}

	if len(kept) == 0 {
		return nil, false, nil
	}

	return s.register(kept), true, nil
}

func (s *Selector) ceilingOn() bool { return s.Ceiling > 0 && s.Ceiling < 100 }

func (s *Selector) skip(id int64) bool { return s.Dead != nil && s.Dead(id) }

// register lists the chosen levels in the catalog and queues their downloads.
func (s *Selector) register(levels []index.Level) []int64 {
	ids := make([]int64, 0, len(levels))

	for _, l := range levels {
		if s.Stub != nil {
			if err := s.Stub(l.Upstream()); err != nil {
				s.logf("selection: cannot list level %d: %v", l.ID, err)
				continue
			}
		}

		ids = append(ids, l.ID)
	}

	if s.Prefetch != nil && len(ids) > 0 {
		s.Prefetch(ids...)
	}

	return ids
}

// randomSeed is a fresh seed for a request that has none: the same picker, a different answer every time.
func randomSeed() string {
	var b [12]byte

	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}

	return hex.EncodeToString(b[:])
}

// catalogSeeded picks from the levels the catalog holds. Every level gets a pseudo-random weight from the seed and
// the list; the levels with the smallest weights win ("rendezvous hashing"). Two servers whose catalogs hold the same
// levels give the same list; levels that are added later can move into it, which is why the index mode is the one meant
// for challenges between servers.
//
// With fresh set (an unseeded list) the levels that were played recently go to the back, so a round does not hand out
// the same levels again while others exist, and NeedMore hears when the unplayed ones run low.
//
// In a catalog with a difficulty prior, a difficulty range is weighted by confidence, as in index.Sample: the levels whose
// score lies within difficulty.Margin points of the range are considered, and each is accepted with the probability that its
// true clear rate lies in the range, decided by a second number from the same hash so the list stays a pure function of the
// seed and the catalog. Levels that are not from the archive (no counts) are in or out by their ranking value.
func (s *Selector) catalogSeeded(q Query, seed string, fresh bool) ([]int64, bool, error) {
	soft := difficulty.Current.Strength > 0 && q.App == s.DifficultyApp && !(q.Min <= 0 && q.Max >= 100)
	qLo, qHi := q.Min, q.Max

	if soft {
		lo, hi := difficulty.Window(int(q.Min), int(q.Max))
		qLo, qHi = int64(lo), int64(hi)
	}

	// * the neighbouring-range window must not reach above the ceiling either
	if s.ceilingOn() && q.App == s.DifficultyApp {
		qHi = min(qHi, int64(s.Ceiling))
	}

	rows, err := s.DB.Query(`
		SELECT o.data_id, r.value, ls.clear_rate, ls.attempts FROM datastore.objects o
		JOIN datastore.object_custom_rankings r ON r.data_id = o.data_id AND r.application_id = $1 AND r.value BETWEEN $2 AND $3
		LEFT JOIN datastore.level_sources ls ON ls.data_id = o.data_id AND ls.attempts > 0
		WHERE o.upload_completed = TRUE AND o.deleted = FALSE AND o.under_review = FALSE AND o.data_type > 2 AND o.data_type < 50`, q.App, qLo, qHi)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	type weighted struct {
		id     int64
		w      uint64
		played bool
		at     int64 // when the player last opened it (0 = not in their history)
	}

	var (
		all     []weighted
		history map[int64]int64
	)

	if fresh {
		history = s.recentlyPlayed(q.NNID)
	}

	norm, ctx := index.NormalizeSeed(seed), q.context(q.Min, q.Max)

	for rows.Next() {
		var (
			id       int64
			value    int64
			rate     sql.NullFloat64
			attempts sql.NullInt64
		)

		if err := rows.Scan(&id, &value, &rate, &attempts); err != nil {
			return nil, false, err
		}

		if s.skip(id) {
			continue
		}

		if soft {
			var confidence float64

			switch {
			case rate.Valid && attempts.Valid:
				confidence = difficulty.Current.InRange(rate.Float64, float64(attempts.Int64), int(q.Min), int(q.Max))
			case value >= q.Min && value <= q.Max:
				confidence = 1
			}

			a := sha256.Sum256([]byte(fmt.Sprintf("smm-accept-v%d\x00%s\x00%s\x00%d", index.AlgorithmVersion, norm, ctx, id)))
			if float64(binary.BigEndian.Uint64(a[:8]))/(1<<64) >= confidence {
				continue
			}
		}

		h := sha256.Sum256([]byte(fmt.Sprintf("smm-pick-v%d\x00%s\x00%s\x00%d", index.AlgorithmVersion, norm, ctx, id)))
		at, played := history[id]
		all = append(all, weighted{id, binary.BigEndian.Uint64(h[:8]), played, at})
	}

	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	unseen := 0

	for _, l := range all {
		if !l.played {
			unseen++
		}
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].played != all[j].played {
			return !all[i].played
		}

		// * when only played levels are left, the ones opened longest ago come first
		if all[i].played && all[i].at != all[j].at {
			return all[i].at < all[j].at
		}

		if all[i].w != all[j].w {
			return all[i].w < all[j].w
		}

		return all[i].id < all[j].id
	})

	if fresh && s.NeedMore != nil && unseen < 2*q.Length {
		s.NeedMore(q.Min, q.Max, unseen)
	}

	if q.Offset >= len(all) {
		return nil, false, nil
	}

	end := min(q.Offset+q.Length, len(all))
	ids := make([]int64, 0, end-q.Offset)

	for _, w := range all[q.Offset:end] {
		ids = append(ids, w.id)
	}

	return ids, true, nil
}
