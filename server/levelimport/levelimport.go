// Package levelimport turns one archived level (metadata record + the original course file) into catalog rows.
//
// The course file is stored byte for byte as downloaded. meta_binary is built from it the way the SMMNEX
// template describes it (compressed size and CRC32 of each of the four ASH0 chunks); its three unknown fields
// keep the defaults used everywhere else in this project.
//
// What the archive's metadata cannot supply, and what is substituted:
//   - number of players who played ("user plays"): the attempt count is used
//   - who cleared first / who holds the record (only an NNID is known, not a PID): no course record is imported
//   - the individual players who gave stars: one placeholder entry per star, capped at MaxStarBuffers; the
//     full star count is kept in the star ranking
package levelimport

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/ash0"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

type Options struct {
	CoursesDir     string // where <id>.bin is written (empty = do not write the file)
	DataType       int    // objects.data_type for courses (default 3; UNVERIFIED, see README)
	StarsApp       uint32 // custom ranking that carries the star count (default 0)
	DifficultyApp  uint32 // custom ranking that carries the clear rate in percent (default 300000000; UNVERIFIED)
	MaxStarBuffers int    // placeholder star entries per level (default 500)
}

func (o *Options) defaults() {
	if o.DataType == 0 {
		o.DataType = 3
	}

	if o.DifficultyApp == 0 {
		o.DifficultyApp = 300000000
	}

	if o.MaxStarBuffers == 0 {
		o.MaxStarBuffers = 500
	}
}

// ErrLocalObject is returned instead of overwriting an object that players uploaded to this server.
var ErrLocalObject = errors.New("levelimport: an object with this ID was uploaded locally; not overwriting")

// Chunks are the four compressed ASH0 chunks of a course file, in file order.
type Chunks struct {
	Thumbnail0, Course, Subworld, Thumbnail1 []byte
}

// Split cuts a course file at its four "ASH0" markers and checks that every chunk really decodes to what it
// should be: two 0x15000-byte course blobs and two JPEG thumbnails.
func Split(bin []byte) (*Chunks, error) {
	var idx []int

	for off := 0; ; {
		i := bytes.Index(bin[off:], []byte("ASH0"))
		if i < 0 {
			break
		}

		idx = append(idx, off+i)
		off += i + 1
	}

	if len(idx) != 4 || idx[0] != 0 {
		return nil, fmt.Errorf("levelimport: expected 4 ASH0 chunks starting at offset 0, found %d (first at %v)", len(idx), idx)
	}

	idx = append(idx, len(bin))
	parts := make([][]byte, 4)

	for i := range parts {
		parts[i] = bin[idx[i]:idx[i+1]]

		raw, err := ash0.Decompress(parts[i])
		if err != nil {
			return nil, fmt.Errorf("levelimport: chunk %d: %w", i, err)
		}

		switch i {
		case 1, 2:
			if len(raw) != 0x15000 {
				return nil, fmt.Errorf("levelimport: chunk %d decodes to %d bytes, want %d", i, len(raw), 0x15000)
			}
		default:
			if len(raw) < 10 || raw[8] != 0xFF || raw[9] != 0xD8 {
				return nil, fmt.Errorf("levelimport: chunk %d is not a thumbnail", i)
			}
		}
	}

	return &Chunks{parts[0], parts[1], parts[2], parts[3]}, nil
}

// MetaBinary builds the 44-byte meta_binary (see tools/meta.mjs for the layout and its caveats).
func MetaBinary(c *Chunks) []byte {
	b := make([]byte, 0, 44)
	put := func(v uint32) { b = binary.BigEndian.AppendUint32(b, v) }

	put(1) // unk1 (UNVERIFIED default)
	put(0) // unk2 (UNVERIFIED default)

	order := [][]byte{c.Course, c.Subworld, c.Thumbnail0, c.Thumbnail1}
	for _, ch := range order {
		put(uint32(len(ch)))
	}

	put(1) // unk3 (UNVERIFIED default)

	for _, ch := range order {
		put(crc32.ChecksumIEEE(ch))
	}

	return b
}

// Import validates bin and stores the level, replacing an earlier import of the same ID. It refuses (ErrLocalObject)
// to touch an object that was not itself imported from the archive.
func Import(db *sql.DB, lvl upstream.Level, bin []byte, opt Options) error {
	opt.defaults()

	chunks, err := Split(bin)
	if err != nil {
		return fmt.Errorf("level %d: %w", lvl.LevelID, err)
	}

	if lvl.LevelID <= 0 {
		return errors.New("levelimport: level has no ID")
	}

	if err := writeFile(opt.CoursesDir, lvl.LevelID, bin); err != nil {
		return err
	}

	return write(db, lvl, len(bin), MetaBinary(chunks), true, false, opt)
}

// writeFile stores a course file as <dir>/<id>.bin (atomically). An empty dir means "do not write files".
func writeFile(dir string, id int64, bin []byte) error {
	if dir == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	target := filepath.Join(dir, strconv.FormatInt(id, 10)+".bin")

	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o644); err != nil {
		return err
	}

	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}

	return nil
}

// ErrNoStub is returned by Complete for a level the catalog does not know.
var ErrNoStub = errors.New("levelimport: the level is not in the catalog")

// ImportStub adds a level's rows without its file (size 0, empty meta_binary, fetched = NULL), so it can be listed
// before it is downloaded. Complete fills in the rest. A level that is already known (stub or complete) is left alone.
func ImportStub(db *sql.DB, lvl upstream.Level, opt Options) error {
	opt.defaults()

	if lvl.LevelID <= 0 {
		return errors.New("levelimport: level has no ID")
	}

	return write(db, lvl, 0, []byte{}, false, true, opt)
}

// RecordSlot is the course record slot the game reads for the world record.
const RecordSlot = 0

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertRecord stores the archived world record and first clear of a level. If a row exists already (a local player
// cleared the level before the archived record was looked up, and was wrongly the "first clear") the archived first
// clear takes its place and the better time of the two stays the record.
func insertRecord(x execer, id int64, lvl upstream.Level) error {
	created, updated := lvl.WorldRecordCreated, lvl.WorldRecordUpdated

	if created.IsZero() {
		created = lvl.UploadTime
	}

	if updated.IsZero() {
		updated = created
	}

	first := lvl.FirstClearPID
	if first == 0 {
		first = lvl.WorldRecordPID
	}

	_, err := x.Exec(`INSERT INTO datastore.course_records (data_id, slot, first_pid, best_pid, best_score, creation_date, update_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (data_id, slot) DO UPDATE SET
			first_pid = excluded.first_pid,
			best_pid = CASE WHEN datastore.course_records.best_score <= excluded.best_score THEN datastore.course_records.best_pid ELSE excluded.best_pid END,
			update_date = CASE WHEN datastore.course_records.best_score <= excluded.best_score THEN datastore.course_records.update_date ELSE excluded.update_date END,
			creation_date = excluded.creation_date,
			best_score = CASE WHEN datastore.course_records.best_score <= excluded.best_score THEN datastore.course_records.best_score ELSE excluded.best_score END`,
		id, RecordSlot, first, lvl.WorldRecordPID, lvl.WorldRecordMs, created, updated)

	return err
}

// NoPersistence is the persistence slot of an object that is not a player's persistent object (every course). Slot 0 is
// where a maker's profile lives: a level stored there would be mistaken for its creator's profile.
const NoPersistence = 65535

// ProfileSlot is the persistence slot of a maker's profile object.
const ProfileSlot = 0

// insertProfile creates the profile object of a maker (what the game asks for to show a Mii and a name next to a level,
// a record or a clear): data type 1, named after the NNID, with the Mii data as its meta binary, shaped like the profile
// object the game uploads for its own player. A profile that already exists (an archived one, or a real player's) stays.
func insertProfile(x execer, pid int64, nnid string, mii []byte, since time.Time) error {
	if pid == 0 || nnid == "" || len(mii) == 0 {
		return nil
	}

	_, err := x.Exec(`INSERT INTO datastore.objects (
			upload_completed, owner, size, name, data_type, meta_binary, permission, permission_recipients, delete_permission,
			delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id, extra_data, creation_date, update_date)
		SELECT 1, $1, 0, $2, 1, $3, 0, '[]', 3, '[]', 256, 0, 0, $6, $4, '["WUP"]', $5, $5
		WHERE NOT EXISTS (SELECT 1 FROM datastore.objects WHERE owner = $1 AND persistence_slot_id = $4 AND data_type = 1)`,
		pid, nnid, mii, ProfileSlot, since, countryTag(mii))
	if err != nil {
		return err
	}

	if err := AddProfileRankings(x); err != nil {
		return err
	}

	return AddProfileRatings(x)
}

// profileRatingSlots is how many rating slots the game gives its own profile object (the maker's statistics). A profile
// without them is not what the game wrote, and the game may refuse it.
const profileRatingSlots = 9

// AddProfileRatings gives every profile object (data type 1) the rating slots the game creates for its own profile,
// where they are missing (zeros). Idempotent; profiles the game uploaded itself already have them.
func AddProfileRatings(x execer) error {
	_, err := x.Exec(`INSERT INTO datastore.object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value,
			range_min, range_max, period_hour, period_duration, total_value, count)
		SELECT o.data_id, s.slot, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
		FROM datastore.objects o
		JOIN (SELECT 0 AS slot UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4
			UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8) s
		WHERE o.data_type = 1 AND s.slot < $1
			AND NOT EXISTS (SELECT 1 FROM datastore.object_ratings r WHERE r.data_id = o.data_id AND r.slot = s.slot)`, profileRatingSlots)

	return err
}

// levelRatingSlots is how many rating slots a level has (0..5, see importLevel).
const levelRatingSlots = 6

// AddLevelRatings gives every level the rating slots the game expects (zeros where missing). Idempotent.
func AddLevelRatings(x execer) error {
	_, err := x.Exec(`INSERT INTO datastore.object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value,
			range_min, range_max, period_hour, period_duration, total_value, count)
		SELECT o.data_id, s.slot, 0, 0, 0, 0, 0, 2147483647, 0, 0, 0, 0
		FROM datastore.objects o
		JOIN (SELECT 0 AS slot UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5) s
		WHERE o.data_type > 2 AND o.data_type < 50 AND s.slot < $1
			AND NOT EXISTS (SELECT 1 FROM datastore.object_ratings r WHERE r.data_id = o.data_id AND r.slot = s.slot)`, levelRatingSlots)

	return err
}

// regionCountry maps the region/language code that ends the archive's Mii data (the last 4 bytes, big endian: 0 Japan,
// 1 USA English, 2 Canada French, 3 Latin America Spanish, 4 UK English, 5 France, 6 Spain, 7 Germany, 8 Italy,
// 9 Netherlands, 10 Russia, 11 Portugal; read from the names of the makers) to a representative country in the
// game's own numbering (Wii country codes; the flag shown is the animation frame with that number). The archive does not
// say where a maker really lives, so this is the nearest guess: an Australian maker gets the UK flag, for instance.
var regionCountry = []int{1, 49, 18, 36, 110, 77, 105, 78, 83, 94, 100, 98}

// countryTag is the profile tag that carries a maker's country (the game reads the first tag as a number). Mii data
// without the 4 byte region tail keeps Japan, the tag the game wrote for the first profile this server saw.
func countryTag(mii []byte) string {
	if len(mii) >= 140 {
		if r := int(binary.BigEndian.Uint32(mii[136:140])); r >= 0 && r < len(regionCountry) {
			return fmt.Sprintf(`["%d"]`, regionCountry[r])
		}
	}

	return `["1"]`
}

// FixProfileCountries sets the country tag of the archive's profiles (period 0: the ones this server built or imported,
// never a profile a real game uploaded) from the region code in their Mii data. Idempotent.
func FixProfileCountries(x execer) error {
	cases := ""

	for r, country := range regionCountry {
		cases += fmt.Sprintf(` WHEN '%08X' THEN '["%d"]'`, r, country)
	}

	_, err := x.Exec(`UPDATE datastore.objects SET tags = CASE hex(substr(meta_binary, 137, 4))` + cases + ` ELSE tags END
		WHERE data_type = 1 AND period = 0 AND length(meta_binary) = 140`)

	return err
}

// profileRankingApps are the custom rankings the game puts a maker's profile in when somebody stars one of their levels
// (a game test showed the entries appear then): the game asks for profiles through these rankings, and a profile that is
// not in them is skipped, so its Mii, name and flag are not shown. The value is how many stars the maker's levels hold.
var profileRankingApps = []int{300000000, 300001200}

// AddProfileRankings puts every profile object (data type 1) into the rankings above where it is missing. Idempotent.
func AddProfileRankings(x execer) error {
	for _, app := range profileRankingApps {
		if _, err := x.Exec(`INSERT INTO datastore.object_custom_rankings (data_id, application_id, value)
			SELECT p.data_id, $1, MAX(1, COALESCE((SELECT SUM(r.value) FROM datastore.object_custom_rankings r
				JOIN datastore.objects l ON l.data_id = r.data_id
				WHERE l.owner = p.owner AND r.application_id = 0 AND l.data_type > 2 AND l.data_type < 50), 0))
			FROM datastore.objects p
			WHERE p.data_type = 1
				AND NOT EXISTS (SELECT 1 FROM datastore.object_custom_rankings q WHERE q.data_id = p.data_id AND q.application_id = $1)`, app); err != nil {
			return err
		}
	}

	return nil
}

// insertPeople creates the profile objects of the creator, the world record holder and the first player to clear the level.
func insertPeople(x execer, lvl upstream.Level) error {
	for _, p := range []struct {
		pid  int64
		nnid string
		mii  []byte
	}{
		{lvl.CreatorID, lvl.Creator, lvl.CreatorMii},
		{lvl.WorldRecordPID, lvl.WorldRecordHolderNNID, lvl.WorldRecordMii},
		{lvl.FirstClearPID, lvl.FirstClearNNID, lvl.FirstClearMii},
	} {
		if err := insertProfile(x, p.pid, p.nnid, p.mii, lvl.UploadTime); err != nil {
			return err
		}
	}

	return nil
}

// ImportRecord adds the archived record of a level that is already in the catalog and remembers that it was looked
// up. A level without a record (nobody cleared it, or the archive has none) is remembered too, so it is not asked again.
func ImportRecord(db *sql.DB, id int64, lvl upstream.Level) error {
	if lvl.HasRecord() {
		if err := insertRecord(db, id, lvl); err != nil {
			return err
		}
	}

	if err := insertPeople(db, lvl); err != nil {
		return err
	}

	return MarkRecordChecked(db, id)
}

// MarkRecordChecked remembers that the level's record was looked up.
func MarkRecordChecked(db *sql.DB, id int64) error {
	_, err := db.Exec(`INSERT INTO datastore.level_lookup (data_id, checked) VALUES ($1, $2)
		ON CONFLICT (data_id) DO UPDATE SET checked = excluded.checked`, id, time.Now().UTC())

	return err
}

// RecordState reports whether the level has a course record, and whether its record was already looked up.
func RecordState(db *sql.DB, id int64) (has, checked bool, err error) {
	var one int

	switch e := db.QueryRow(`SELECT 1 FROM datastore.course_records WHERE data_id = $1 AND slot = $2`, id, RecordSlot).Scan(&one); {
	case e == nil:
		has = true
	case e != sql.ErrNoRows:
		return false, false, e
	}

	switch e := db.QueryRow(`SELECT 1 FROM datastore.level_lookup WHERE data_id = $1`, id).Scan(&one); {
	case e == nil:
		checked = true
	case e != sql.ErrNoRows:
		return has, false, e
	}

	return has, checked, nil
}

// IsStub reports whether the catalog holds the level as an archive level whose file has not been fetched yet.
// known is false for levels the catalog does not have as archive levels at all.
func IsStub(db *sql.DB, id int64) (stub, known bool, err error) {
	var fetched sql.NullTime

	switch err := db.QueryRow(`SELECT fetched FROM datastore.level_sources WHERE data_id = $1`, id).Scan(&fetched); {
	case err == sql.ErrNoRows:
		return false, false, nil
	case err != nil:
		return false, false, err
	}

	return !fetched.Valid, true, nil
}

// DropStub removes a level that is still a stub (it never had a file). A complete level or a local object is left alone.
// It reports whether anything was removed.
func DropStub(db *sql.DB, id int64) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}

	defer tx.Rollback() //nolint:errcheck

	var one int
	switch err := tx.QueryRow(`SELECT 1 FROM datastore.level_sources WHERE data_id = $1 AND fetched IS NULL`, id).Scan(&one); {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}

	for _, t := range []string{"objects", "object_ratings", "object_custom_rankings", "buffer_queues", "course_records", "level_sources"} {
		if _, err := tx.Exec(`DELETE FROM datastore.`+t+` WHERE data_id = $1`, id); err != nil {
			return false, err
		}
	}

	return true, tx.Commit()
}

// Complete finishes a stub: it validates and stores the file and fills in size and meta_binary.
func Complete(db *sql.DB, id int64, bin []byte, opt Options) error {
	opt.defaults()

	chunks, err := Split(bin)
	if err != nil {
		return fmt.Errorf("level %d: %w", id, err)
	}

	if _, known, err := IsStub(db, id); err != nil {
		return err
	} else if !known {
		return ErrNoStub
	}

	if err := writeFile(opt.CoursesDir, id, bin); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`UPDATE datastore.objects SET size = $1, meta_binary = $2 WHERE data_id = $3`, len(bin), MetaBinary(chunks), id); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE datastore.level_sources SET fetched = $1 WHERE data_id = $2`, time.Now().UTC(), id); err != nil {
		return err
	}

	return tx.Commit()
}

// write replaces a level's rows. fetched=false leaves level_sources.fetched NULL (a stub). With ifAbsent, a level that is
// already known as an archive level is not touched. Objects that are not archive levels (uploaded by players) are never touched.
func write(db *sql.DB, lvl upstream.Level, size int, meta []byte, fetched, ifAbsent bool, opt Options) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback() //nolint:errcheck

	id := lvl.LevelID

	var one int
	switch err := tx.QueryRow(`SELECT 1 FROM datastore.objects WHERE data_id = $1`, id).Scan(&one); {
	case err == nil:
		var src int
		if err := tx.QueryRow(`SELECT 1 FROM datastore.level_sources WHERE data_id = $1`, id).Scan(&src); err != nil {
			return ErrLocalObject
		}

		if ifAbsent {
			return nil
		}
	case err != sql.ErrNoRows:
		return err
	}

	for _, t := range []string{"objects", "object_ratings", "object_custom_rankings", "buffer_queues", "course_records", "level_sources"} {
		if _, err := tx.Exec(`DELETE FROM datastore.`+t+` WHERE data_id = $1`, id); err != nil {
			return err
		}
	}

	created := lvl.UploadTime.UTC()

	if _, err := tx.Exec(`INSERT INTO datastore.objects (
			data_id, upload_completed, owner, size, name, data_type, meta_binary, permission, permission_recipients,
			delete_permission, delete_permission_recipients, flag, period, refer_data_id, tags, persistence_slot_id,
			extra_data, creation_date, update_date
		) VALUES ($1, 1, $2, $3, $4, $5, $6, 0, '[]', 3, '[]', 0, 0, 0, '[]', 65535, '[]', $7, $7)`,
		id, lvl.CreatorID, size, lvl.Name, opt.DataType, meta, created); err != nil {
		return err
	}

	// * slots as the SMMDB indexer reads them: 0 plays, 2 clears, 3 attempts, 4 failures (1 unknown)
	// * plus slot 5 (the star/favorite counter): a level without it is not what the game wrote, and the game then shows no
	// * Mii, name or flag for its creator (found in a game test: only levels the player had starred, which makes the
	// * server create that slot, showed them)
	counters := []int64{lvl.TotalAttempts, 0, lvl.Clears, lvl.TotalAttempts, lvl.Failures, 0}
	for slot, v := range counters {
		if _, err := tx.Exec(`INSERT INTO datastore.object_ratings (data_id, slot, flag, internal_flag, lock_type, initial_value,
			range_min, range_max, period_hour, period_duration, total_value, count) VALUES ($1, $2, 0, 0, 0, 0, 0, 2147483647, 0, 0, $3, $3)`,
			id, slot, v); err != nil {
			return err
		}
	}

	if lvl.Stars > 0 {
		if _, err := tx.Exec(`INSERT INTO datastore.object_custom_rankings (data_id, application_id, value) VALUES ($1, $2, $3)`,
			id, opt.StarsApp, lvl.Stars); err != nil {
			return err
		}

		n := lvl.Stars
		if n > int64(opt.MaxStarBuffers) {
			n = int64(opt.MaxStarBuffers)
		}

		for i := int64(0); i < n; i++ {
			buf := binary.BigEndian.AppendUint32(nil, uint32(i))
			if _, err := tx.Exec(`INSERT INTO datastore.buffer_queues (data_id, slot, creation_date, buffer) VALUES ($1, 2, $2, $3)`, id, created, buf); err != nil {
				return err
			}
		}
	}

	if lvl.TotalAttempts > 0 {
		if _, err := tx.Exec(`INSERT INTO datastore.object_custom_rankings (data_id, application_id, value) VALUES ($1, $2, $3)`,
			id, opt.DifficultyApp, int64(math.Round(lvl.ClearRate*100))); err != nil {
			return err
		}
	}

	if err := insertPeople(tx, lvl); err != nil {
		return err
	}

	if lvl.HasRecord() {
		if err := insertRecord(tx, id, lvl); err != nil {
			return err
		}

		if _, err := tx.Exec(`INSERT INTO datastore.level_lookup (data_id, checked) VALUES ($1, $2) ON CONFLICT (data_id) DO NOTHING`, id, time.Now().UTC()); err != nil {
			return err
		}
	}

	var fetchedAt any // NULL for a stub
	if fetched {
		fetchedAt = time.Now().UTC()
	}

	if _, err := tx.Exec(`INSERT INTO datastore.level_sources (data_id, url, clear_rate, attempts, stars, fetched) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, lvl.URL, lvl.ClearRate, lvl.TotalAttempts, lvl.Stars, fetchedAt); err != nil {
		return err
	}

	return tx.Commit()
}
