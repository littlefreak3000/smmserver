package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestTheEventListIsRefreshedOnlyWhenTheFileChanged(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, size, name, data_type, update_date) VALUES (900000, 1, 2, 450068, '', 50, $1)`, old); err != nil {
		t.Fatal(err)
	}

	state := func() (int64, time.Time) {
		var size int64
		var updated time.Time

		if err := db.QueryRow(`SELECT size, update_date FROM datastore.objects WHERE data_id = 900000`).Scan(&size, &updated); err != nil {
			t.Fatal(err)
		}

		return size, updated
	}

	v1 := minio.ObjectInfo{Size: 450068, ETag: "aaa", LastModified: old}
	rememberEventCourseFile(v1) // what start-up does

	now := time.Date(2026, 11, 15, 22, 0, 0, 0, time.UTC)

	// * the same file again: nothing happens
	if changed, err := refreshEventCourseList(db, v1, now); changed || err != nil {
		t.Fatalf("unchanged file: changed %v, %v", changed, err)
	}

	if size, updated := state(); size != 450068 || !updated.Equal(old) {
		t.Fatalf("the catalog was touched without a change: %d %v", size, updated)
	}

	// * a replaced file (other size): the catalog follows, once
	v2 := minio.ObjectInfo{Size: 48924, ETag: "bbb", LastModified: now}

	if changed, err := refreshEventCourseList(db, v2, now); !changed || err != nil {
		t.Fatalf("replaced file: changed %v, %v", changed, err)
	}

	if size, updated := state(); size != 48924 || !updated.Equal(now) {
		t.Fatalf("size %d updated %v after the file changed", size, updated)
	}

	later := now.Add(time.Hour)

	if changed, _ := refreshEventCourseList(db, v2, later); changed {
		t.Fatal("the same new file was handled twice")
	}

	if _, updated := state(); !updated.Equal(now) {
		t.Fatalf("update time moved without a change: %v", updated)
	}

	// * same size, other content (an edit of the same length) is a change too
	v3 := minio.ObjectInfo{Size: 48924, ETag: "ccc", LastModified: later}

	if changed, err := refreshEventCourseList(db, v3, later); !changed || err != nil {
		t.Fatalf("same-size edit: changed %v, %v", changed, err)
	}

	if _, updated := state(); !updated.Equal(later) {
		t.Fatalf("update time %v after a same-size edit, want %v", updated, later)
	}
}

func TestWithoutABaselineTheFirstLookOnlyRemembers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	rememberEventCourseFile(minio.ObjectInfo{}) // a zero info is a version too, so clear the baseline explicitly
	eventList.Lock()
	eventList.sig = ""
	eventList.Unlock()

	if changed, err := refreshEventCourseList(db, minio.ObjectInfo{Size: 5, ETag: "x"}, time.Now()); changed || err != nil {
		t.Fatalf("changed %v, %v", changed, err)
	}
}
