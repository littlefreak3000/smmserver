package selection_test

import (
	"reflect"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/challenge"
	"github.com/PretendoNetwork/super-mario-maker/difficulty"
	"github.com/PretendoNetwork/super-mario-maker/selection"
)

// softCatalog fills a pool-mode catalog with levels of three kinds, as an import with the current prior would:
// ids 7000+ are 100 well-played levels at 10 %, 8000+ are 100 well-played levels at 30 %, 9000+ are 40 levels with only 10
// attempts and 1 clear (score about 12 %).
func softCatalog(t *testing.T, e *env) {
	t.Helper()

	add := func(base, n, clears, attempts int64) {
		for i := int64(0); i < n; i++ {
			id := base + i
			rate := float64(clears) / float64(attempts)

			for _, q := range []string{
				`INSERT INTO datastore.objects (data_id, upload_completed, owner, name, data_type) VALUES ($1, 1, 1, 'level', 3)`,
			} {
				if _, err := e.sel.DB.Exec(q, id); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := e.sel.DB.Exec(`INSERT INTO datastore.object_custom_rankings (data_id, application_id, value) VALUES ($1, $2, $3)`,
				id, diffApp, difficulty.Current.PercentCounts(clears, attempts)); err != nil {
				t.Fatal(err)
			}

			if _, err := e.sel.DB.Exec(`INSERT INTO datastore.level_sources (data_id, url, clear_rate, attempts, stars, fetched) VALUES ($1, 'u', $2, $3, 0, CURRENT_TIMESTAMP)`,
				id, rate, attempts); err != nil {
				t.Fatal(err)
			}
		}
	}

	add(7000, 100, 50, 500)
	add(8000, 100, 150, 500)
	add(9000, 40, 1, 10)
}

func softKind(id int64) string {
	switch {
	case id >= 9000:
		return "thin"
	case id >= 8000:
		return "middle"
	}

	return "easy"
}

func catalogTally(t *testing.T, e *env, lo, hi int64, rounds int) map[string]int {
	t.Helper()

	out := map[string]int{}

	for i := 0; i < rounds; i++ {
		for _, id := range e.ids(t, selection.Query{Kind: selection.Random, App: diffApp, Min: lo, Max: hi, Length: 20, NNID: "A"}) {
			out[softKind(id)]++
		}
	}

	return out
}

func TestCatalogListsPlaceThinLevelsInAWiderRange(t *testing.T) {
	old := difficulty.Current

	t.Cleanup(func() { difficulty.Current = old })

	// * without a prior: the thin levels (score 10 %) are only in the ranges that contain 10 %
	difficulty.Current = difficulty.Prior{}
	hard := newEnv(t, false)
	softCatalog(t, hard)

	h := catalogTally(t, hard, 20, 49, 30)
	if h["thin"] != 0 || h["easy"] != 0 || h["middle"] == 0 {
		t.Fatalf("hard edges, range 20-49: %v", h)
	}

	// * with the default prior: thin levels turn up in 20-49 now and then, well-played 10 % levels never
	difficulty.Current = difficulty.Default
	soft := newEnv(t, false)
	softCatalog(t, soft)

	s := catalogTally(t, soft, 20, 49, 30)
	if s["thin"] == 0 || s["easy"] != 0 || s["middle"] == 0 || s["thin"] > s["middle"]/2 {
		t.Fatalf("soft edges, range 20-49: %v", s)
	}

	// * in the range that contains them they are a smaller share of the list than of the levels (40 of 140)
	own := catalogTally(t, soft, 5, 19, 30)
	if own["thin"] == 0 || own["easy"] == 0 || float64(own["thin"])/float64(own["thin"]+own["easy"]) >= 40.0/140 {
		t.Fatalf("soft edges, range 5-19: %v", own)
	}

	// * the whole scale is not weighted: an unrestricted unseeded request is left to the normal query, as before
	if _, handled, _ := soft.sel.IDs(selection.Query{Kind: selection.Random, App: diffApp, Min: 0, Max: 100, Length: 20, NNID: "A"}); handled {
		t.Fatal("an unrestricted unseeded request was taken by the picker")
	}
}

func TestSeededCatalogListsStayAFunctionOfTheSeed(t *testing.T) {
	old := difficulty.Current

	t.Cleanup(func() { difficulty.Current = old })

	difficulty.Current = difficulty.Default
	e := newEnv(t, false)
	softCatalog(t, e)

	e.store.Set(challenge.Everyone, "soft seed") //nolint:errcheck

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 20, Max: 49, Length: 30, NNID: "A"}
	a := e.ids(t, q)
	q.NNID = "B"
	b := e.ids(t, q)

	if len(a) != 30 || !reflect.DeepEqual(a, b) {
		t.Fatalf("two players with one seed got different lists:\n%v\n%v", a, b)
	}

	// * a shorter request starts with the same levels
	q.Length = 10
	if short := e.ids(t, q); !reflect.DeepEqual(short, a[:10]) {
		t.Fatalf("a shorter list does not start the longer one:\n%v\n%v", short, a[:10])
	}

	e.store.Set(challenge.Everyone, "another seed") //nolint:errcheck

	q.Length = 30
	if reflect.DeepEqual(a, e.ids(t, q)) {
		t.Fatal("another seed gave the same list")
	}
}
