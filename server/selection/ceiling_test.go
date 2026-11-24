package selection_test

import (
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/selection"
)

// * SMM_DIFFICULTY_CEILING: difficulty lists never offer a level above the ceiling, whatever range the game asks for

func rankingOf(t *testing.T, e *env, id int64) int64 {
	t.Helper()

	var v int64
	if err := e.sel.DB.QueryRow(`SELECT value FROM datastore.object_custom_rankings WHERE data_id = $1 AND application_id = $2`, id, diffApp).Scan(&v); err != nil {
		t.Fatalf("ranking of %d: %v", id, err)
	}

	return v
}

func TestACeilingKeepsTooEasyLevelsOutOfDifficultyLists(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 60) // clear rates from 50 % to 79 %

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 50, Max: 100, Length: 20, NNID: "A"}

	// * without a ceiling the lists do offer levels above 60
	above := false

	for round := 0; round < 20 && !above; round++ {
		for _, id := range e.ids(t, q) {
			above = above || rankingOf(t, e, id) > 60
		}
	}

	if !above {
		t.Fatal("test set-up: with no ceiling no level above 60 % was ever offered")
	}

	e.sel.Ceiling = 60

	for _, query := range []selection.Query{
		q, // the range reaches above the ceiling
		{Kind: selection.Random, App: diffApp, Min: 0, Max: 100, Length: 20, NNID: "A"},  // "all difficulties"
		{Kind: selection.Random, App: diffApp, Min: 90, Max: 100, Length: 20, NNID: "A"}, // wholly above the ceiling
	} {
		for round := 0; round < 20; round++ {
			ids := e.ids(t, query)
			if len(ids) == 0 {
				t.Fatalf("%+v: an empty list", query)
			}

			for _, id := range ids {
				if v := rankingOf(t, e, id); v > 60 {
					t.Fatalf("%+v: level %d with a clear rate of %d %% was offered under a ceiling of 60", query, id, v)
				}
			}
		}
	}
}

func TestACeilingOf100OrNothingChangesNothing(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 60)

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 50, Max: 100, Length: 20, NNID: "A"}

	for _, ceiling := range []int{0, 100, 250} {
		e.sel.Ceiling = ceiling

		above := false

		for round := 0; round < 20 && !above; round++ {
			for _, id := range e.ids(t, q) {
				above = above || rankingOf(t, e, id) > 60
			}
		}

		if !above {
			t.Fatalf("ceiling %d filtered levels out", ceiling)
		}
	}
}
