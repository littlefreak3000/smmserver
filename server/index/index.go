// Package index is a compact, read-only list of every archived level (about 385 bytes each), laid out so that the
// operations a Super Mario Maker server needs are fast at ten million levels:
//
//   - a random level in a clear-rate range is ONE primary-key lookup (every level has a dense position inside its
//     clear-rate bucket), about 30 microseconds, instead of a scan;
//   - the same seed always gives the same levels (see rng.go), so players can run the same challenge;
//   - newest, most-starred and by-ID lookups use ordinary indexes.
//
// The index only describes levels. Level files are fetched from the Wayback Machine when needed (package pool).
package index

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// Level is one row of the index.
type Level struct {
	ID       int64
	Name     string
	Creator  int64
	Clears   int64
	Attempts int64
	Stars    int64
	Upload   time.Time
	Bucket   int // clear rate in whole percent
	URL      string
}

// Upstream converts a level to the form the importer uses.
func (l Level) Upstream() upstream.Level {
	return upstream.Level{
		URL: l.URL, Name: l.Name, LevelID: l.ID, CreatorID: l.Creator, Clears: l.Clears, Failures: l.Attempts - l.Clears,
		TotalAttempts: l.Attempts, ClearRate: float64(l.Clears) / float64(l.Attempts), UploadTime: l.Upload, Stars: l.Stars,
	}
}

type Index struct {
	db          *sql.DB
	counts      [101]int64
	total       int64
	fingerprint string
	prefixes    map[int64]string
	keypairs    map[int64]string
}

const levelColumns = `bucket, id, name, creator, clears, attempts, stars, upload, expires, rev, sig, sigraw, pfx, kp`

// Open opens an index file for reading.
func Open(path string) (*Index, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path+"?_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}

	x := &Index{db: db, prefixes: map[int64]string{}, keypairs: map[int64]string{}}

	fail := func(err error) (*Index, error) {
		db.Close()
		return nil, fmt.Errorf("index %s: %w", path, err)
	}

	rows, err := db.Query(`SELECT bucket, n FROM bucket_counts`)
	if err != nil {
		return fail(err)
	}

	for rows.Next() {
		var b int

		var n int64
		if err := rows.Scan(&b, &n); err != nil {
			rows.Close()
			return fail(err)
		}

		if b < 0 || b > 100 {
			rows.Close()
			return fail(fmt.Errorf("bucket %d out of range", b))
		}

		x.counts[b], x.total = n, x.total+n
	}

	rows.Close()

	for _, t := range []struct {
		q string
		m map[int64]string
	}{{`SELECT id, prefix FROM prefixes`, x.prefixes}, {`SELECT id, keypair FROM keypairs`, x.keypairs}} {
		r, err := db.Query(t.q)
		if err != nil {
			return fail(err)
		}

		for r.Next() {
			var id int64

			var s string
			if err := r.Scan(&id, &s); err != nil {
				r.Close()
				return fail(err)
			}

			t.m[id] = s
		}

		r.Close()
	}

	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'fingerprint'`).Scan(&x.fingerprint); err != nil {
		return fail(err)
	}

	if x.total == 0 {
		return fail(errors.New("it contains no levels"))
	}

	return x, nil
}

func (x *Index) Close() error { return x.db.Close() }

// Total is the number of levels in the index.
func (x *Index) Total() int64 { return x.total }

// Counts returns how many levels each clear-rate percent (0..100) holds.
func (x *Index) Counts() [101]int64 { return x.counts }

// Fingerprint identifies the set of levels: the same fingerprint plus the same seed means the same levels.
func (x *Index) Fingerprint() string { return x.fingerprint }

type scanner interface{ Scan(dest ...any) error }

func (x *Index) scan(s scanner) (Level, error) {
	var (
		l            Level
		upload       int64
		c            capture
		sigRaw       int
		pfx, kp      int64
		expires, rev int64
		sig          []byte
	)

	if err := s.Scan(&l.Bucket, &l.ID, &l.Name, &l.Creator, &l.Clears, &l.Attempts, &l.Stars, &upload, &expires, &rev, &sig, &sigRaw, &pfx, &kp); err != nil {
		return Level{}, err
	}

	c = capture{Prefix: x.prefixes[pfx], ID: l.ID, Rev: rev, Expires: expires, Sig: sig, SigRaw: sigRaw == 1, KeyPair: x.keypairs[kp]}
	l.Upload = time.Unix(upload, 0).UTC()
	l.URL = c.URL()

	return l, nil
}

// ByID returns the level with that ID, or (nil, nil) if the index does not have it.
func (x *Index) ByID(id int64) (*Level, error) {
	l, err := x.scan(x.db.QueryRow(`SELECT `+levelColumns+` FROM levels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &l, nil
}

func (x *Index) list(order string, offset, n int) ([]Level, error) {
	if n <= 0 {
		return nil, nil
	}

	rows, err := x.db.Query(`SELECT `+levelColumns+` FROM levels ORDER BY `+order+` LIMIT ? OFFSET ?`, n, max(offset, 0))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Level

	for rows.Next() {
		l, err := x.scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, l)
	}

	return out, rows.Err()
}

// Newest returns levels by upload time, newest first.
func (x *Index) Newest(offset, n int) ([]Level, error) { return x.list("upload DESC, id", offset, n) }

// TopStars returns levels by star count, most first.
func (x *Index) TopStars(offset, n int) ([]Level, error) { return x.list("stars DESC, id", offset, n) }

// Sample draws up to n distinct levels whose clear rate is between lo and hi percent (inclusive), uniformly over all
// such levels. skip may exclude levels (for example ones known to have no capture).
//
// It uses one algorithm for every n, so with the same Source a longer request starts with exactly the levels of a shorter
// one: asking for 32 and then for 100 gives the same first 32. If the range holds fewer than n levels it returns all of them.
func (x *Index) Sample(src Source, lo, hi, n int, skip func(id int64) bool) ([]Level, error) {
	lo, hi = max(lo, 0), min(hi, 100)
	if lo > hi || n <= 0 {
		return nil, nil
	}

	var total int64
	for b := lo; b <= hi; b++ {
		total += x.counts[b]
	}

	if total == 0 {
		return nil, nil
	}

	out := make([]Level, 0, n)
	seen := make(map[int64]bool, n)

	for attempts := 0; len(out) < n && attempts < n*40+200; attempts++ {
		k := int64(src.IntN(int(total)))

		bucket := lo
		for ; bucket < hi && k >= x.counts[bucket]; bucket++ {
			k -= x.counts[bucket]
		}

		l, err := x.scan(x.db.QueryRow(`SELECT `+levelColumns+` FROM levels WHERE bucket = ? AND seq = ?`, bucket, k))
		if err != nil {
			return nil, fmt.Errorf("index: bucket %d position %d: %w", bucket, k, err)
		}

		if seen[l.ID] || (skip != nil && skip(l.ID)) {
			continue
		}

		seen[l.ID] = true
		out = append(out, l)
	}

	return out, nil
}
