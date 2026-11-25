package nex_datastore_super_mario_maker

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/PretendoNetwork/nex-go/v2/types"
	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

func TestOnlyASearchByOneIDWaitsForTheArchive(t *testing.T) {
	globals.Logger = plogger.NewLogger()

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	oldDB, oldMissing := database.DB, database.OnMissing

	t.Cleanup(func() { database.DB, database.OnMissing = oldDB, oldMissing })

	database.DB = db

	// * one level the catalog already holds
	err = levelimport.ImportStub(db, upstream.Level{
		URL: "https://cdn.example/x", Name: "held", LevelID: 7000, CreatorID: 1, Clears: 5, Failures: 5, TotalAttempts: 10, ClearRate: 0.5,
		UploadTime: time.Unix(1_440_000_000, 0).UTC(), Stars: 1,
	}, levelimport.Options{})
	if err != nil {
		t.Fatal(err)
	}

	var asked []uint64

	database.OnMissing = func(id uint64) bool { asked = append(asked, id); return false }

	// * a request naming several IDs (the rankings of a list, levels the game remembers) never waits for a download
	bringIntoCatalog(types.List[types.UInt64]{7000, 11, 12, 13, 14, 15})
	bringIntoCatalog(types.List[types.UInt64]{11, 12})

	if len(asked) != 0 {
		t.Fatalf("a request with several IDs asked the archive for %v", asked)
	}

	// * a single missing ID, as in a search by course ID, is asked for
	bringIntoCatalog(types.List[types.UInt64]{572666675})

	if len(asked) != 1 || asked[0] != 572666675 {
		t.Fatalf("asked %v for a search by ID, want [572666675]", asked)
	}

	// * a single ID the catalog holds is not asked for
	asked = nil

	bringIntoCatalog(types.List[types.UInt64]{7000})

	if len(asked) != 0 {
		t.Fatalf("asked %v for a level the catalog holds", asked)
	}

	// * with on-demand fetching off nothing happens, and nothing panics
	database.OnMissing = nil

	bringIntoCatalog(types.List[types.UInt64]{1})
}
