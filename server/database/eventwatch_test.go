package database_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/database"
)

func TestAChangedEventListUpdatesSizeAndTime(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, size, name, data_type, update_date) VALUES (900000, 1, 2, 450068, '', 50, $1)`, old); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, size, name, data_type, update_date) VALUES (12320059, 1, 5, 39292, 'level', 3, $1)`, old); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 15, 22, 0, 0, 0, time.UTC)

	if err := database.MarkEventCourseListChanged(db, 48924, now); err != nil {
		t.Fatal(err)
	}

	var size int64
	var updated time.Time

	if err := db.QueryRow(`SELECT size, update_date FROM datastore.objects WHERE data_id = 900000`).Scan(&size, &updated); err != nil {
		t.Fatal(err)
	}

	if size != 48924 || !updated.Equal(now) {
		t.Fatalf("size %d, updated %v; want 48924 at %v", size, updated, now)
	}

	// * nothing else is touched
	var other int64
	var otherUpdated time.Time

	if err := db.QueryRow(`SELECT size, update_date FROM datastore.objects WHERE data_id = 12320059`).Scan(&other, &otherUpdated); err != nil {
		t.Fatal(err)
	}

	if other != 39292 || !otherUpdated.Equal(old) {
		t.Fatalf("another object changed: size %d, updated %v", other, otherUpdated)
	}
}
