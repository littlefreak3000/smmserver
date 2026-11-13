package pool_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PretendoNetwork/nex-go/v2/types"
	datastore_types "github.com/PretendoNetwork/nex-protocols-go/v2/datastore/types"
	"github.com/PretendoNetwork/super-mario-maker/database"
	datastore_db "github.com/PretendoNetwork/super-mario-maker/database/datastore"
	"github.com/PretendoNetwork/super-mario-maker/levelimport"
)

// An archive level gets its world record and first clear from the archive API the first time the game asks, once.
func TestEnsureRecordFetchesTheArchivedRecordOnce(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	lvl := f.levels[10] // 1010
	lvl.WorldRecordMs, lvl.WorldRecordPID, lvl.WorldRecordHolderNNID = 11360, 1761895654, "Ohm7101"
	lvl.FirstClearPID, lvl.FirstClearNNID = 1786067547, "krausm"
	lvl.WorldRecordCreated = time.Date(2015, 12, 24, 16, 9, 33, 0, time.UTC)
	lvl.WorldRecordUpdated = time.Date(2017, 3, 29, 8, 30, 57, 0, time.UTC)

	// a plain import of the level, as the pool does it: no record yet
	plain := *lvl
	plain.WorldRecordMs, plain.WorldRecordPID, plain.FirstClearPID = 0, 0, 0

	if err := levelimport.Import(db, plain, f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	ok, err := p.EnsureRecord(context.Background(), 1010)
	if err != nil || !ok {
		t.Fatalf("EnsureRecord = %v, %v", ok, err)
	}

	var first, best, score int64

	var created, updated time.Time

	if err := db.QueryRow(`SELECT first_pid, best_pid, best_score, creation_date, update_date FROM datastore.course_records WHERE data_id = 1010 AND slot = 0`).
		Scan(&first, &best, &score, &created, &updated); err != nil {
		t.Fatal(err)
	}

	if first != 1786067547 || best != 1761895654 || score != 11360 || !created.Equal(lvl.WorldRecordCreated) || !updated.Equal(lvl.WorldRecordUpdated) {
		t.Fatalf("record = first %d best %d score %d created %v updated %v", first, best, score, created, updated)
	}

	calls := f.levelCalls

	if ok, err := p.EnsureRecord(context.Background(), 1010); !ok || err != nil || f.levelCalls != calls {
		t.Fatalf("a known record must not be looked up again (ok=%v err=%v calls %d->%d)", ok, err, calls, f.levelCalls)
	}
}

// A level nobody has cleared has no record; that is remembered, so the archive is not asked again.
func TestEnsureRecordRemembersLevelsWithoutARecord(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	if err := levelimport.Import(db, *f.levels[11], f.good, p.Import); err != nil { // 1011: no record data
		t.Fatal(err)
	}

	if ok, err := p.EnsureRecord(context.Background(), 1011); ok || err != nil {
		t.Fatalf("EnsureRecord = %v, %v", ok, err)
	}

	calls := f.levelCalls

	if ok, _ := p.EnsureRecord(context.Background(), 1011); ok || f.levelCalls != calls {
		t.Fatalf("the missing record must be remembered (calls %d->%d)", calls, f.levelCalls)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datastore.course_records WHERE data_id = 1011`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("records for 1011: %d (%v)", n, err)
	}
}

// A failed lookup (network) is not remembered, and levels players uploaded here are never looked up.
func TestEnsureRecordErrorsAreNotRemembered(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	if err := levelimport.Import(db, *f.levels[12], f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	f.levelErr = errors.New("api down")

	if _, err := p.EnsureRecord(context.Background(), 1012); err == nil {
		t.Fatal("expected the lookup error")
	}

	f.levelErr = nil
	f.levels[12].WorldRecordMs, f.levels[12].WorldRecordPID = 9000, 77

	if ok, err := p.EnsureRecord(context.Background(), 1012); !ok || err != nil {
		t.Fatalf("second try = %v, %v", ok, err)
	}

	calls := f.levelCalls

	if ok, _ := p.EnsureRecord(context.Background(), 424242); ok || f.levelCalls != calls {
		t.Fatalf("an unknown/local object must not be looked up (calls %d->%d)", calls, f.levelCalls)
	}
}

// A row written by a local clear before the archive was asked (that player was wrongly the "first clear") is merged
// with the archived record: the archived first clearer wins, the better time stays the record.
func TestEnsureRecordMergesWithAnEarlierLocalClear(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	if err := levelimport.Import(db, *f.levels[13], f.good, p.Import); err != nil { // 1013
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO datastore.course_records (data_id, slot, first_pid, best_pid, best_score, creation_date, update_date)
		VALUES (1013, 0, 4242, 4242, 30000, '2026-11-11 00:00:00', '2026-11-11 00:00:00')`); err != nil {
		t.Fatal(err)
	}

	f.levels[13].WorldRecordMs, f.levels[13].WorldRecordPID, f.levels[13].FirstClearPID = 20000, 111, 222

	if ok, err := p.EnsureRecord(context.Background(), 1013); !ok || err != nil {
		t.Fatalf("EnsureRecord = %v, %v", ok, err)
	}

	var first, best, score int64
	if err := db.QueryRow(`SELECT first_pid, best_pid, best_score FROM datastore.course_records WHERE data_id = 1013`).Scan(&first, &best, &score); err != nil {
		t.Fatal(err)
	}

	if first != 222 || best != 111 || score != 20000 {
		t.Fatalf("merged record = first %d best %d score %d (archived first clear and the better time expected)", first, best, score)
	}
}

// miiBlob is a stand-in for the 140-byte "BPFC" Mii data of the API.
func miiBlob(tag byte) []byte {
	b := make([]byte, 140)
	copy(b, "BPFC")
	b[139] = tag

	return b
}

// The game asks for a maker's profile object (Mii, name) next to a level, a record and a clear. For people the archive
// knows - creators and record holders - the profile is made from the per-level answer when the game asks for it.
func TestEnsureProfileMakesTheProfilesOfCreatorsAndRecordHolders(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	lvl := f.levels[30] // 1030, created by 5030
	lvl.Creator, lvl.CreatorMii = "MakerMan", miiBlob(1)
	lvl.WorldRecordMs, lvl.WorldRecordPID, lvl.WorldRecordHolderNNID, lvl.WorldRecordMii = 8000, 7001, "SpeedyOne", miiBlob(2)
	lvl.FirstClearPID, lvl.FirstClearNNID, lvl.FirstClearMii = 7002, "FirstFinisher", miiBlob(3)

	plain := *lvl
	plain.Creator, plain.CreatorMii = "", nil // as the random-level answer and the index have it: no people
	plain.WorldRecordMs, plain.WorldRecordPID, plain.FirstClearPID = 0, 0, 0

	if err := levelimport.Import(db, plain, f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	profile := func(pid int64) (name string, mii []byte, ok bool) {
		err := db.QueryRow(`SELECT name, meta_binary FROM datastore.objects WHERE owner = $1 AND persistence_slot_id = 0 AND data_type = 1 AND upload_completed = 1`, pid).Scan(&name, &mii)

		return name, mii, err == nil
	}

	if _, _, ok := profile(5030); ok {
		t.Fatal("the creator's profile must not exist before it is asked for")
	}

	// the game asks for the creator's profile: it is made from one of their levels
	if ok, err := p.EnsureProfile(context.Background(), 5030); !ok || err != nil {
		t.Fatalf("EnsureProfile(creator) = %v, %v", ok, err)
	}

	name, mii, ok := profile(5030)
	if !ok || name != "MakerMan" || len(mii) != 140 || string(mii[:4]) != "BPFC" || mii[139] != 1 {
		t.Fatalf("creator profile = %q %d bytes ok=%v", name, len(mii), ok)
	}

	// the same answer also made the record holder's and the first clearer's profiles: no second lookup is needed
	calls := f.levelCalls

	for pid, want := range map[int64]string{7001: "SpeedyOne", 7002: "FirstFinisher"} {
		if n, m, ok := profile(pid); !ok || n != want || len(m) != 140 {
			t.Fatalf("profile of %d = %q %d bytes ok=%v", pid, n, len(m), ok)
		}
	}

	if f.levelCalls != calls {
		t.Fatal("the profiles of the record holders must come from the same lookup")
	}

	// the record came with it
	if has, checked, _ := levelimport.RecordState(db, 1030); !has || !checked {
		t.Fatalf("record state has=%v checked=%v", has, checked)
	}

	// someone the archive knows nothing about is not found, and not asked about again
	if ok, err := p.EnsureProfile(context.Background(), 99999); ok || err != nil {
		t.Fatalf("EnsureProfile(stranger) = %v, %v", ok, err)
	}

	calls = f.levelCalls

	if ok, _ := p.EnsureProfile(context.Background(), 99999); ok || f.levelCalls != calls {
		t.Fatal("a stranger must be remembered")
	}
}

// A profile that exists already (a real player's) is never replaced by the archived one.
func TestImportKeepsAnExistingProfile(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	if _, err := db.Exec(`INSERT INTO datastore.objects (upload_completed, owner, size, name, data_type, meta_binary, permission, delete_permission, flag, period, refer_data_id, persistence_slot_id)
		VALUES (1, 5040, 0, 'RealPlayer', 1, x'0102', 0, 3, 256, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}

	lvl := *f.levels[40] // creator 5040
	lvl.Creator, lvl.CreatorMii = "ArchiveName", miiBlob(9)
	lvl.WorldRecordMs, lvl.WorldRecordPID = 5000, 8000 // a record, so the people are stored with the level

	if err := levelimport.Import(db, lvl, f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	var name string

	var n int
	if err := db.QueryRow(`SELECT name, (SELECT COUNT(*) FROM datastore.objects WHERE owner = 5040 AND data_type = 1) FROM datastore.objects WHERE owner = 5040 AND data_type = 1`).Scan(&name, &n); err != nil || name != "RealPlayer" || n != 1 {
		t.Fatalf("profile = %q, %d of them (%v)", name, n, err)
	}
}

// End to end for the lookup the game makes (GetMeta by owner and persistence slot): an unknown creator is made on the
// spot and answered with their name and Mii; a stranger is still "not found".
func TestProfileLookupByOwnerCreatesTheArchivedProfile(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	lvl := f.levels[50] // creator 5050
	lvl.Creator, lvl.CreatorMii = "PersistedMaker", miiBlob(5)

	if err := levelimport.Import(db, *lvl, f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	database.OnMissingProfile = func(pid uint32) bool {
		ok, err := p.EnsureProfile(context.Background(), int64(pid))

		return ok && err == nil
	}

	t.Cleanup(func() { database.OnMissingProfile = nil })

	target := datastore_types.NewDataStorePersistenceTarget()
	target.OwnerID = types.NewPID(5050)
	target.PersistenceSlotID = 0

	meta, e := datastore_db.GetObjectInfoByPersistenceTargetWithPassword(target, 0)
	if e != nil {
		t.Fatalf("profile lookup: %v", e.Message)
	}

	if string(meta.Name) != "PersistedMaker" || len(meta.MetaBinary) != 140 || string(meta.MetaBinary[:4]) != "BPFC" || meta.DataType != 1 || uint64(meta.OwnerID) != 5050 {
		t.Fatalf("profile = name %q, %d bytes, type %d, owner %d", meta.Name, len(meta.MetaBinary), meta.DataType, meta.OwnerID)
	}

	target.OwnerID = types.NewPID(123456)
	if _, e := datastore_db.GetObjectInfoByPersistenceTargetWithPassword(target, 0); e == nil {
		t.Fatal("a player the archive does not know must still be not found")
	}
}

// A level imported from the random-level answer already carries its creator (name and Mii): the profile exists right
// away, with the nine rating slots the game gives its own profile, so no lookup is needed when a list loads.
func TestImportCreatesTheCreatorProfileWithRatingSlots(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	lvl := *f.levels[60] // creator 5060, no record
	lvl.Creator, lvl.CreatorMii = "AtImport", miiBlob(7)

	if err := levelimport.Import(db, lvl, f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	var id int64

	var name string
	if err := db.QueryRow(`SELECT data_id, name FROM datastore.objects WHERE owner = 5060 AND data_type = 1 AND persistence_slot_id = 0`).Scan(&id, &name); err != nil || name != "AtImport" {
		t.Fatalf("creator profile: %q (%v)", name, err)
	}

	var slots int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT slot) FROM datastore.object_ratings WHERE data_id = $1 AND slot BETWEEN 0 AND 8`, id).Scan(&slots); err != nil || slots != 9 {
		t.Fatalf("rating slots of the profile: %d (%v)", slots, err)
	}

	// importing the creator's next level does not make a second profile
	next := *f.levels[61]
	next.CreatorID, next.Creator, next.CreatorMii = 5060, "AtImport", miiBlob(7)

	if err := levelimport.Import(db, next, f.good, p.Import); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datastore.objects WHERE owner = 5060 AND data_type = 1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("profiles of the creator: %d (%v)", n, err)
	}
}

// Levels that are in the catalog without a creator profile get theirs from the background backfill; profiles built
// before the rating slots existed are completed.
func TestBackfillCreatesMissingCreatorProfiles(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)

	for _, i := range []int{70, 71, 72} { // creators 5070, 5071, 5072
		plain := *f.levels[i]
		plain.Creator, plain.CreatorMii = "", nil

		if err := levelimport.Import(db, plain, f.good, p.Import); err != nil {
			t.Fatal(err)
		}

		f.levels[i].Creator, f.levels[i].CreatorMii = fmt.Sprintf("Creator%d", 5000+i), miiBlob(byte(i))
	}

	calls := f.levelCalls

	p.BackfillProfiles(context.Background())

	if f.levelCalls != calls+3 {
		t.Fatalf("backfill made %d API calls, want 3", f.levelCalls-calls)
	}

	for _, i := range []int{70, 71, 72} {
		var name string
		if err := db.QueryRow(`SELECT name FROM datastore.objects WHERE owner = $1 AND data_type = 1 AND persistence_slot_id = 0`, 5000+i).Scan(&name); err != nil || name != fmt.Sprintf("Creator%d", 5000+i) {
			t.Fatalf("creator %d profile: %q (%v)", 5000+i, name, err)
		}
	}

	// a second run has nothing to do
	calls = f.levelCalls
	p.BackfillProfiles(context.Background())

	if f.levelCalls != calls {
		t.Fatal("a finished backfill must not ask the API again")
	}

	// old profiles without rating slots are completed
	if _, err := db.Exec(`DELETE FROM datastore.object_ratings WHERE data_id IN (SELECT data_id FROM datastore.objects WHERE data_type = 1)`); err != nil {
		t.Fatal(err)
	}

	if err := levelimport.AddProfileRatings(db); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM datastore.object_ratings r JOIN datastore.objects o ON o.data_id = r.data_id WHERE o.data_type = 1`).Scan(&n); err != nil || n != 27 {
		t.Fatalf("rating rows after completing 3 profiles: %d (%v)", n, err)
	}
}
