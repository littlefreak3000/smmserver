package levelimport_test

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

func sample(t *testing.T) []byte {
	t.Helper()

	b, err := os.ReadFile("testdata/sample.bin")
	if os.IsNotExist(err) {
		t.Skip("no sample level file: see testdata/README.md")
	}

	if err != nil {
		t.Fatal(err)
	}

	return b
}

func level(id int64) upstream.Level {
	return upstream.Level{
		URL: "https://cdn.example/data/00001829603-00001?Expires=1&Signature=x", Name: "Mini castle destruction", Creator: "Zaxonork",
		LevelID: id, CreatorID: 1799947827, Clears: 352, Failures: 221, TotalAttempts: 573, ClearRate: 352.0 / 573.0,
		UploadTime: time.Date(2015, 9, 12, 1, 7, 16, 0, time.UTC), Stars: 35,
	}
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()

	globals.Logger = plogger.NewLogger()

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	database.DB = db
	t.Cleanup(func() { db.Close() })

	return db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()

	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestSplitAndMetaBinaryMatchTheNodeTool(t *testing.T) {
	c, err := levelimport.Split(sample(t))
	if err != nil {
		t.Fatal(err)
	}

	want, err := os.ReadFile("testdata/sample.meta") // written by tools/build-bin.mjs for the same file
	if os.IsNotExist(err) {
		t.Skip("no sample meta file: see testdata/README.md")
	}

	if err != nil {
		t.Fatal(err)
	}

	if got := levelimport.MetaBinary(c); !bytes.Equal(got, want) {
		t.Fatalf("meta_binary differs from the Node tool:\n go   %x\n node %x", got, want)
	}
}

func TestSplitRejectsAnythingThatIsNotACourseFile(t *testing.T) {
	good := sample(t)

	c, _ := levelimport.Split(good)

	swapped := bytes.Join([][]byte{c.Course, c.Thumbnail0, c.Subworld, c.Thumbnail1}, nil) // course data where a thumbnail belongs

	cases := map[string][]byte{
		"empty":              nil,
		"html error page":    []byte("<?xml version=\"1.0\"?><Error><Code>AccessDenied</Code></Error>"),
		"truncated":          good[:len(good)/2],
		"extra chunk":        append(append([]byte{}, good...), c.Thumbnail1...),
		"leading junk":       append([]byte("HDR"), good...),
		"thumbnails swapped": swapped,
		"one chunk only":     c.Course,
		"corrupted body":     corrupt(good),
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := levelimport.Split(in); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func corrupt(b []byte) []byte {
	out := append([]byte{}, b...)

	// * flip bytes inside the second chunk's bit streams (past its 16-byte header)
	c, _ := levelimport.Split(b)
	start := len(c.Thumbnail0) + 40

	for i := start; i < start+64; i++ {
		out[i] ^= 0xA5
	}

	return out
}

func TestImportWritesFileAndRowsReadableByServerQueries(t *testing.T) {
	db := openDB(t)
	dir := t.TempDir()
	bin := sample(t)

	if err := levelimport.Import(db, level(1829603), bin, levelimport.Options{CoursesDir: dir}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "1829603.bin"))
	if err != nil || !bytes.Equal(got, bin) {
		t.Fatalf("course file not stored byte for byte: %v", err)
	}

	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(m) != 0 {
		t.Fatalf("temp files left behind: %v", m)
	}

	// * through the server's own query functions
	info, nexErr := datastore_db.GetObjectInfoByDataID(1829603)
	if nexErr != nil {
		t.Fatal(nexErr.Message)
	}

	if string(info.Name) != "Mini castle destruction" || uint64(info.OwnerID) != 1799947827 || int(info.Size) != len(bin) || len(info.MetaBinary) != 44 {
		t.Fatalf("info: %q owner %d size %d meta %d", info.Name, info.OwnerID, info.Size, len(info.MetaBinary))
	}

	if y := info.CreatedTime.Standard().Year(); y != 2015 {
		t.Fatalf("creation year %d", y)
	}

	if len(info.Ratings) != 6 || info.Ratings[2].Rating.TotalValue != 352 || info.Ratings[3].Rating.TotalValue != 573 || info.Ratings[4].Rating.TotalValue != 221 {
		t.Fatalf("ratings: %+v", info.Ratings)
	}

	// * rankings: stars in app 0, clear rate in percent in the difficulty app
	if v := count(t, db, `SELECT value FROM datastore.object_custom_rankings WHERE data_id = 1829603 AND application_id = 0`); v != 35 {
		t.Fatalf("stars ranking = %d", v)
	}

	if v := count(t, db, `SELECT value FROM datastore.object_custom_rankings WHERE data_id = 1829603 AND application_id = 300000000`); v != 61 {
		t.Fatalf("difficulty ranking = %d, want 61 (352/573 = 61.4%%)", v)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM datastore.buffer_queues WHERE data_id = 1829603 AND slot = 2`); n != 35 {
		t.Fatalf("%d star buffers", n)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM datastore.level_sources WHERE data_id = 1829603 AND url LIKE 'https://cdn.example/%' AND attempts = 573`); n != 1 {
		t.Fatal("source row missing")
	}
}

func TestReimportReplacesInsteadOfDuplicating(t *testing.T) {
	db := openDB(t)
	bin := sample(t)

	l := level(555)
	if err := levelimport.Import(db, l, bin, levelimport.Options{}); err != nil {
		t.Fatal(err)
	}

	l.Name, l.Stars, l.Clears, l.TotalAttempts, l.ClearRate = "renamed", 3, 1, 4, 0.25
	if err := levelimport.Import(db, l, bin, levelimport.Options{}); err != nil {
		t.Fatal(err)
	}

	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM datastore.objects WHERE data_id = 555`:                                            1,
		`SELECT COUNT(*) FROM datastore.object_ratings WHERE data_id = 555`:                                     6,
		`SELECT COUNT(*) FROM datastore.object_custom_rankings WHERE data_id = 555`:                             2,
		`SELECT COUNT(*) FROM datastore.buffer_queues WHERE data_id = 555`:                                      3,
		`SELECT COUNT(*) FROM datastore.level_sources WHERE data_id = 555`:                                      1,
		`SELECT value FROM datastore.object_custom_rankings WHERE data_id = 555 AND application_id = 300000000`: 25,
	} {
		if got := count(t, db, q); got != want {
			t.Fatalf("%s = %d, want %d", q, got, want)
		}
	}
}

func TestNeverOverwritesALocallyUploadedObject(t *testing.T) {
	db := openDB(t)

	if _, err := db.Exec(`INSERT INTO datastore.objects (data_id, upload_completed, owner, name, data_type) VALUES (777, 1, 42, 'my own level', 3)`); err != nil {
		t.Fatal(err)
	}

	err := levelimport.Import(db, level(777), sample(t), levelimport.Options{CoursesDir: t.TempDir()})
	if !errors.Is(err, levelimport.ErrLocalObject) {
		t.Fatalf("err = %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM datastore.objects WHERE data_id = 777`).Scan(&name); err != nil || name != "my own level" {
		t.Fatalf("local object changed: %q %v", name, err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM datastore.object_ratings WHERE data_id = 777`); n != 0 {
		t.Fatal("rows were added for a local object")
	}
}

func TestInvalidFilesLeaveNothingBehind(t *testing.T) {
	db := openDB(t)
	dir := t.TempDir()

	if err := levelimport.Import(db, level(888), []byte("<html>nope</html>"), levelimport.Options{CoursesDir: dir}); err == nil {
		t.Fatal("accepted")
	}

	if n := count(t, db, `SELECT COUNT(*) FROM datastore.objects WHERE data_id = 888`); n != 0 {
		t.Fatal("row written for an invalid file")
	}

	if m, _ := filepath.Glob(filepath.Join(dir, "*")); len(m) != 0 {
		t.Fatalf("files written for an invalid file: %v", m)
	}
}

func TestStarPlaceholdersAreCappedButTheRankingKeepsTheTrueCount(t *testing.T) {
	db := openDB(t)

	l := level(999)
	l.Stars = 123456

	if err := levelimport.Import(db, l, sample(t), levelimport.Options{MaxStarBuffers: 50}); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM datastore.buffer_queues WHERE data_id = 999`); n != 50 {
		t.Fatalf("%d placeholders", n)
	}

	if v := count(t, db, `SELECT value FROM datastore.object_custom_rankings WHERE data_id = 999 AND application_id = 0`); v != 123456 {
		t.Fatalf("ranking = %d", v)
	}
}

func TestLevelWithoutAttemptsHasNoDifficultyRanking(t *testing.T) {
	db := openDB(t)

	l := level(1001)
	l.Clears, l.Failures, l.TotalAttempts, l.ClearRate, l.Stars = 0, 0, 0, 0, 0

	if err := levelimport.Import(db, l, sample(t), levelimport.Options{}); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM datastore.object_custom_rankings WHERE data_id = 1001`); n != 0 {
		t.Fatalf("%d ranking rows for a level nobody played", n)
	}
}

func TestProfileCountryComesFromTheRegionCodeOfTheMiiData(t *testing.T) {
	db := openDB(t)

	for pid, c := range map[int64]struct {
		region uint32
		want   string
	}{101: {0, `["1"]`}, 102: {1, `["49"]`}, 103: {7, `["78"]`}, 104: {11, `["98"]`}} {
		b := make([]byte, 140)
		copy(b, "BPFC")
		binary.BigEndian.PutUint32(b[136:], c.region)

		if _, err := db.Exec(`INSERT INTO datastore.objects (upload_completed, owner, size, name, data_type, meta_binary, permission, permission_recipients,
				delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id, extra_data, creation_date, update_date)
			VALUES (1, $1, 0, 'maker', 1, $2, 0, '[]', 3, '[]', 256, 0, 0, '["1"]', 0, '["WUP"]', $3, $3)`, pid, b, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	if err := levelimport.FixProfileCountries(db); err != nil {
		t.Fatal(err)
	}

	for pid, want := range map[int64]string{101: `["1"]`, 102: `["49"]`, 103: `["78"]`, 104: `["98"]`} {
		var tags string
		if err := db.QueryRow(`SELECT tags FROM datastore.objects WHERE owner = $1 AND data_type = 1`, pid).Scan(&tags); err != nil || tags != want {
			t.Fatalf("owner %d: tags %q (%v), want %s", pid, tags, err, want)
		}
	}
}
