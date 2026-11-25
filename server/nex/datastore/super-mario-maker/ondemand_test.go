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

func TestSearchByIDAsksTheArchiveForLevelsTheCatalogLacks(t *testing.T) {
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

	bringIntoCatalog(types.List[types.UInt64]{7000, 11, 12, 13, 14, 15})

	// * the held level is not asked for, and no more than three lookups happen for one request
	if len(asked) != 3 || asked[0] != 11 || asked[1] != 12 || asked[2] != 13 {
		t.Fatalf("asked the archive for %v, want [11 12 13]", asked)
	}

	// * a single missing ID, as in a search by course ID, is asked for
	asked = nil

	bringIntoCatalog(types.List[types.UInt64]{572666675})

	if len(asked) != 1 || asked[0] != 572666675 {
		t.Fatalf("asked %v for a search by ID, want [572666675]", asked)
	}

	// * with on-demand fetching off nothing happens, and nothing panics
	database.OnMissing = nil

	bringIntoCatalog(types.List[types.UInt64]{1, 2, 3})
}
