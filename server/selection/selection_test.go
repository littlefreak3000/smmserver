package selection_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/challenge"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/index"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/selection"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

const diffApp = 300000000

type env struct {
	sel      *selection.Selector
	store    *challenge.Store
	prefetch [][]int64
	dead     map[int64]bool
}

// newEnv builds an index of 3000 levels (ids 1000..3999 step ~1, clear rates spread over 0..100%) and a selector over it.
func newEnv(t *testing.T, withIndex bool) *env {
	t.Helper()

	globals.Logger = plogger.NewLogger()

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	e := &env{store: &challenge.Store{DB: db}, dead: map[int64]bool{}}

	e.sel = &selection.Selector{
		DB: db, Seeds: e.store, DifficultyApp: diffApp,
		Stub:     func(l upstream.Level) error { return levelimport.ImportStub(db, l, levelimport.Options{}) },
		Prefetch: func(ids ...int64) { e.prefetch = append(e.prefetch, ids) },
		Dead:     func(id int64) bool { return e.dead[id] },
	}

	if !withIndex {
		return e
	}

	path := filepath.Join(t.TempDir(), "index.sqlite")

	b, err := index.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3000; i++ {
		id := int64(1000 + i)
		attempts := int64(100)
		clears := int64((i * 37) % 101)

		err := b.Add(index.Record{
			ID: id, Name: fmt.Sprintf("Level %d", id), Creator: 1_000_000_000 + id%50, Clears: clears, Attempts: attempts, Stars: id % 40,
			Upload: time.Date(2015, 9, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Minute),
			URL:    fmt.Sprintf("https://cdn.example/ds/1/data/%011d-00001?Expires=1624143146&Signature=AAAA~bbb_&Key-Pair-Id=KP1", id),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := b.Finish(); err != nil {
		t.Fatal(err)
	}

	x, err := index.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { x.Close() })

	e.sel.Index = x

	return e
}

func (e *env) ids(t *testing.T, q selection.Query) []int64 {
	t.Helper()

	ids, handled, err := e.sel.IDs(q)
	if err != nil || !handled {
		t.Fatalf("IDs(%+v): handled=%v err=%v", q, handled, err)
	}

	return ids
}

func hundred(nnid string) selection.Query {
	return selection.Query{Kind: selection.Random, App: diffApp, Min: 1, Max: 49, Length: 32, NNID: nnid}
}

func TestSameSeedGivesTheSameLevelsToEveryPlayer(t *testing.T) {
	e := newEnv(t, true)

	if err := e.store.Set(challenge.Everyone, "Friday Race"); err != nil {
		t.Fatal(err)
	}

	a, b := e.ids(t, hundred("Alice")), e.ids(t, hundred("Bob"))

	if len(a) != 32 || !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed, different levels:\n%v\n%v", a, b)
	}

	// * spelling, case and spacing of the seed do not matter
	if err := e.store.Set(challenge.Everyone, "  friday   RACE "); err != nil {
		t.Fatal(err)
	}

	if c := e.ids(t, hundred("Carol")); !reflect.DeepEqual(a, c) {
		t.Fatal("a differently typed seed gave other levels")
	}

	// * a different seed, a different list
	e.store.Set(challenge.Everyone, "another one") //nolint:errcheck

	if d := e.ids(t, hundred("Alice")); reflect.DeepEqual(a, d) {
		t.Fatal("two seeds gave the same list")
	}

	// * a player's own seed beats the shared one, and returns to it once cleared
	e.store.Set(challenge.Everyone, "Friday Race")              //nolint:errcheck
	e.store.Set(challenge.PlayerScope("Bob"), "something else") //nolint:errcheck

	if reflect.DeepEqual(e.ids(t, hundred("Bob")), a) || !reflect.DeepEqual(e.ids(t, hundred("Alice")), a) {
		t.Fatal("per-player seed was not applied")
	}

	e.store.Clear(challenge.PlayerScope("Bob")) //nolint:errcheck

	if !reflect.DeepEqual(e.ids(t, hundred("Bob")), a) {
		t.Fatal("Bob did not return to the shared seed")
	}
}

func TestNoSeedMeansRandomLevels(t *testing.T) {
	e := newEnv(t, true)

	a, b := e.ids(t, hundred("Alice")), e.ids(t, hundred("Alice"))

	if reflect.DeepEqual(a, b) {
		t.Fatal("two unseeded draws of 32 levels were identical")
	}
}

func TestLongerRequestsStartWithTheShorterOne(t *testing.T) {
	e := newEnv(t, true)
	e.store.Set(challenge.Everyone, "prefix") //nolint:errcheck

	short := hundred("A")
	short.Length = 10
	long := hundred("A")
	long.Length = 60

	s, l := e.ids(t, short), e.ids(t, long)

	if len(s) != 10 || len(l) != 60 || !reflect.DeepEqual(s, l[:10]) {
		t.Fatalf("not a prefix:\n%v\n%v", s, l[:10])
	}

	// * a later page continues the same list
	page := hundred("A")
	page.Offset, page.Length = 10, 20

	if got := e.ids(t, page); !reflect.DeepEqual(got, l[10:30]) {
		t.Fatalf("offset page differs from the same slice of the long list")
	}
}

func TestSeededLevelsStayInTheRequestedDifficultyRange(t *testing.T) {
	e := newEnv(t, true)
	e.store.Set(challenge.Everyone, "range") //nolint:errcheck

	q := hundred("A")
	q.Min, q.Max, q.Length = 96, 100, 40

	for _, id := range e.ids(t, q) {
		l, err := e.sel.Index.ByID(id)
		if err != nil || l == nil {
			t.Fatalf("level %d: %v", id, err)
		}

		if l.Bucket < 96 || l.Bucket > 100 {
			t.Fatalf("level %d has clear rate %d%%", id, l.Bucket)
		}
	}

	// * different ranges are different lists, even with the same seed
	easy := q
	easy.Min, easy.Max = 0, 34

	if reflect.DeepEqual(e.ids(t, q), e.ids(t, easy)) {
		t.Fatal("different ranges gave the same list")
	}
}

func TestChosenLevelsAreListedAsStubsAndPrefetchedInOrder(t *testing.T) {
	e := newEnv(t, true)
	e.store.Set(challenge.Everyone, "stubs") //nolint:errcheck

	q := hundred("A")
	q.Length = 5
	ids := e.ids(t, q)

	if len(e.prefetch) != 1 || !reflect.DeepEqual(e.prefetch[0], ids) {
		t.Fatalf("prefetch calls %v, want one call with %v", e.prefetch, ids)
	}

	for _, id := range ids {
		var stub, known = false, false
		if err := e.sel.DB.QueryRow(`SELECT 1, fetched IS NULL FROM datastore.level_sources WHERE data_id = $1`, id).Scan(&known, &stub); err != nil || !known || !stub {
			t.Fatalf("level %d: known=%v stub=%v err=%v", id, known, stub, err)
		}
	}
}

func TestDeadLevelsAreNeverOffered(t *testing.T) {
	e := newEnv(t, true)
	e.store.Set(challenge.Everyone, "dead") //nolint:errcheck

	q := hundred("A")
	first := e.ids(t, q)

	for _, id := range first[:5] {
		e.dead[id] = true
	}

	again := e.ids(t, q)

	if len(again) != 32 {
		t.Fatalf("got %d levels, the list should be topped up", len(again))
	}

	for _, id := range again {
		if e.dead[id] {
			t.Fatalf("dead level %d was offered", id)
		}
	}
}

func TestEmptyRangeAndOtherRankingsFallBackToTheCaller(t *testing.T) {
	e := newEnv(t, true)

	// * a range with no levels (the index holds clear rates of 0..100 but 'min > max' never matches)
	if _, handled, err := e.sel.IDs(selection.Query{Kind: selection.Random, App: diffApp, Min: 60, Max: 50, Length: 10}); handled || err != nil {
		t.Fatalf("empty range: handled=%v err=%v", handled, err)
	}

	// * the index only knows the difficulty ranking; without a seed any other ranking is left to the catalog
	if _, handled, _ := e.sel.IDs(selection.Query{Kind: selection.Random, App: 0, Min: 1, Max: 5, Length: 10}); handled {
		t.Fatal("index answered for a ranking it does not cover")
	}
}

func TestLatestAndStarsComeFromTheIndex(t *testing.T) {
	e := newEnv(t, true)

	latest := e.ids(t, selection.Query{Kind: selection.Latest, Length: 5})
	if !reflect.DeepEqual(latest, []int64{3999, 3998, 3997, 3996, 3995}) {
		t.Fatalf("latest = %v", latest)
	}

	stars := e.ids(t, selection.Query{Kind: selection.Stars, Length: 3})

	for _, id := range stars {
		if l, _ := e.sel.Index.ByID(id); l == nil || l.Stars != 39 {
			t.Fatalf("level %d is not among the most-starred", id)
		}
	}

	e.dead[latest[0]] = true

	if again := e.ids(t, selection.Query{Kind: selection.Latest, Length: 5}); again[0] != 3998 {
		t.Fatalf("a dead level is listed: %v", again)
	}
}

func TestFailureRangesBecomeClearRanges(t *testing.T) {
	if lo, hi := selection.FailureToClear(96, 100); lo != 0 || hi != 4 {
		t.Fatalf("Super Expert -> %d..%d", lo, hi)
	}

	if lo, hi := selection.FailureToClear(0, 34); lo != 66 || hi != 100 {
		t.Fatalf("Easy -> %d..%d", lo, hi)
	}
}

// * pool mode (no index): unseeded requests are left to the database query; seeded ones are a stable pick from the catalog

func TestPoolModeSeedingPicksTheSameCatalogLevels(t *testing.T) {
	e := newEnv(t, false)

	sample := readSample(t)

	for i := 0; i < 60; i++ {
		l := upstream.Level{
			URL: "https://cdn.example/x", Name: "n", LevelID: int64(5000 + i), CreatorID: 1, Clears: int64(i), Failures: 100 - int64(i),
			TotalAttempts: 100, ClearRate: float64(i) / 100, UploadTime: time.Unix(1_440_000_000, 0).UTC(), Stars: 1,
		}

		if err := levelimport.Import(e.sel.DB, l, sample, levelimport.Options{CoursesDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 10, Max: 40, Length: 8, NNID: "A"}

	// * no seed: still honors the difficulty range (the normal query would ignore it), and is random each time
	first := e.ids(t, q)
	second := e.ids(t, q)

	for _, id := range append(append([]int64{}, first...), second...) {
		if id < 5010 || id > 5040 {
			t.Fatalf("unseeded pool request returned level %d outside 10..40%%", id)
		}
	}

	if reflect.DeepEqual(first, second) {
		t.Fatal("two unseeded draws were identical")
	}

	if _, handled, _ := e.sel.IDs(selection.Query{Kind: selection.Random, App: diffApp, Min: 0, Max: 100, Length: 8}); handled {
		t.Fatal("an unrestricted unseeded pool request can use the normal query")
	}

	e.store.Set(challenge.Everyone, "pool seed") //nolint:errcheck

	a := e.ids(t, q)
	q.NNID = "B"
	b := e.ids(t, q)

	if len(a) != 8 || !reflect.DeepEqual(a, b) {
		t.Fatalf("seeded pool picks differ:\n%v\n%v", a, b)
	}

	for _, id := range a {
		if id < 5010 || id > 5040 {
			t.Fatalf("level %d is outside 10..40%%", id)
		}
	}

	e.store.Set(challenge.Everyone, "other seed") //nolint:errcheck

	if reflect.DeepEqual(a, e.ids(t, q)) {
		t.Fatal("another seed picked the same levels")
	}
}
