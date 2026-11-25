package database_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/database"
)

func TestCloseMergesTheWriteAheadLogSoTheFolderCanBeCopied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.sqlite")

	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	database.DB = db

	for i := 1; i <= 50; i++ {
		if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, name, data_type) VALUES ($1, 1, 1, 'level', 3)`, i); err != nil {
			t.Fatal(err)
		}
	}

	// * while open, the newest changes live in the -wal file
	if st, err := os.Stat(path + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("expected a non-empty -wal file while the catalog is open (%v)", err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if database.DB != nil {
		t.Fatal("DB should be nil after Close")
	}

	// * after a clean close nothing is left in the log: catalog.sqlite alone holds all 50 rows
	if st, err := os.Stat(path + "-wal"); err == nil && st.Size() != 0 {
		t.Fatalf("the -wal file still holds %d bytes after Close", st.Size())
	}

	only := filepath.Join(t.TempDir(), "copy.sqlite")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(only, raw, 0o644); err != nil { // a copy of the main file ALONE
		t.Fatal(err)
	}

	copyDB, err := database.Open(only)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()

	var n int
	if err := copyDB.QueryRow(`SELECT COUNT(*) FROM datastore.objects WHERE name = 'level'`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("the copied catalog has %d of 50 rows (%v)", n, err)
	}

	// * Close on an already closed catalog is harmless
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestParseServerID(t *testing.T) {
	for in, want := range map[string]uint16{"0001": 1, "a3f2": 0xA3F2, " A3F2 ": 0xA3F2, "FFFF": 0xFFFF, "0042": 0x42} {
		if got, err := database.ParseServerID(in); err != nil || got != want {
			t.Errorf("database.ParseServerID(%q) = %x, %v, want %x", in, got, err, want)
		}
	}

	for _, bad := range []string{"", "0000", "1", "123", "12345", "xyz1", "-001", "0x12", "A3 F2"} {
		if got, err := database.ParseServerID(bad); err == nil {
			t.Errorf("database.ParseServerID(%q) = %x was accepted", bad, got)
		}
	}
}
