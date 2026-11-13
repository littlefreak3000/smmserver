package index

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Record is one level as the metadata dump or the metadata API describes it.
type Record struct {
	ID       int64
	Name     string
	Creator  int64
	Clears   int64
	Attempts int64
	Stars    int64
	Upload   time.Time
	URL      string // the original (signed) download URL, which is what the Wayback Machine captured
}

// SkipError reports a record that was not added, and why. Builder.Add returns it; building can carry on.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "skipped: " + e.Reason }

// Stats describe a finished build.
type Stats struct {
	Added             int // records accepted (a repeated ID replaces the earlier record)
	SkippedNoAttempts int // nobody played them: no win rate to sort by
	SkippedBad        int // no ID, or a URL that does not have the expected shape or belongs to another level
	Total             int64
	Counts            [101]int64
	Fingerprint       string
}

// Builder writes a new index. It writes to temporary files and only replaces the target on Finish.
type Builder struct {
	path, tmp, stage string
	db               *sql.DB
	ins              *sql.Stmt
	prefixes         map[string]int
	keypairs         map[string]int
	stats            Stats
	done             bool
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func removeAll(paths ...string) {
	for _, p := range paths {
		os.Remove(p)
		os.Remove(p + "-journal")
		os.Remove(p + "-wal")
		os.Remove(p + "-shm")
	}
}

// Create starts building an index that will be written to path.
func Create(path string) (*Builder, error) {
	b := &Builder{path: path, tmp: path + ".building", stage: path + ".stage", prefixes: map[string]int{}, keypairs: map[string]int{}}
	removeAll(b.tmp, b.stage)

	db, err := sql.Open("sqlite", b.tmp+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1) // ATTACH and BEGIN belong to a connection; keep to one

	b.db = db

	for _, stmt := range []string{
		`ATTACH DATABASE ` + quote(b.stage) + ` AS stage`,
		`PRAGMA stage.journal_mode=OFF`,
		`PRAGMA stage.synchronous=OFF`,
		// * the staging table is a separate file, so the final file never holds this (about 2x bigger) copy of the data
		`CREATE TABLE stage.rows (
			id INTEGER PRIMARY KEY, bucket INTEGER NOT NULL, name TEXT NOT NULL, creator INTEGER NOT NULL,
			clears INTEGER NOT NULL, attempts INTEGER NOT NULL, stars INTEGER NOT NULL, upload INTEGER NOT NULL,
			expires INTEGER NOT NULL, rev INTEGER NOT NULL, sig BLOB NOT NULL, sigraw INTEGER NOT NULL, pfx INTEGER NOT NULL, kp INTEGER NOT NULL)`,
		`BEGIN`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			b.Abort()
			return nil, fmt.Errorf("%s: %w", firstWord(stmt), err)
		}
	}

	b.ins, err = db.Prepare(`INSERT OR REPLACE INTO stage.rows VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		b.Abort()
		return nil, err
	}

	return b, nil
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " \n\t"); i > 0 {
		return s[:i]
	}

	return s
}

func intern(m map[string]int, s string) int {
	if id, ok := m[s]; ok {
		return id
	}

	m[s] = len(m)

	return m[s]
}

// bucketOf is the clear rate in whole percent, from the counts (any stored rate in a dump is ignored).
func bucketOf(clears, attempts int64) int {
	if attempts <= 0 {
		return 0
	}

	b := int(math.Round(100 * float64(clears) / float64(attempts)))

	return max(0, min(100, b))
}

// Add stages one record. It returns a *SkipError for records that cannot be indexed and a plain error for I/O problems.
func (b *Builder) Add(r Record) error {
	if b.done {
		return errors.New("index: builder already finished")
	}

	if r.ID <= 0 {
		b.stats.SkippedBad++
		return &SkipError{"no level ID"}
	}

	if r.Attempts <= 0 {
		b.stats.SkippedNoAttempts++
		return &SkipError{"no attempts"}
	}

	c, err := parseCapture(r.URL)
	if err != nil {
		b.stats.SkippedBad++
		return &SkipError{err.Error()}
	}

	if c.ID != r.ID {
		b.stats.SkippedBad++
		return &SkipError{fmt.Sprintf("URL is for level %d, not %d", c.ID, r.ID)}
	}

	name := strings.ToValidUTF8(r.Name, "")
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}

	sigRaw := 0
	if c.SigRaw {
		sigRaw = 1
	}

	if _, err := b.ins.Exec(r.ID, bucketOf(r.Clears, r.Attempts), name, r.Creator, r.Clears, r.Attempts, r.Stars, r.Upload.Unix(),
		c.Expires, c.Rev, c.Sig, sigRaw, intern(b.prefixes, c.Prefix), intern(b.keypairs, c.KeyPair)); err != nil {
		return err
	}

	b.stats.Added++

	return nil
}

// Abort throws the partial build away.
func (b *Builder) Abort() {
	if b.done {
		return
	}

	b.done = true

	if b.ins != nil {
		b.ins.Close()
	}

	if b.db != nil {
		b.db.Close()
	}

	removeAll(b.tmp, b.stage)
}

// Finish assigns each level its dense position inside its clear-rate bucket, builds the lookup indexes and moves the
// finished file into place.
func (b *Builder) Finish() (Stats, error) {
	if b.done {
		return b.stats, errors.New("index: builder already finished")
	}

	defer b.Abort() // closes handles and removes temporary files (the finished file has been renamed away by then)

	exec := func(q string, args ...any) error {
		if _, err := b.db.Exec(q, args...); err != nil {
			return fmt.Errorf("%s: %w", firstWord(q), err)
		}

		return nil
	}

	for _, q := range []string{
		`COMMIT`,
		`CREATE TABLE levels (
			bucket INTEGER NOT NULL, seq INTEGER NOT NULL, id INTEGER NOT NULL, name TEXT NOT NULL, creator INTEGER NOT NULL,
			clears INTEGER NOT NULL, attempts INTEGER NOT NULL, stars INTEGER NOT NULL, upload INTEGER NOT NULL,
			expires INTEGER NOT NULL, rev INTEGER NOT NULL, sig BLOB NOT NULL, sigraw INTEGER NOT NULL, pfx INTEGER NOT NULL, kp INTEGER NOT NULL,
			PRIMARY KEY (bucket, seq)) WITHOUT ROWID`,
		`INSERT INTO levels
			SELECT bucket, row_number() OVER (PARTITION BY bucket ORDER BY id) - 1, id, name, creator, clears, attempts, stars,
			       upload, expires, rev, sig, sigraw, pfx, kp
			FROM stage.rows ORDER BY bucket, id`,
		`CREATE UNIQUE INDEX levels_id ON levels(id)`,
		`CREATE INDEX levels_stars ON levels(stars DESC, id)`,
		`CREATE INDEX levels_upload ON levels(upload DESC, id)`,
		`CREATE TABLE bucket_counts (bucket INTEGER PRIMARY KEY, n INTEGER NOT NULL)`,
		`INSERT INTO bucket_counts SELECT bucket, COUNT(*) FROM levels GROUP BY bucket`,
		`CREATE TABLE prefixes (id INTEGER PRIMARY KEY, prefix TEXT NOT NULL)`,
		`CREATE TABLE keypairs (id INTEGER PRIMARY KEY, keypair TEXT NOT NULL)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if err := exec(q); err != nil {
			return b.stats, err
		}
	}

	for p, id := range b.prefixes {
		if err := exec(`INSERT INTO prefixes VALUES (?, ?)`, id, p); err != nil {
			return b.stats, err
		}
	}

	for k, id := range b.keypairs {
		if err := exec(`INSERT INTO keypairs VALUES (?, ?)`, id, k); err != nil {
			return b.stats, err
		}
	}

	rows, err := b.db.Query(`SELECT bucket, n FROM bucket_counts`)
	if err != nil {
		return b.stats, err
	}

	for rows.Next() {
		var bucket int

		var n int64
		if err := rows.Scan(&bucket, &n); err != nil {
			rows.Close()
			return b.stats, err
		}

		b.stats.Counts[bucket] = n
		b.stats.Total += n
	}

	rows.Close()

	var sumID int64
	if err := b.db.QueryRow(`SELECT COALESCE(SUM(id), 0) FROM levels`).Scan(&sumID); err != nil {
		return b.stats, err
	}

	b.stats.Fingerprint = fingerprint(b.stats.Total, sumID, b.stats.Counts)

	for k, v := range map[string]string{
		"schema": "1", "algorithm": fmt.Sprint(AlgorithmVersion), "levels": fmt.Sprint(b.stats.Total),
		"fingerprint": b.stats.Fingerprint, "built": time.Now().UTC().Format(time.RFC3339),
	} {
		if err := exec(`INSERT INTO meta VALUES (?, ?)`, k, v); err != nil {
			return b.stats, err
		}
	}

	if err := exec(`ANALYZE`); err != nil {
		return b.stats, err
	}

	// * close everything before renaming (Windows will not rename an open file)
	b.ins.Close()
	b.db.Close()
	b.ins, b.db = nil, nil

	os.Remove(b.stage)
	os.Remove(b.path)

	if err := os.Rename(b.tmp, b.path); err != nil {
		return b.stats, err
	}

	return b.stats, nil
}

// fingerprint identifies the set of levels in an index (not the order they were added in): two players with the same
// fingerprint get the same levels from the same seed.
func fingerprint(total, sumID int64, counts [101]int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("smm-index-v%d|n=%d|sumid=%d|counts=%v", AlgorithmVersion, total, sumID, counts)))

	return strings.ToUpper(hex.EncodeToString(sum[:4]))
}
