package pool_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PretendoNetwork/nex-go/v2/types"
	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/pool"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// fake is an in-memory upstream: 200 levels (ids 1000-1199) whose clear rates sweep 0.5%..99.5% twice.
type fake struct {
	mu                                sync.Mutex
	levels                            []*upstream.Level
	good                              []byte
	garbageFor                        map[string]bool // URLs whose capture is not a level
	missingFor                        map[string]bool // URLs with no capture at all
	randomErr, levelErr               error
	randomCalls, levelCalls, binCalls int
}

func newFake(t *testing.T) *fake {
	t.Helper()

	good, err := os.ReadFile("../levelimport/testdata/sample.bin")
	if os.IsNotExist(err) {
		t.Skip("no sample level file: see ../levelimport/testdata/README.md")
	}

	if err != nil {
		t.Fatal(err)
	}

	f := &fake{good: good, garbageFor: map[string]bool{}, missingFor: map[string]bool{}}

	for i := 0; i < 200; i++ {
		rate := float64(i%100)/100 + 0.005
		f.levels = append(f.levels, &upstream.Level{
			URL: fmt.Sprintf("https://cdn.example/data/%011d-00001", 1000+i), Name: fmt.Sprintf("Level %d", 1000+i),
			LevelID: int64(1000 + i), CreatorID: 5000 + int64(i), Clears: int64(rate * 200), Failures: 200 - int64(rate*200),
			TotalAttempts: 200, ClearRate: rate, UploadTime: time.Date(2015, 10, 1, 0, 0, 0, 0, time.UTC), Stars: int64(i % 7),
		})
	}

	return f
}

func (f *fake) RandomLevel(ctx context.Context, n int) (*upstream.Level, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.randomCalls++

	if f.randomErr != nil {
		return nil, f.randomErr
	}

	if n%2 == 1 { // half of all indexes are empty, like the real API
		return nil, nil
	}

	l := *f.levels[(n/2)%len(f.levels)]

	return &l, nil
}

func (f *fake) Level(ctx context.Context, id int64) (*upstream.Level, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.levelCalls++

	if f.levelErr != nil {
		return nil, f.levelErr
	}

	for _, l := range f.levels {
		if l.LevelID == id {
			c := *l
			return &c, nil
		}
	}

	return nil, nil
}

func (f *fake) FetchBin(ctx context.Context, u string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.binCalls++

	switch {
	case f.missingFor[u]:
		return nil, upstream.ErrNotFound
	case f.garbageFor[u]:
		return []byte("<Error><Code>AccessDenied</Code></Error>"), nil
	}

	return f.good, nil
}

func setup(t *testing.T, f *fake) (*pool.Pool, *sql.DB) {
	t.Helper()

	globals.Logger = plogger.NewLogger()

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	database.DB = db
	t.Cleanup(func() { db.Close(); database.OnMissing = nil })

	n := 0

	return &pool.Pool{
		DB: db, Src: f, TargetPerBand: 3, MaxDraws: 3000, ErrorBackoff: time.Millisecond,
		Import: levelimport.Options{CoursesDir: filepath.Join(t.TempDir(), "courses")},
		Rand:   func(int) int { n++; return n }, // sweeps the fake's indexes in order
	}, db
}

func TestFillStocksEveryBandAndDownloadsOnlyWhatItKeeps(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	st, err := p.Fill(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	counts, _ := p.Counts()
	for i, c := range counts {
		if c != 3 {
			t.Fatalf("band %d holds %d, want 3 (all: %v)", i, c, counts)
		}
	}

	if st.Imported != 12 || f.binCalls != 12 {
		t.Fatalf("imported %d, downloaded %d files; only kept levels may be downloaded (stats %+v)", st.Imported, f.binCalls, st)
	}

	if st.BandFull == 0 || st.Empty == 0 {
		t.Fatalf("expected skipped draws for full bands and empty indexes: %+v", st)
	}

	// * the catalog really holds them, with files on disk
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM datastore.objects WHERE data_id BETWEEN 1000 AND 1199`).Scan(&n)

	files, _ := filepath.Glob(filepath.Join(p.Import.CoursesDir, "*.bin"))
	if n != 12 || len(files) != 12 {
		t.Fatalf("%d objects, %d files", n, len(files))
	}
}

func TestFillResumesAndAFullPoolCostsNothing(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)

	if _, err := p.Fill(context.Background()); err != nil {
		t.Fatal(err)
	}

	randomBefore, binBefore := f.randomCalls, f.binCalls
	st, err := p.Fill(context.Background())

	if err != nil || st.Draws != 0 || f.randomCalls != randomBefore || f.binCalls != binBefore {
		t.Fatalf("second fill did work: %+v err=%v (calls %d->%d, bins %d->%d)", st, err, randomBefore, f.randomCalls, binBefore, f.binCalls)
	}

	// * raising the target tops the pool up by exactly the difference, never re-importing known levels
	p.TargetPerBand = 5

	st, err = p.Fill(context.Background())
	if err != nil || st.Imported != 8 {
		t.Fatalf("top-up imported %d (err %v), want 8 (4 bands x 2)", st.Imported, err)
	}
}

func TestFillSkipsLevelsWithoutUsableCapturesWithoutLosingCount(t *testing.T) {
	f := newFake(t)

	// * make a third of the levels unusable in different ways
	for i, l := range f.levels {
		switch i % 3 {
		case 0:
			f.garbageFor[l.URL] = true
		case 1:
			if i%2 == 0 {
				f.missingFor[l.URL] = true
			}
		}
	}

	p, _ := setup(t, f)

	st, err := p.Fill(context.Background())
	if err != nil {
		t.Fatalf("%v (%+v)", err, st)
	}

	counts, _ := p.Counts()
	for i, c := range counts {
		if c != 3 {
			t.Fatalf("band %d holds %d (%v)", i, c, counts)
		}
	}

	if st.Invalid == 0 || st.FetchFailed == 0 {
		t.Fatalf("expected both kinds of failure to be counted: %+v", st)
	}

	// * nothing unusable was imported
	var bad int
	for url := range f.garbageFor {
		var n int
		p.DB.QueryRow(`SELECT COUNT(*) FROM datastore.level_sources WHERE url = $1`, url).Scan(&n)
		bad += n
	}

	if bad != 0 {
		t.Fatalf("%d garbage captures were imported", bad)
	}
}

func TestFillIgnoresLevelsNobodyPlayed(t *testing.T) {
	f := newFake(t)
	for _, l := range f.levels {
		l.TotalAttempts, l.Clears, l.Failures, l.ClearRate = 0, 0, 0, 0
	}

	p, _ := setup(t, f)
	p.MaxDraws = 50

	st, err := p.Fill(context.Background())
	if !errors.Is(err, pool.ErrMaxDraws) || st.Imported != 0 || st.NoAttempts == 0 || f.binCalls != 0 {
		t.Fatalf("err=%v stats=%+v bins=%d", err, st, f.binCalls)
	}
}

func TestFillGivesUpAtItsDrawBudget(t *testing.T) {
	f := newFake(t)
	f.levels = nil // an archive with nothing in it

	p, _ := setup(t, f)
	p.MaxDraws = 40
	p.Rand = func(int) int { return 2 } // always an even index, which maps to no level

	f.levels = []*upstream.Level{{LevelID: 1, TotalAttempts: 0}} // keep the fake's modulo happy

	st, err := p.Fill(context.Background())
	if !errors.Is(err, pool.ErrMaxDraws) || st.Draws != 40 {
		t.Fatalf("err=%v draws=%d", err, st.Draws)
	}
}

func TestFillStopsWhenCancelled(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := p.Fill(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestFillAbortsWhenUpstreamKeepsFailing(t *testing.T) {
	f := newFake(t)
	f.randomErr = errors.New("503 from upstream")

	p, _ := setup(t, f)

	start := time.Now()

	st, err := p.Fill(context.Background())
	if err == nil || errors.Is(err, pool.ErrMaxDraws) || st.APIErrors < 18 || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v errors=%d after %v", err, st.APIErrors, time.Since(start))
	}
}

func TestEnsureFetchesOnDemandRemembersMissesAndProtectsLocalUploads(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	ctx := context.Background()

	// * unknown to the catalog, known to the archive
	ok, err := p.Ensure(ctx, 1042)
	if err != nil || !ok {
		t.Fatalf("ensure: %v %v", ok, err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM datastore.objects WHERE data_id = 1042`).Scan(&name); err != nil || name != "Level 1042" {
		t.Fatalf("%q %v", name, err)
	}

	// * a second ask is served from the catalog, no upstream traffic
	calls := f.levelCalls
	if ok, _ := p.Ensure(ctx, 1042); !ok || f.levelCalls != calls {
		t.Fatalf("known level went upstream again (%d -> %d calls)", calls, f.levelCalls)
	}

	// * not in the archive: false, and remembered
	if ok, err := p.Ensure(ctx, 4242424); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}

	calls = f.levelCalls
	if ok, _ := p.Ensure(ctx, 4242424); ok || f.levelCalls != calls {
		t.Fatalf("a remembered miss went upstream again")
	}

	// * a transient upstream error is NOT remembered as a miss
	f.levelErr = errors.New("timeout")
	if ok, err := p.Ensure(ctx, 1043); ok || err == nil {
		t.Fatalf("%v %v", ok, err)
	}

	f.levelErr = nil
	if ok, err := p.Ensure(ctx, 1043); !ok || err != nil {
		t.Fatalf("level should be fetchable once upstream recovers: %v %v", ok, err)
	}

	// * an archive level never replaces something a player uploaded under the same ID
	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, name, data_type) VALUES (1044, 1, 9, 'mine', 3)`); err != nil {
		t.Fatal(err)
	}

	if ok, _ := p.Ensure(ctx, 1044); !ok { // it exists in the catalog, so it is "available"; the point is it stays untouched
		t.Fatal("existing object reported unavailable")
	}

	db.QueryRow(`SELECT name FROM datastore.objects WHERE data_id = 1044`).Scan(&name)
	if name != "mine" || f.binCalls != 2 {
		t.Fatalf("local object changed to %q or extra downloads happened (%d)", name, f.binCalls)
	}
}

func TestEnsureRemembersCapturesThatDoNotExistOrAreNotLevels(t *testing.T) {
	f := newFake(t)
	f.missingFor[f.levels[7].URL] = true
	f.garbageFor[f.levels[8].URL] = true

	p, _ := setup(t, f)

	for _, id := range []int64{1007, 1008} {
		if ok, err := p.Ensure(context.Background(), id); ok || err == nil {
			t.Fatalf("level %d: %v %v", id, ok, err)
		}
	}

	// * both are now negative-cached: asking again costs nothing
	levels, bins := f.levelCalls, f.binCalls

	for _, id := range []int64{1007, 1008} {
		p.Ensure(context.Background(), id)
	}

	if f.levelCalls != levels || f.binCalls != bins {
		t.Fatalf("remembered failures went upstream again: levels %d->%d bins %d->%d", levels, f.levelCalls, bins, f.binCalls)
	}
}

func TestOnMissingHookMakesLookupsFetchOnDemandThroughTheServersOwnFunctions(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)

	id := types.NewUInt64(1100)

	// * without the hook the level does not exist
	if err := datastore_db.IsObjectAvailable(id); err == nil {
		t.Fatal("level should not exist yet")
	}

	database.OnMissing = p.OnMissing

	if err := datastore_db.IsObjectAvailable(id); err != nil {
		t.Fatalf("on-demand fetch did not make the level available: %s", err.Message)
	}

	info, nexErr := datastore_db.GetObjectInfoByDataID(id)
	if nexErr != nil || string(info.Name) != "Level 1100" || len(info.MetaBinary) != 44 {
		t.Fatalf("%v %q", nexErr, info.Name)
	}

	// * the password variant used by GetMeta goes through the same hook
	if err := datastore_db.IsObjectAvailableWithPassword(types.NewUInt64(1101), 0); err != nil {
		t.Fatalf("password variant: %s", err.Message)
	}

	// * IDs the archive does not have stay NotFound, and one lookup asks upstream only once
	calls := f.levelCalls
	if err := datastore_db.IsObjectAvailable(types.NewUInt64(999999)); err == nil {
		t.Fatal("unknown id reported available")
	}

	if f.levelCalls != calls+1 {
		t.Fatalf("one failed lookup made %d upstream calls", f.levelCalls-calls)
	}

	// * impossible IDs never reach upstream
	calls = f.levelCalls
	for _, bad := range []uint64{0, 1 << 31, 1 << 40} {
		if p.OnMissing(bad) {
			t.Fatalf("%d accepted", bad)
		}
	}

	if f.levelCalls != calls {
		t.Fatal("implausible ids were looked up")
	}
}

func TestOnMissingNeverLoopsWhenTheImportedLevelIsStillUnavailable(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	database.OnMissing = p.OnMissing

	// * the fetch succeeds but the object ends up hidden (deleted), so the retry still finds nothing
	hook := database.OnMissing
	database.OnMissing = func(id uint64) bool {
		ok := hook(id)
		db.Exec(`UPDATE datastore.objects SET deleted = 1 WHERE data_id = $1`, id)

		return ok
	}

	done := make(chan *struct{}, 1)

	go func() {
		datastore_db.IsObjectAvailable(types.NewUInt64(1150))
		done <- nil
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lookup did not terminate")
	}
}

func TestBandsCoverEveryClearRateExactlyOnce(t *testing.T) {
	p := &pool.Pool{}

	for r := 0.0; r <= 1.0; r += 0.001 {
		n := 0

		for _, b := range pool.DefaultBands {
			if r >= b.Min && r < b.Max {
				n++
			}
		}

		if n != 1 {
			t.Fatalf("clear rate %.3f falls in %d bands", r, n)
		}
	}

	_ = p
}

func runMaintain(t *testing.T, p *pool.Pool, g pool.Growth) (cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{})

	go func() {
		p.Maintain(ctx, g)
		close(ch)
	}()

	t.Cleanup(cancel)

	return cancel, ch
}

func waitDone(t *testing.T, done <-chan struct{}, d time.Duration, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v", what, d)
	}
}

func TestMaintainGrowsEveryBandToTheCapThenStops(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)
	p.TargetPerBand = 2

	_, done := runMaintain(t, p, pool.Growth{Every: 5 * time.Millisecond, By: 1, Max: 4})
	waitDone(t, done, 60*time.Second, "growth to the cap")

	counts, _ := p.Counts()
	for i, c := range counts {
		if c != 4 {
			t.Fatalf("band %d holds %d, want the cap of 4 (%v)", i, c, counts)
		}
	}

	if f.binCalls != 16 {
		t.Fatalf("downloaded %d files for a pool of 16; growth must not fetch anything it does not keep", f.binCalls)
	}
}

func TestMaintainWithoutGrowthStopsAfterTheInitialFill(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)
	p.TargetPerBand = 2

	_, done := runMaintain(t, p, pool.Growth{}) // growth disabled
	waitDone(t, done, 30*time.Second, "initial fill")

	counts, _ := p.Counts()
	for _, c := range counts {
		if c != 2 {
			t.Fatalf("counts %v", counts)
		}
	}
}

func TestMaintainStopsPromptlyWhenCancelled(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)
	p.TargetPerBand = 1

	cancel, done := runMaintain(t, p, pool.Growth{Every: 5 * time.Millisecond, By: 1, Max: 1000})

	time.Sleep(150 * time.Millisecond)
	cancel()
	waitDone(t, done, 5*time.Second, "cancelled growth")
}

func TestOneBandThatCannotBeFilledDoesNotInflateTheOthers(t *testing.T) {
	f := newFake(t)

	// * the archive has nothing below 5%
	var kept []*upstream.Level
	for _, l := range f.levels {
		if l.ClearRate >= 0.05 {
			kept = append(kept, l)
		}
	}

	f.levels = kept

	p, _ := setup(t, f)
	p.TargetPerBand = 0

	cancel, done := runMaintain(t, p, pool.Growth{Every: 5 * time.Millisecond, By: 1, Max: 50})

	time.Sleep(400 * time.Millisecond)
	cancel()
	waitDone(t, done, 5*time.Second, "growth with an empty band")

	// * the lowest band never gets past 0, so the target stays at 1: the other bands hold exactly one level each
	counts, _ := p.Counts()
	if counts[0] != 0 || counts[1] != 1 || counts[2] != 1 || counts[3] != 1 {
		t.Fatalf("counts %v; an unfillable band must hold the others at the same target", counts)
	}
}
