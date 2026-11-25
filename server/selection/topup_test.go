package selection_test

import (
	"sort"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/selection"
)

// * a difficulty list is always as long as asked for, when the catalog holds that many levels: the game refuses a short
// * challenge list ("This service is currently unavailable")

func TestAShortDifficultyListIsToppedUpWithTheNearestLevels(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 60) // clear rates from 50 % to 79 %, nothing above

	// * a range far above everything: the 20 levels nearest to it are the 20 easiest
	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 95, Max: 100, Length: 20, NNID: "A"}

	for round := 0; round < 10; round++ {
		ids := e.ids(t, q)
		if len(ids) != 20 {
			t.Fatalf("round %d: %d levels, want 20", round, len(ids))
		}

		seen := map[int64]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("round %d: level %d is listed twice", round, id)
			}

			seen[id] = true
		}
	}

	var values []int64

	var all []int64

	for i := 0; i < 60; i++ { // the ids stockLevels makes
		all = append(all, int64(7000+i))
		values = append(values, rankingOf(t, e, int64(7000+i)))
	}

	sort.Slice(values, func(i, j int) bool { return values[i] > values[j] })

	threshold := values[19]
	got := map[int64]bool{}

	for _, id := range e.ids(t, q) {
		got[id] = true

		if v := rankingOf(t, e, id); v < threshold {
			t.Fatalf("level %d with a clear rate of %d %% was listed although %d %% levels exist", id, v, threshold)
		}
	}

	for _, id := range all {
		if rankingOf(t, e, id) > threshold && !got[id] {
			t.Fatalf("level %d (above the threshold of %d %%) is missing from the list", id, threshold)
		}
	}
}

func TestAListIsNotLongerThanTheCatalogAndAFullListIsUnchanged(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 12)

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 95, Max: 100, Length: 50, NNID: "A"}
	if ids := e.ids(t, q); len(ids) != 12 {
		t.Fatalf("%d levels listed from a catalog of 12, want all 12", len(ids))
	}

	// * a ceiling is kept: nothing above it is added to fill a list
	e.sel.Ceiling = 55

	for _, id := range e.ids(t, q) {
		if v := rankingOf(t, e, id); v > 55 {
			t.Fatalf("level %d with a clear rate of %d %% was added to fill a list under a ceiling of 55", id, v)
		}
	}
}
