package selection_test

import (
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/challenge"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/selection"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// * pool mode: levels that were just played go to the back of random lists, and the pool is asked for more when the
// * unplayed ones run low

func TestPlayedLevelsGoToTheBackAndTheCatalogIsAskedForMore(t *testing.T) {
	e := newEnv(t, false)

	sample := readSample(t)

	for i := 0; i < 60; i++ {
		l := upstream.Level{
			URL: "https://cdn.example/x", Name: "n", LevelID: int64(7000 + i), CreatorID: 1, Clears: int64(50 + i/2), Failures: 50,
			TotalAttempts: 100, ClearRate: float64(50+i/2) / 100, UploadTime: time.Unix(1_440_000_000, 0).UTC(), Stars: 1,
		}

		if err := levelimport.Import(e.sel.DB, l, sample, levelimport.Options{CoursesDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}

	type ask struct {
		min, max int64
		unseen   int
	}

	var asks []ask

	e.sel.NeedMore = func(lo, hi int64, unseen int) { asks = append(asks, ask{lo, hi, unseen}) }

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 50, Max: 100, Length: 10, NNID: "A"}

	// * 60 levels, 10 wanted: plenty unplayed, so nobody is asked for more
	e.ids(t, q)

	if len(asks) != 0 {
		t.Fatalf("asked for more with 60 unplayed levels: %v", asks)
	}

	// * play 45 of them: 15 are left, fewer than two lists' worth
	played := map[int64]bool{}

	for i := 0; i < 45; i++ {
		id := int64(7000 + i)
		e.sel.MarkPlayed(id)
		played[id] = true
	}

	for round := 0; round < 20; round++ {
		for _, id := range e.ids(t, q) {
			if played[id] {
				t.Fatalf("round %d offered level %d that was played, while unplayed ones are left", round, id)
			}
		}
	}

	if len(asks) == 0 || asks[0].min != 50 || asks[0].max != 100 || asks[0].unseen != 15 {
		t.Fatalf("NeedMore calls: %v", asks)
	}

	// * play everything: the lists still work (played levels come back) instead of being empty
	for i := 0; i < 60; i++ {
		e.sel.MarkPlayed(int64(7000 + i))
	}

	if got := e.ids(t, q); len(got) != 10 {
		t.Fatalf("with everything played the list has %d levels, want 10", len(got))
	}

	// * a seeded list ignores what was played: it is the same for everyone
	e.store.Set(challenge.Everyone, "seed one") //nolint:errcheck

	first := e.ids(t, q)

	e.sel.MarkPlayed(first[0])

	if again := e.ids(t, q); again[0] != first[0] {
		t.Fatal("a played level moved in a seeded list")
	}
}
