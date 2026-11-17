package selection_test

import (
	"fmt"
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
		e.sel.MarkPlayed("A", id)
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
		e.sel.MarkPlayed("A", int64(7000+i))
	}

	if got := e.ids(t, q); len(got) != 10 {
		t.Fatalf("with everything played the list has %d levels, want 10", len(got))
	}

	// * a seeded list ignores what was played: it is the same for everyone
	e.store.Set(challenge.Everyone, "seed one") //nolint:errcheck

	first := e.ids(t, q)

	e.sel.MarkPlayed("A", first[0])

	if again := e.ids(t, q); again[0] != first[0] {
		t.Fatal("a played level moved in a seeded list")
	}
}

// * helper: n levels in the 50-100% clear-rate range, ids 7000..7000+n-1
func stockLevels(t *testing.T, e *env, n int) {
	t.Helper()

	sample := readSample(t)

	for i := 0; i < n; i++ {
		l := upstream.Level{
			URL: "https://cdn.example/x", Name: "n", LevelID: int64(7000 + i), CreatorID: 1, Clears: int64(50 + i/2), Failures: 50,
			TotalAttempts: 100, ClearRate: float64(50+i/2) / 100, UploadTime: time.Unix(1_440_000_000, 0).UTC(), Stars: 1,
		}

		if err := levelimport.Import(e.sel.DB, l, sample, levelimport.Options{CoursesDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
}

func historyRows(t *testing.T, e *env, nnid string) int {
	t.Helper()

	var n int
	if err := e.sel.DB.QueryRow(`SELECT count(*) FROM datastore.play_history WHERE nnid = $1`, nnid).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestPlayHistoryIsPerPlayerAndSurvivesANewSelector(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 40)

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 50, Max: 100, Length: 10, NNID: "A"}

	// * player A plays 25 levels
	played := map[int64]bool{}

	for i := 0; i < 25; i++ {
		e.sel.MarkPlayed("a", int64(7000+i)) // the player's name is not case sensitive
		played[int64(7000+i)] = true
	}

	// * a selector that starts later (the server restarted) still knows
	again := &selection.Selector{DB: e.sel.DB, DifficultyApp: e.sel.DifficultyApp}

	for round := 0; round < 10; round++ {
		ids, handled, err := again.IDs(q)
		if err != nil || !handled {
			t.Fatalf("IDs: handled=%v err=%v", handled, err)
		}

		for _, id := range ids {
			if played[id] {
				t.Fatalf("after a restart player A was offered level %d, which they played, with unplayed ones left", id)
			}
		}
	}

	// * player B has played nothing, so for B those levels are as likely as any
	q.NNID = "B"
	seen := false

	for round := 0; round < 30 && !seen; round++ {
		for _, id := range e.ids(t, q) {
			seen = seen || played[id]
		}
	}

	if !seen {
		t.Fatal("player B never saw a level only player A played")
	}
}

func TestPlayHistoryIsARollingWindow(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 40)

	e.sel.HistoryLevels = 10

	for i := 0; i < 40; i++ {
		e.sel.MarkPlayed("A", int64(7000+i))
		time.Sleep(2 * time.Millisecond) // distinct timestamps
	}

	if n := historyRows(t, e, "A"); n != 10 {
		t.Fatalf("history has %d rows after 40 levels with a window of 10", n)
	}

	// * the window holds the newest ten: 7030..7039
	rows, err := e.sel.DB.Query(`SELECT data_id FROM datastore.play_history WHERE nnid = 'A' ORDER BY data_id`)
	if err != nil {
		t.Fatal(err)
	}

	defer rows.Close()

	want := int64(7030)

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}

		if id != want {
			t.Fatalf("history holds %d where %d was expected", id, want)
		}

		want++
	}

	// * playing one of them again does not add a row, and another player's window is separate
	e.sel.MarkPlayed("A", 7035)
	e.sel.MarkPlayed("B", 7001)

	if a, b := historyRows(t, e, "A"), historyRows(t, e, "B"); a != 10 || b != 1 {
		t.Fatalf("rows A=%d B=%d, want 10 and 1", a, b)
	}

	// * a level that is not one (the event list, a profile) is not recorded
	e.sel.MarkPlayed("A", 900000)

	if n := historyRows(t, e, "A"); n != 10 {
		t.Fatalf("a non-level changed the history: %d rows", n)
	}
}

func TestWhenEverythingWasPlayedTheOldestComeBack(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 30)

	// * play all 30 in order, oldest first
	for i := 0; i < 30; i++ {
		e.sel.MarkPlayed("A", int64(7000+i))
		time.Sleep(2 * time.Millisecond)
	}

	q := selection.Query{Kind: selection.Random, App: diffApp, Min: 50, Max: 100, Length: 5, NNID: "A"}

	for _, id := range e.ids(t, q) {
		if id >= 7005 {
			t.Fatalf("with everything played the list offered level %d, not one of the five played longest ago", id)
		}
	}
}

func TestHistoryCanBeSwitchedOff(t *testing.T) {
	e := newEnv(t, false)
	stockLevels(t, e, 5)

	e.sel.HistoryLevels = -1
	e.sel.MarkPlayed("A", 7000)

	if n := historyRows(t, e, "A"); n != 0 {
		t.Fatal(fmt.Sprintf("history is off but %d rows were written", n))
	}
}
