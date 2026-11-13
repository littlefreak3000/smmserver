package levelimport_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

func TestStubIsListedWithoutAFileAndCompleteFillsItIn(t *testing.T) {
	db := openDB(t)
	dir := t.TempDir()
	opt := levelimport.Options{CoursesDir: dir}

	if err := levelimport.ImportStub(db, level(777), opt); err != nil {
		t.Fatal(err)
	}

	stub, known, err := levelimport.IsStub(db, 777)
	if err != nil || !stub || !known {
		t.Fatalf("after ImportStub: stub=%v known=%v err=%v", stub, known, err)
	}

	if _, err := os.Stat(filepath.Join(dir, "777.bin")); !os.IsNotExist(err) {
		t.Fatalf("a stub must not write a file (stat err: %v)", err)
	}

	if n := count(t, db, `SELECT size FROM datastore.objects WHERE data_id = 777`); n != 0 {
		t.Fatalf("stub size = %d", n)
	}

	// * the ratings and rankings are there already, so the level can be sorted and listed
	if n := count(t, db, `SELECT COUNT(*) FROM datastore.object_custom_rankings WHERE data_id = 777`); n != 2 {
		t.Fatalf("stub has %d rankings, want stars + difficulty", n)
	}

	// * a second stub for the same level changes nothing
	other := level(777)
	other.Name = "renamed"

	if err := levelimport.ImportStub(db, other, opt); err != nil {
		t.Fatal(err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM datastore.objects WHERE data_id = 777`).Scan(&name); err != nil || name != "Mini castle destruction" {
		t.Fatalf("name = %q, %v", name, err)
	}

	// * a file that is not a level does not complete it
	if err := levelimport.Complete(db, 777, []byte("<Error/>"), opt); err == nil {
		t.Fatal("garbage completed a stub")
	}

	if stub, _, _ := levelimport.IsStub(db, 777); !stub {
		t.Fatal("a failed Complete must leave the stub as it was")
	}

	bin := sample(t)

	if err := levelimport.Complete(db, 777, bin, opt); err != nil {
		t.Fatal(err)
	}

	if stub, known, _ := levelimport.IsStub(db, 777); stub || !known {
		t.Fatalf("after Complete: stub=%v known=%v", stub, known)
	}

	if n := count(t, db, `SELECT size FROM datastore.objects WHERE data_id = 777`); n != len(bin) {
		t.Fatalf("size = %d, want %d", n, len(bin))
	}

	var meta []byte
	if err := db.QueryRow(`SELECT meta_binary FROM datastore.objects WHERE data_id = 777`).Scan(&meta); err != nil || len(meta) != 44 {
		t.Fatalf("meta_binary: %d bytes, %v", len(meta), err)
	}

	if got, err := os.ReadFile(filepath.Join(dir, "777.bin")); err != nil || len(got) != len(bin) {
		t.Fatalf("file: %d bytes, %v", len(got), err)
	}

	// * completing the same level as a full Import would have: identical rows
	if err := levelimport.Import(db, level(778), bin, opt); err != nil {
		t.Fatal(err)
	}

	var a []byte
	db.QueryRow(`SELECT meta_binary FROM datastore.objects WHERE data_id = 778`).Scan(&a) //nolint:errcheck

	if string(a) != string(meta) {
		t.Fatal("Complete and Import produce different meta_binary")
	}
}

func TestStubNeverTouchesLocalObjectsAndCompleteNeedsAStub(t *testing.T) {
	db := openDB(t)
	opt := levelimport.Options{CoursesDir: t.TempDir()}

	if err := levelimport.Complete(db, 4242, sample(t), opt); !errors.Is(err, levelimport.ErrNoStub) {
		t.Fatalf("Complete of an unknown level: %v", err)
	}

	// * an object a player uploaded (no level_sources row)
	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, size, name, data_type, meta_binary, permission,
		permission_recipients, delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id, extra_data,
		creation_date, update_date) VALUES (4243, 1, 1, 10, 'mine', 3, x'00', 0, '[]', 3, '[]', 0, 0, 0, '[]', 0, '[]', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	if err := levelimport.ImportStub(db, level(4243), opt); !errors.Is(err, levelimport.ErrLocalObject) {
		t.Fatalf("stub over a local object: %v", err)
	}

	if stub, known, _ := levelimport.IsStub(db, 4243); stub || known {
		t.Fatalf("a local object is not an archive level: stub=%v known=%v", stub, known)
	}

	if err := levelimport.Complete(db, 4243, sample(t), opt); !errors.Is(err, levelimport.ErrNoStub) {
		t.Fatalf("Complete over a local object: %v", err)
	}
}

// A level imported with its archived record gets a course record row (so the game does not call every clear a "first
// clear" and "new record"); a record set locally in the meantime is not overwritten.
func TestImportStoresTheArchivedRecord(t *testing.T) {
	db := openDB(t)

	lvl := upstream.Level{
		URL: "https://cdn.example/x", Name: "n", LevelID: 7001, CreatorID: 5, Clears: 10, Failures: 5, TotalAttempts: 15, ClearRate: 10.0 / 15,
		UploadTime: time.Date(2015, 9, 20, 0, 0, 0, 0, time.UTC), WorldRecordMs: 12345, WorldRecordPID: 111, FirstClearPID: 222,
		WorldRecordCreated: time.Date(2015, 12, 1, 0, 0, 0, 0, time.UTC), WorldRecordUpdated: time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	if err := levelimport.Import(db, lvl, sample(t), levelimport.Options{CoursesDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	var first, best, score int64
	if err := db.QueryRow(`SELECT first_pid, best_pid, best_score FROM datastore.course_records WHERE data_id = 7001 AND slot = 0`).Scan(&first, &best, &score); err != nil {
		t.Fatal(err)
	}

	if first != 222 || best != 111 || score != 12345 {
		t.Fatalf("record = first %d best %d score %d", first, best, score)
	}

	// a local player beat it (and is a stranger to the archive); looking the record up again must keep their better time
	if _, err := db.Exec(`UPDATE datastore.course_records SET best_pid = 999, best_score = 5000 WHERE data_id = 7001`); err != nil {
		t.Fatal(err)
	}

	if err := levelimport.ImportRecord(db, 7001, lvl); err != nil {
		t.Fatal(err)
	}

	if err := db.QueryRow(`SELECT best_pid, best_score FROM datastore.course_records WHERE data_id = 7001`).Scan(&best, &score); err != nil || best != 999 || score != 5000 {
		t.Fatalf("local record overwritten: best %d score %d (%v)", best, score, err)
	}

	if err := db.QueryRow(`SELECT first_pid FROM datastore.course_records WHERE data_id = 7001`).Scan(&first); err != nil || first != 222 {
		t.Fatalf("first clear = %d (%v), want the archived first clearer", first, err)
	}

	if _, checked, _ := levelimport.RecordState(db, 7001); !checked {
		t.Fatal("the lookup was not remembered")
	}
}
