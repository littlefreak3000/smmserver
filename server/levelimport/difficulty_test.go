package levelimport_test

import (
	"database/sql"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/difficulty"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
)

func withPrior(t *testing.T, p difficulty.Prior) {
	t.Helper()

	old := difficulty.Current
	difficulty.Current = p

	t.Cleanup(func() { difficulty.Current = old })
}

func rankingOf(t *testing.T, db *sql.DB, id int64) int64 {
	t.Helper()

	var v int64
	if err := db.QueryRow(`SELECT value FROM datastore.object_custom_rankings WHERE data_id = $1 AND application_id = 300000000`, id).Scan(&v); err != nil {
		t.Fatalf("level %d: %v", id, err)
	}

	return v
}

// addLevel puts a level with the given counts into the catalog the way an import does (an object, its ranking value and
// its level_sources row), with the ranking value chosen by the caller.
func addLevel(t *testing.T, db *sql.DB, id int64, clears, attempts int64, ranking int64) {
	t.Helper()

	rate := 0.0
	if attempts > 0 {
		rate = float64(clears) / float64(attempts)
	}

	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, name, data_type) VALUES ($1, 1, 1, 'level', 3)`, id); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO datastore.object_custom_rankings (data_id, application_id, value) VALUES ($1, 300000000, $2)`, id, ranking); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO datastore.level_sources (data_id, url, clear_rate, attempts, stars, fetched) VALUES ($1, 'u', $2, $3, 0, CURRENT_TIMESTAMP)`, id, rate, attempts); err != nil {
		t.Fatal(err)
	}
}

func TestAnImportedLevelGetsItsDifficultyScoreAsRankingValue(t *testing.T) {
	db := openDB(t)
	opt := levelimport.Options{CoursesDir: t.TempDir()}

	// * 3 clears in 3 attempts: raw 100, with the default prior 60
	thin := level(900)
	thin.Clears, thin.Failures, thin.TotalAttempts, thin.ClearRate = 3, 0, 3, 1

	withPrior(t, difficulty.Prior{})

	if err := levelimport.ImportStub(db, thin, opt); err != nil {
		t.Fatal(err)
	}

	if got := rankingOf(t, db, 900); got != 100 {
		t.Fatalf("raw: %d, want 100", got)
	}

	withPrior(t, difficulty.Default)

	thin.LevelID = 901
	if err := levelimport.ImportStub(db, thin, opt); err != nil {
		t.Fatal(err)
	}

	if got := rankingOf(t, db, 901); got != 60 {
		t.Fatalf("with the default prior: %d, want 60", got)
	}

	// * a well-played level keeps its rate (352 of 573 = 61 %)
	big := level(902)
	if err := levelimport.ImportStub(db, big, opt); err != nil {
		t.Fatal(err)
	}

	if got := rankingOf(t, db, 902); got != 61 {
		t.Fatalf("352/573 with the default prior: %d, want 61", got)
	}
}

func TestRecomputeDifficultyFollowsThePriorAndIsIdempotent(t *testing.T) {
	db := openDB(t)

	addLevel(t, db, 1, 3, 3, 100)     // thin: 100 raw, 60 with the default prior
	addLevel(t, db, 2, 20, 400, 5)    // 5 % either way
	addLevel(t, db, 3, 0, 5, 0)       // thin: 0 raw, 8 with the default prior
	addLevel(t, db, 4, 0, 0, 77)      // no attempts: left alone
	addLevel(t, db, 5, 200, 1000, 20) // 20 % either way

	// * a level that is not from the archive (no level_sources row) is never touched
	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, name, data_type) VALUES (6, 1, 1, 'mine', 3)`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO datastore.object_custom_rankings (data_id, application_id, value) VALUES (6, 300000000, 42)`); err != nil {
		t.Fatal(err)
	}

	withPrior(t, difficulty.Default)

	n, err := levelimport.RecomputeDifficulty(db, 300000000)
	if err != nil || n != 2 {
		t.Fatalf("first run: %d changed, %v; want 2 (the two thin levels)", n, err)
	}

	for id, want := range map[int64]int64{1: 60, 2: 5, 3: 8, 4: 77, 5: 20, 6: 42} {
		if got := rankingOf(t, db, id); got != want {
			t.Errorf("level %d: %d, want %d", id, got, want)
		}
	}

	if n, err := levelimport.RecomputeDifficulty(db, 300000000); err != nil || n != 0 {
		t.Fatalf("second run: %d changed, %v; want 0", n, err)
	}

	// * switching the prior off brings the raw clear rates back
	difficulty.Current = difficulty.Prior{}

	if n, err := levelimport.RecomputeDifficulty(db, 300000000); err != nil || n != 2 {
		t.Fatalf("after switching off: %d changed, %v; want 2", n, err)
	}

	for id, want := range map[int64]int64{1: 100, 2: 5, 3: 0, 4: 77, 5: 20, 6: 42} {
		if got := rankingOf(t, db, id); got != want {
			t.Errorf("level %d after off: %d, want %d", id, got, want)
		}
	}
}
