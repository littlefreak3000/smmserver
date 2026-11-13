package datastore_smm_db_test

// These tests exercise the real database functions against a fresh SQLite catalog. They exist
// because the queries were written for Postgres: they check placeholder binding, schema-qualified
// upserts, arrays-as-JSON, json_each ordering, RETURNING, AUTOINCREMENT, time round-trips and
// concurrent writers.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_smm_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/super-mario-maker/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/plogger-go"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	datastore_smm_db "github.com/PretendoNetwork/super-mario-maker/database/datastore/super-mario-maker"
	"github.com/PretendoNetwork/super-mario-maker/globals"
)

var dbPath string

func TestMain(m *testing.M) {
	globals.Logger = plogger.NewLogger()

	dir, err := os.MkdirTemp("", "smm-sqlite-test")
	if err != nil {
		panic(err)
	}

	dbPath = filepath.Join(dir, "catalog.sqlite")

	database.DB, err = database.Open(dbPath)
	if err != nil {
		panic(err)
	}

	code := m.Run()
	database.DB.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

func seedObject(t *testing.T, id uint64, owner uint32, name string, dataType int, created time.Time, meta []byte) {
	t.Helper()

	_, err := database.DB.Exec(`INSERT INTO datastore.objects (
		data_id, upload_completed, owner, size, name, data_type, meta_binary, permission,
		permission_recipients, delete_permission, delete_permission_recipients, flag, period,
		refer_data_id, tags, persistence_slot_id, extra_data, creation_date, update_date
	) VALUES ($1, TRUE, $2, 1234, $3, $4, $5, 0, $6, 3, $6, 0, 0, 0, $7, 0, $6, $8, $8)`,
		id, owner, name, dataType, meta, database.Array([]uint32{}), database.Array([]string{"a", "b"}), created)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSequenceStartsAt940000AndFollowsExplicitIDs(t *testing.T) {
	insert := func(explicit any) uint64 {
		var id uint64
		var err error
		if explicit == nil {
			err = database.DB.QueryRow(`INSERT INTO datastore.objects (owner) VALUES (1) RETURNING data_id`).Scan(&id)
		} else {
			err = database.DB.QueryRow(`INSERT INTO datastore.objects (data_id, owner) VALUES ($1, 1) RETURNING data_id`, explicit).Scan(&id)
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	if got := insert(nil); got != 940000 {
		t.Fatalf("first generated id = %d, want 940000", got)
	}
	if got := insert(nil); got != 940001 {
		t.Fatalf("second generated id = %d, want 940001", got)
	}
	if got := insert(uint64(5_000_000)); got != 5_000_000 {
		t.Fatalf("explicit id = %d", got)
	}
	if got := insert(nil); got != 5_000_001 {
		t.Fatalf("id after explicit = %d, want 5000001 (no collisions with imported ids)", got)
	}
	// * importing an id BELOW the counter must not disturb it
	insert(uint64(942697))
	if got := insert(nil); got != 5_000_002 {
		t.Fatalf("id after low explicit = %d", got)
	}
}

func TestPreparePostRoundTripsArraysMetaBinaryAndTimes(t *testing.T) {
	param := datastore_types.NewDataStorePreparePostParam()
	param.Size = 777
	param.Name = "round trip"
	param.DataType = 3
	param.MetaBinary = types.NewQBuffer([]byte{1, 2, 3, 0, 255})
	param.Permission.RecipientIDs = types.List[types.PID]{types.NewPID(11), types.NewPID(4294967295)}
	param.DelPermission.RecipientIDs = types.List[types.PID]{types.NewPID(7)}
	param.Tags = types.List[types.String]{"tag1", "tag with \"quotes\" and ünïcode"}
	param.ExtraData = types.List[types.String]{"x", "y"}

	id, nexErr := datastore_db.InitializeObjectByPreparePostParam(types.NewPID(42), param)
	if nexErr != nil {
		t.Fatal(nexErr.Message)
	}
	if id < 940000 {
		t.Fatalf("generated id %d below 940000", id)
	}

	// * not visible until upload completes
	if _, e := datastore_db.GetObjectInfoByDataID(types.NewUInt64(id)); e == nil {
		t.Fatal("object visible before upload_completed")
	}
	if e := datastore_db.UpdateObjectUploadCompletedByDataID(types.NewUInt64(id), true); e != nil {
		t.Fatal(e.Message)
	}

	info, e := datastore_db.GetObjectInfoByDataID(types.NewUInt64(id))
	if e != nil {
		t.Fatal(e.Message)
	}

	if info.Name != "round trip" || info.Size != 777 || info.OwnerID != types.NewPID(42) {
		t.Fatalf("basic fields wrong: %+v", info)
	}
	if string(info.MetaBinary) != string([]byte{1, 2, 3, 0, 255}) {
		t.Fatalf("meta_binary = %v", []byte(info.MetaBinary))
	}
	if len(info.Permission.RecipientIDs) != 2 || info.Permission.RecipientIDs[1] != types.NewPID(4294967295) {
		t.Fatalf("recipients = %v", info.Permission.RecipientIDs)
	}
	if len(info.DelPermission.RecipientIDs) != 1 || info.DelPermission.RecipientIDs[0] != types.NewPID(7) {
		t.Fatalf("del recipients = %v", info.DelPermission.RecipientIDs)
	}
	if len(info.Tags) != 2 || info.Tags[1] != "tag with \"quotes\" and ünïcode" {
		t.Fatalf("tags = %v", info.Tags)
	}
	if time.Since(info.CreatedTime.Standard()) > time.Minute || info.CreatedTime.Standard().After(time.Now().Add(time.Minute)) {
		t.Fatalf("created time = %v", info.CreatedTime.Standard())
	}
}

func TestCustomRankingUpsertAccumulates(t *testing.T) {
	seedObject(t, 100, 1, "ranked", 3, time.Now().UTC(), nil)
	for i := 0; i < 3; i++ {
		if e := datastore_smm_db.InsertOrUpdateCustomRanking(100, 2400, 5); e != nil {
			t.Fatal(e.Message)
		}
	}

	var v int
	if err := database.DB.QueryRow(`SELECT value FROM datastore.object_custom_rankings WHERE data_id=100 AND application_id=2400`).Scan(&v); err != nil || v != 15 {
		t.Fatalf("value = %d, err = %v (want 15)", v, err)
	}
}

func TestCourseRecordKeepsFirstAndBest(t *testing.T) {
	seedObject(t, 200, 1, "record", 3, time.Now().UTC(), nil)

	steps := []struct {
		pid, score             uint32
		wantBestPID, wantFirst uint32
		wantBestScore          int32
	}{
		{10, 500, 10, 10, 500}, // first clear
		{20, 300, 20, 10, 300}, // faster (lower) wins best, first stays
		{30, 400, 20, 10, 300}, // slower does not replace best
	}

	for i, s := range steps {
		if e := datastore_smm_db.InsertOrUpdateCourseRecord(200, 0, types.NewPID(uint64(s.pid)), types.NewInt32(int32(s.score))); e != nil {
			t.Fatalf("step %d: %s", i, e.Message)
		}
		rec, e := datastore_smm_db.GetCourseRecordByDataIDAndSlot(200, 0)
		if e != nil {
			t.Fatalf("step %d: %s", i, e.Message)
		}
		if uint32(rec.BestPID) != s.wantBestPID || uint32(rec.FirstPID) != s.wantFirst || int32(rec.BestScore) != s.wantBestScore {
			t.Fatalf("step %d: first=%d best=%d score=%d, want first=%d best=%d score=%d", i, rec.FirstPID, rec.BestPID, rec.BestScore, s.wantFirst, s.wantBestPID, s.wantBestScore)
		}
	}
}

func TestCustomRankingsByDataIDsKeepsDuplicatesAndOrder(t *testing.T) {
	seedObject(t, 301, 1, "A", 3, time.Now().UTC(), nil)
	seedObject(t, 302, 1, "B", 3, time.Now().UTC(), nil)
	seedObject(t, 303, 1, "C-no-ranking", 3, time.Now().UTC(), nil)
	for id, score := range map[uint64]uint32{301: 10, 302: 20} {
		if e := datastore_smm_db.InsertOrUpdateCustomRanking(types.NewUInt64(id), 3600, types.NewUInt32(score)); e != nil {
			t.Fatal(e.Message)
		}
	}

	ids := types.List[types.UInt64]{302, 301, 302, 303, 999999, 301}
	got := datastore_smm_db.GetCustomRankingsByDataIDs(3600, ids)

	var order []string
	for _, r := range got {
		order = append(order, fmt.Sprintf("%d:%d", r.MetaInfo.DataID, r.Score))
	}

	want := []string{"302:20", "301:10", "302:20", "301:10"} // unknown and unranked ids omitted, duplicates kept, input order kept
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", order, want)
	}
}

func TestGetCoursesNewestRankedAndRandomRange(t *testing.T) {
	// * far in the future so these are the newest objects in the shared test catalog
	base := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)
	// * ids 401..405, created in id order; stars in application 2400
	for i := uint64(0); i < 5; i++ {
		seedObject(t, 401+i, 1, fmt.Sprintf("course %d", 401+i), 3, base.Add(time.Duration(i)*time.Hour), nil)
	}
	stars := map[uint64]uint32{401: 50, 402: 5, 403: 30, 404: 0, 405: 30}
	for id, s := range stars {
		if s > 0 {
			if e := datastore_smm_db.InsertOrUpdateCustomRanking(types.NewUInt64(id), 2401, types.NewUInt32(s)); e != nil {
				t.Fatal(e.Message)
			}
		}
	}

	ids := func(l types.List[datastore_smm_types.DataStoreCustomRankingResult]) []uint64 {
		var out []uint64
		for _, r := range l {
			out = append(out, uint64(r.MetaInfo.DataID))
		}
		return out
	}

	// * non-course objects newer than every course must never show up in the newest list
	seedObject(t, 410, 1, "event metadata", 50, base.Add(100*time.Hour), nil)
	seedObject(t, 411, 1, "maker profile", 1, base.Add(101*time.Hour), nil)

	// * newest first (creation_date DESC): 405, 404, 403, ...; with offset/limit
	newest, e := datastore_smm_db.GetCourses(2401, true, 1, 3)
	if e != nil {
		t.Fatal(e.Message)
	}
	if fmt.Sprint(ids(newest)) != "[404 403 402]" {
		t.Fatalf("newest page = %v", ids(newest))
	}
	if newest[0].Order != 2 || newest[2].Order != 4 {
		t.Fatalf("order numbers = %d..%d, want 2..4", newest[0].Order, newest[2].Order)
	}

	// * ranked: value DESC then data_id DESC: 401(50), 405(30), 403(30), 402(5)
	ranked, e := datastore_smm_db.GetCourses(2401, false, 0, 10)
	if e != nil {
		t.Fatal(e.Message)
	}
	if fmt.Sprint(ids(ranked)) != "[401 405 403 402]" {
		t.Fatalf("ranked = %v", ids(ranked))
	}
	if ranked[0].Score != 50 {
		t.Fatalf("score = %d", ranked[0].Score)
	}

	// * random within [5, 30] (inclusive): only 402, 403, 405, never 401 (50) or 404 (none)
	for i := 0; i < 20; i++ {
		rnd, e := datastore_smm_db.GetRandomCoursesInScoreRange(2401, 5, 30, 10)
		if e != nil {
			t.Fatal(e.Message)
		}
		if len(rnd) != 3 {
			t.Fatalf("random range returned %v", ids(rnd))
		}
		for _, r := range rnd {
			if r.MetaInfo.DataID == 401 || r.MetaInfo.DataID == 404 {
				t.Fatalf("random range leaked %d", r.MetaInfo.DataID)
			}
		}
	}

	// * deleted / unfinished / under review objects never listed
	database.DB.Exec(`UPDATE datastore.objects SET deleted=TRUE WHERE data_id=405`)
	database.DB.Exec(`UPDATE datastore.objects SET upload_completed=FALSE WHERE data_id=404`)
	database.DB.Exec(`UPDATE datastore.objects SET under_review=TRUE WHERE data_id=403`)
	ranked, _ = datastore_smm_db.GetCourses(2401, false, 0, 10)
	if fmt.Sprint(ids(ranked)) != "[401 402]" {
		t.Fatalf("ranked after hiding = %v", ids(ranked))
	}
}

func TestRateObjectReturning(t *testing.T) {
	seedObject(t, 500, 1, "rated", 3, time.Now().UTC(), nil)
	if _, err := database.DB.Exec(`INSERT INTO datastore.object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value, range_min, range_max, period_hour, period_duration, total_value, count) VALUES (500, 0, 0, 0, 0, 10, 0, 1000, 0, 0, 10, 0)`); err != nil {
		t.Fatal(err)
	}

	var last datastore_types.DataStoreRatingInfo
	for i := 0; i < 3; i++ {
		r, e := datastore_db.RateObjectWithPassword(500, 0, 5, 0)
		if e != nil {
			t.Fatal(e.Message)
		}
		last = r
	}
	if last.TotalValue != 25 || last.Count != 3 || last.InitialValue != 10 {
		t.Fatalf("rating = total %d count %d initial %d", last.TotalValue, last.Count, last.InitialValue)
	}

	if _, e := datastore_db.RateObjectWithPassword(500, 200, 1, 0); e == nil {
		t.Fatal("rating a slot far out of range should fail")
	}

	// * an archived level has no row for a slot the game rates: it is created, not refused
	made, e := datastore_db.RateObjectWithPassword(500, 7, 1, 0)
	if e != nil || made.TotalValue != 1 || made.Count != 1 {
		t.Fatalf("rating a missing slot = %+v, %v", made, e)
	}

	again, e := datastore_db.RateObjectWithPassword(500, 7, 1, 0)
	if e != nil || again.TotalValue != 2 || again.Count != 2 {
		t.Fatalf("second rating of the created slot = %+v, %v", again, e)
	}

	ratings, e := datastore_db.GetObjectRatingsWithSlotByDataID(500)
	if e != nil || len(ratings) != 2 || ratings[0].Rating.TotalValue != 25 {
		t.Fatalf("ratings = %+v err = %v", ratings, e)
	}
}

func TestBufferQueuesDeduplicateAndKeepOrder(t *testing.T) {
	seedObject(t, 600, 1, "stars", 3, time.Now().UTC(), nil)
	for _, b := range [][]byte{{1}, {2}, {1}, {3}} { // {1} twice: the real server allows no duplicates
		if e := datastore_smm_db.InsertOrUpdateBufferQueueData(600, 2, types.NewQBuffer(b)); e != nil {
			t.Fatal(e.Message)
		}
		time.Sleep(2 * time.Millisecond)
	}

	got, e := datastore_smm_db.GetBufferQueuesByDataIDAndSlot(600, 2)
	if e != nil {
		t.Fatal(e.Message)
	}
	if len(got) != 3 {
		t.Fatalf("got %d buffers, want 3 (duplicate collapsed)", len(got))
	}

	other, _ := datastore_smm_db.GetBufferQueuesByDataIDAndSlot(600, 3)
	if len(other) != 0 {
		t.Fatalf("slot 3 has %d buffers", len(other))
	}
}

func TestUserCourseObjectIDsAndRandomCourses(t *testing.T) {
	seedObject(t, 700, 9000, "mine 1", 3, time.Now().UTC(), nil)
	seedObject(t, 701, 9000, "mine 2", 49, time.Now().UTC(), nil)
	seedObject(t, 702, 9000, "not a course (type 1)", 1, time.Now().UTC(), nil)
	seedObject(t, 703, 9001, "someone else", 3, time.Now().UTC(), nil)

	ids, e := datastore_smm_db.GetUserCourseObjectIDs(types.NewPID(9000))
	if e != nil {
		t.Fatal(e.Message)
	}
	if fmt.Sprint(ids) != "[700 701]" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	seedObject(t, 800, 1, "contended", 3, time.Now().UTC(), nil)

	var wg sync.WaitGroup
	const n = 40
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := datastore_smm_db.InsertOrUpdateCustomRanking(800, 5000, 1); e != nil {
				t.Error(e.Message)
			}
		}()
	}
	wg.Wait()

	var v int
	if err := database.DB.QueryRow(`SELECT value FROM datastore.object_custom_rankings WHERE data_id=800 AND application_id=5000`).Scan(&v); err != nil || v != n {
		t.Fatalf("value = %d err = %v, want %d", v, err, n)
	}
}

func TestCatalogSurvivesReopen(t *testing.T) {
	seedObject(t, 900, 1, "persisted", 3, time.Now().UTC(), []byte{9})
	database.DB.Close()

	var err error
	database.DB, err = database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	info, e := datastore_db.GetObjectInfoByDataID(900)
	if e != nil || info.Name != "persisted" {
		t.Fatalf("after reopen: %+v err=%v", info.Name, e)
	}

	var id uint64
	if err := database.DB.QueryRow(`INSERT INTO datastore.objects (owner) VALUES (1) RETURNING data_id`).Scan(&id); err != nil || id <= 5_000_002 {
		t.Fatalf("id after reopen = %d err=%v (sequence must continue, not restart)", id, err)
	}
}
