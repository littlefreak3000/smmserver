package pool_test

import (
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	"github.com/PretendoNetwork/super-mario-maker/globals"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/pool"
)

const fileSize = 1000

// evictEnv makes a catalog with n downloaded levels (ids 1..n, fetched one minute apart, oldest first), each with a file of
// 1000 bytes and rows in every table that belongs to a level.
func evictEnv(t *testing.T, n int) (*pool.Pool, *sql.DB, string) {
	t.Helper()

	globals.Logger = plogger.NewLogger()

	db, err := database.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	database.DB = db

	t.Cleanup(func() { db.Close() })

	dir := filepath.Join(t.TempDir(), "courses")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 1; i <= n; i++ {
		id := int64(i)
		when := start.Add(time.Duration(i) * time.Minute)

		exec(t, db, `INSERT INTO datastore.objects (upload_completed, data_id, owner, size, name, data_type, meta_binary, permission, permission_recipients,
				delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id, extra_data, creation_date, update_date)
			VALUES (1, $1, 500, 10, 'level', 3, x'00', 0, '[]', 3, '[]', 0, 0, 0, '[]', 65535, '[]', $2, $2)`, id, when)
		exec(t, db, `INSERT INTO datastore.object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value, range_min, range_max, period_hour, period_duration, total_value, count)
			VALUES ($1, 0, 0, 0, 0, 0, 0, 100, 0, 0, 1, 1)`, id)
		exec(t, db, `INSERT INTO datastore.object_custom_rankings (data_id, application_id, value) VALUES ($1, 0, 1)`, id)
		exec(t, db, `INSERT INTO datastore.level_sources (data_id, url, clear_rate, attempts, stars, fetched) VALUES ($1, 'u', 0.5, 10, 1, $2)`, id, when)
		exec(t, db, `INSERT INTO datastore.level_lookup (data_id, checked) VALUES ($1, $2)`, id, when)

		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".bin"), make([]byte, fileSize), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return &pool.Pool{DB: db, Import: levelimport.Options{CoursesDir: dir}}, db, dir
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()

	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func exists(dir string, id int) bool {
	_, err := os.Stat(filepath.Join(dir, strconv.Itoa(id)+".bin"))

	return err == nil
}

func rows(t *testing.T, db *sql.DB, table string, id int) int {
	t.Helper()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datastore.`+table+` WHERE data_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestEvictRemovesTheOldestLevelsAndEverythingOfTheirs(t *testing.T) {
	p, db, dir := evictEnv(t, 20) // 20 000 bytes

	removed, freed, err := p.Evict(10 * fileSize) // limit 10 000: evict down to 9 000
	if err != nil {
		t.Fatal(err)
	}

	if removed != 11 || freed != 11*fileSize {
		t.Fatalf("removed %d levels, %d bytes; want 11 and 11000", removed, freed)
	}

	for id := 1; id <= 11; id++ {
		if exists(dir, id) {
			t.Fatalf("file of level %d is still there", id)
		}

		for _, table := range []string{"objects", "object_ratings", "object_custom_rankings", "level_sources", "level_lookup"} {
			if n := rows(t, db, table, id); n != 0 {
				t.Fatalf("level %d still has %d rows in %s", id, n, table)
			}
		}
	}

	for id := 12; id <= 20; id++ {
		if !exists(dir, id) || rows(t, db, "objects", id) != 1 || rows(t, db, "level_sources", id) != 1 {
			t.Fatalf("level %d should have been kept", id)
		}
	}

	if size, _ := p.CourseFilesSize(); size != 9*fileSize {
		t.Fatalf("files take %d bytes, want 9000", size)
	}
}

func TestEvictDoesNothingWhenOffOrUnderTheLimit(t *testing.T) {
	p, _, dir := evictEnv(t, 5)

	for _, limit := range []int64{0, -1, 5 * fileSize, 100 * fileSize} {
		if removed, _, err := p.Evict(limit); err != nil || removed != 0 {
			t.Fatalf("limit %d: removed %d (%v)", limit, removed, err)
		}
	}

	for id := 1; id <= 5; id++ {
		if !exists(dir, id) {
			t.Fatalf("level %d was removed", id)
		}
	}
}

func TestEvictKeepsLevelsALocalPlayerStarredOrMade(t *testing.T) {
	p, db, dir := evictEnv(t, 10)

	// * a local account (pid 700) with a profile whose star list holds level 1 (the oldest), and a level made by that account
	exec(t, db, `INSERT INTO datastore.accounts (nnid, pid, created) VALUES ('me', 700, $1)`, time.Now())
	exec(t, db, `INSERT INTO datastore.objects (upload_completed, data_id, owner, size, name, data_type, meta_binary, permission, permission_recipients,
			delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id, extra_data, creation_date, update_date)
		VALUES (1, 9000, 700, 0, 'me', 1, x'00', 0, '[]', 3, '[]', 256, 0, 0, '["1"]', 0, '[]', $1, $1)`, time.Now())

	star := make([]byte, 8)
	binary.LittleEndian.PutUint32(star, 1)
	exec(t, db, `INSERT INTO datastore.buffer_queues (data_id, slot, creation_date, buffer) VALUES (9000, 0, $1, $2)`, time.Now(), star)
	exec(t, db, `UPDATE datastore.objects SET owner = 700 WHERE data_id = 2`) // level 2 was made by the local account

	if _, _, err := p.Evict(5 * fileSize); err != nil { // evict down to 4 500: only 4 files fit
		t.Fatal(err)
	}

	if !exists(dir, 1) || !exists(dir, 2) {
		t.Fatal("a starred level and a level of the local account must be kept")
	}

	if exists(dir, 3) || exists(dir, 4) {
		t.Fatal("the next oldest levels should have gone")
	}

	if size, _ := p.CourseFilesSize(); size > 5*fileSize {
		t.Fatalf("files take %d bytes", size)
	}
}
