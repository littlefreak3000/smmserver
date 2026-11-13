package pool_test

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/pool"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// flaky fails one RandomLevel call (a network problem), then behaves like the fake.
type flaky struct {
	*fake
	failAt int
	calls  int
}

func (f *flaky) RandomLevel(ctx context.Context, n int) (*upstream.Level, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, errors.New("connection reset")
	}

	return f.fake.RandomLevel(ctx, n)
}

func levelIDs(t *testing.T, db *sql.DB) []int64 {
	t.Helper()

	rows, err := db.Query(`SELECT data_id FROM datastore.level_sources WHERE fetched IS NOT NULL ORDER BY data_id`)
	if err != nil {
		t.Fatal(err)
	}

	defer rows.Close()

	var ids []int64

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	return ids
}

func build(t *testing.T, src pool.Source, seed string, levels int) ([]int64, *pool.CacheManifest) {
	t.Helper()

	f, ok := src.(*fake)
	if !ok {
		f = src.(*flaky).fake
	}

	p, db := setup(t, f)
	p.Src = src

	m, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: seed, Levels: levels, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	return levelIDs(t, db), m
}

func TestSeededBuildsGiveTheSameLevelsOnEveryMachine(t *testing.T) {
	a, ma := build(t, newFake(t), "contest one", 8)
	b, mb := build(t, newFake(t), "Contest   ONE", 8)

	if len(a) != 8 || !reflect.DeepEqual(a, b) || ma.Fingerprint != mb.Fingerprint || ma.NextDraw != mb.NextDraw {
		t.Fatalf("same seed, different caches:\n%v (%s, draw %d)\n%v (%s, draw %d)", a, ma.Fingerprint, ma.NextDraw, b, mb.Fingerprint, mb.NextDraw)
	}

	c, mc := build(t, newFake(t), "contest two", 8)
	if reflect.DeepEqual(a, c) || ma.Fingerprint == mc.Fingerprint {
		t.Fatalf("different seeds gave the same levels: %v", a)
	}

	if !ma.Locked || ma.PlaySeed != "contest one" || len(ma.PerBand) != 4 {
		t.Fatalf("manifest %+v", ma)
	}
}

func TestANetworkProblemDoesNotChangeTheResult(t *testing.T) {
	clean, mc := build(t, newFake(t), "steady", 8)
	bumpy, mb := build(t, &flaky{fake: newFake(t), failAt: 5}, "steady", 8)

	if !reflect.DeepEqual(clean, bumpy) || mc.Fingerprint != mb.Fingerprint {
		t.Fatalf("a retry changed the result:\n%v\n%v", clean, bumpy)
	}
}

func TestABuildCanBeResumedAndExtended(t *testing.T) {
	f := newFake(t)

	direct, md := build(t, f, "growing", 12)

	// * the same seed in two steps (8, then 12) ends with the same cache as one step of 12
	p, db := setup(t, newFake(t))
	dir := t.TempDir()

	if _, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: "growing", Levels: 8, Dir: dir}); err != nil {
		t.Fatal(err)
	}

	step, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: "Growing", Levels: 12, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if got := levelIDs(t, db); !reflect.DeepEqual(got, direct) || step.Fingerprint != md.Fingerprint {
		t.Fatalf("resumed build differs:\n%v\n%v", got, direct)
	}

	// * another seed, or a random build, cannot be mixed into the same folder
	if _, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: "other", Levels: 12, Dir: dir}); err == nil {
		t.Fatal("a different seed was accepted in a folder that holds another seed's cache")
	}
}

func TestASeededBuildNeedsAnEmptyFolder(t *testing.T) {
	p, _ := setup(t, newFake(t))

	if _, err := p.Fill(context.Background()); err != nil { // the pool puts levels in the catalog
		t.Fatal(err)
	}

	if _, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: "x", Levels: 4, Dir: t.TempDir()}); err == nil {
		t.Fatal("a seeded build was started in a catalog that already holds levels")
	}
}

func TestExportAndImportCarryTheLevelsButNotThePlayers(t *testing.T) {
	p, db, courses := evictEnv(t, 12)
	dataDir := filepath.Dir(courses)

	// * a local player: an account, a profile, a record, a seed
	exec(t, db, `INSERT INTO datastore.accounts (nnid, pid, created) VALUES ('me', 700, $1)`, time.Now())
	exec(t, db, `INSERT INTO datastore.objects (upload_completed, data_id, owner, size, name, data_type, meta_binary, permission, permission_recipients,
			delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id, extra_data, creation_date, update_date)
		VALUES (1, 9000, 700, 0, 'me', 1, x'00', 0, '[]', 3, '[]', 256, 0, 0, '["1"]', 0, '[]', $1, $1)`, time.Now())
	exec(t, db, `INSERT INTO datastore.course_records (data_id, slot, first_pid, best_pid, best_score, creation_date, update_date) VALUES (3, 0, 700, 700, 5000, $1, $1)`, time.Now())
	exec(t, db, `INSERT INTO datastore.challenge_seeds (scope, seed, updated) VALUES ('*', 'secret', $1)`, time.Now())

	fp, n, err := pool.CacheFingerprint(db)
	if err != nil || n != 12 {
		t.Fatalf("fingerprint %q, %d levels, %v", fp, n, err)
	}

	m := &pool.CacheManifest{Format: 1, Seed: "contest", Locked: true, PlaySeed: "contest", Wanted: 12, Levels: n, Fingerprint: fp}
	if err := m.Write(dataDir); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(t.TempDir(), "cache.zip")

	if _, err := pool.ExportCache(db, dataDir, zipPath); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()

	got, err := pool.ImportCache(zipPath, target, false)
	if err != nil {
		t.Fatal(err)
	}

	if got.Fingerprint != fp || got.Levels != 12 || got.Seed != "contest" || !got.Locked {
		t.Fatalf("imported manifest %+v", got)
	}

	for id := 1; id <= 12; id++ {
		if !exists(filepath.Join(target, "courses"), id) {
			t.Fatalf("level file %d was not unpacked", id)
		}
	}

	imported, err := database.Open(filepath.Join(target, "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	defer imported.Close()

	var accounts, seeds, profiles, records, levels int

	imported.QueryRow(`SELECT COUNT(*) FROM datastore.accounts`).Scan(&accounts)
	imported.QueryRow(`SELECT COUNT(*) FROM datastore.challenge_seeds`).Scan(&seeds)
	imported.QueryRow(`SELECT COUNT(*) FROM datastore.objects WHERE owner = 700`).Scan(&profiles)
	imported.QueryRow(`SELECT COUNT(*) FROM datastore.course_records`).Scan(&records)
	imported.QueryRow(`SELECT COUNT(*) FROM datastore.objects WHERE data_type = 3`).Scan(&levels)

	if accounts+seeds+profiles+records != 0 || levels != 12 {
		t.Fatalf("accounts %d, seeds %d, local objects %d, records %d (want 0 each); levels %d (want 12)", accounts, seeds, profiles, records, levels)
	}

	// * the live catalog is untouched
	var live int
	db.QueryRow(`SELECT COUNT(*) FROM datastore.accounts`).Scan(&live)

	if live != 1 {
		t.Fatalf("the export changed the live catalog (%d accounts)", live)
	}

	// * a folder that already has a catalog is not overwritten without --force
	if _, err := pool.ImportCache(zipPath, target, false); err == nil {
		t.Fatal("an existing catalog was overwritten")
	}

	if _, err := pool.ImportCache(zipPath, target, true); err != nil {
		t.Fatalf("--force: %v", err)
	}

	_ = p
}

func TestImportRefusesFilesThatAreNotACache(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "evil.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	zw := zip.NewWriter(f)
	w, _ := zw.Create("../../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	f.Close()

	if _, err := pool.ImportCache(zipPath, t.TempDir(), false); err == nil {
		t.Fatal("a zip with a path outside the cache layout was unpacked")
	}
}

// interrupter cancels the build after some draws, as pressing Ctrl+C would.
type interrupter struct {
	*fake
	after  int
	calls  int
	cancel context.CancelFunc
}

func (f *interrupter) RandomLevel(ctx context.Context, n int) (*upstream.Level, error) {
	f.calls++
	if f.calls == f.after {
		f.cancel()
	}

	return f.fake.RandomLevel(ctx, n)
}

func TestAnInterruptedBuildCarriesOnToTheSameResult(t *testing.T) {
	direct, md := build(t, newFake(t), "interrupt me", 12)

	p, db := setup(t, newFake(t))
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	p.Src = &interrupter{fake: newFake(t), after: 20, cancel: cancel}

	if _, err := p.BuildCache(ctx, pool.BuildOptions{Seed: "interrupt me", Levels: 12, Dir: dir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("the build should have been interrupted, got %v", err)
	}

	if half := levelIDs(t, db); len(half) == 0 || len(half) >= 12 {
		t.Fatalf("an interrupted build left %d levels", len(half))
	}

	p.Src = newFake(t)

	m, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: "interrupt me", Levels: 12, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if got := levelIDs(t, db); !reflect.DeepEqual(got, direct) || m.Fingerprint != md.Fingerprint {
		t.Fatalf("a resumed build differs from an uninterrupted one:\n%v\n%v", got, direct)
	}
}
