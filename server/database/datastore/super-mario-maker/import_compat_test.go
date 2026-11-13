package datastore_smm_db_test

// Cross-checks the catalog written by tools/import-archive.mjs (Node, node:sqlite) against the server's real
// query functions (Go, modernc sqlite). Skipped unless SMM_IMPORT_ARCHIVE points at an archive folder:
//
//	node tools/import-archive.mjs --archive X --meta list.json X.zip
//	SMM_IMPORT_ARCHIVE=X go test ./database/... -run ImportedArchive

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_smm_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	datastore_smm_db "github.com/PretendoNetwork/super-mario-maker/database/datastore/super-mario-maker"
)

func TestImportedArchiveReadsBackThroughServerQueries(t *testing.T) {
	archive := os.Getenv("SMM_IMPORT_ARCHIVE")
	if archive == "" {
		t.Skip("SMM_IMPORT_ARCHIVE not set")
	}

	// * work on a copy so the archive is untouched
	tmp := filepath.Join(t.TempDir(), "catalog.sqlite")
	src, err := os.Open(filepath.Join(archive, "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	dst, _ := os.Create(tmp)
	io.Copy(dst, src)
	src.Close()
	dst.Close()

	prev := database.DB
	database.DB, err = database.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { database.DB.Close(); database.DB = prev }()

	rows, err := database.DB.Query(`SELECT data_id, owner, size, name, (SELECT COUNT(*) FROM datastore.buffer_queues b WHERE b.data_id = o.data_id AND b.slot = 2),
		(SELECT value FROM datastore.object_custom_rankings r WHERE r.data_id = o.data_id AND r.application_id = 0) FROM datastore.objects o WHERE data_id <> 900000`)
	if err != nil {
		t.Fatal(err)
	}
	type lvl struct {
		id, owner, size, stars uint64
		name                   string
		rank                   sql.NullInt64
	}
	var levels []lvl
	for rows.Next() {
		var l lvl
		if err := rows.Scan(&l.id, &l.owner, &l.size, &l.name, &l.stars, &l.rank); err != nil {
			t.Fatal(err)
		}
		levels = append(levels, l)
	}
	rows.Close()
	if len(levels) == 0 {
		t.Fatal("archive has no levels")
	}

	newest, nexErr := datastore_smm_db.GetCourses(0, true, 0, 1000)
	if nexErr != nil {
		t.Fatal(nexErr.Message)
	}
	ranked, nexErr := datastore_smm_db.GetCourses(0, false, 0, 1000)
	if nexErr != nil {
		t.Fatal(nexErr.Message)
	}

	for _, l := range levels {
		id := types.NewUInt64(l.id)

		info, e := datastore_db.GetObjectInfoByDataID(id)
		if e != nil {
			t.Fatalf("level %d: %s", l.id, e.Message)
		}
		if string(info.Name) != l.name || uint64(info.OwnerID) != l.owner || uint64(info.Size) != l.size || len(info.MetaBinary) != 44 {
			t.Fatalf("level %d: info = %q owner %d size %d meta %d", l.id, info.Name, info.OwnerID, info.Size, len(info.MetaBinary))
		}
		if y := info.CreatedTime.Standard().Year(); y < 2015 || y > 2100 {
			t.Fatalf("level %d: created year %d", l.id, y)
		}
		if len(info.Ratings) != 5 {
			t.Fatalf("level %d: %d rating slots, want 5", l.id, len(info.Ratings))
		}

		if bin := filepath.Join(archive, "courses", strconv.FormatUint(l.id, 10)+".bin"); fileSize(bin) != int64(l.size) {
			t.Fatalf("level %d: courses/%d.bin is %d bytes, catalog says %d", l.id, l.id, fileSize(bin), l.size)
		}

		q, e := datastore_smm_db.GetBufferQueuesByDataIDAndSlot(id, 2)
		if e != nil || uint64(len(q)) != l.stars {
			t.Fatalf("level %d: %d star buffers, want %d (err %v)", l.id, len(q), l.stars, e)
		}

		if !contains(newest, l.id) {
			t.Fatalf("level %d missing from the newest list", l.id)
		}
		if l.rank.Valid && !contains(ranked, l.id) {
			t.Fatalf("level %d missing from the ranked list", l.id)
		}
		t.Logf("level %d %q: %d bytes, %d stars, listed OK", l.id, l.name, l.size, l.stars)
	}
}

func contains(list types.List[datastore_smm_types.DataStoreCustomRankingResult], id uint64) bool {
	for _, r := range list {
		if uint64(r.MetaInfo.DataID) == id {
			return true
		}
	}

	return false
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return -1
	}

	return st.Size()
}
