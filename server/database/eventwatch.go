package database

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// The event course list (data ID 900000) can be replaced while the server runs (`smm-server events build`, or by hand). The
// game asks the server about 900000 first and downloads the file right after, every time it opens Course World, so that
// request is the moment to look: if the file is not the one the catalog describes, its size and update time are updated
// before the answer goes out. There is no timer.

var eventList struct {
	sync.Mutex
	sig string // identifies the version of 900000.bin the catalog describes
}

func eventSignature(info minio.ObjectInfo) string {
	return fmt.Sprintf("%d/%s/%d", info.Size, info.ETag, info.LastModified.UnixNano())
}

// rememberEventCourseFile records the version of 900000.bin the catalog was just made to match (at start-up).
func rememberEventCourseFile(info minio.ObjectInfo) {
	eventList.Lock()
	defer eventList.Unlock()

	eventList.sig = eventSignature(info)
}

// MarkEventCourseListChanged records a new version of the event course list: its size, and its update time, which the game
// compares with the one in its save data to decide whether there are new event courses.
func MarkEventCourseListChanged(db *sql.DB, size uint64, now time.Time) error {
	_, err := db.Exec(`UPDATE datastore.objects SET size=$1, update_date=$2 WHERE data_id=900000`, size, now.UTC())

	return err
}

// refreshEventCourseList updates the catalog if info is not the version it describes. It reports whether it did.
func refreshEventCourseList(db *sql.DB, info minio.ObjectInfo, now time.Time) (bool, error) {
	eventList.Lock()
	defer eventList.Unlock()

	sig := eventSignature(info)
	if sig == eventList.sig {
		return false, nil
	}

	first := eventList.sig == "" // no baseline (the start-up check failed): take this version as it is
	eventList.sig = sig

	if first {
		return false, nil
	}

	return true, MarkEventCourseListChanged(db, uint64(info.Size), now)
}

// RefreshEventCourseList is called when the game asks about data ID 900000 (see register_common_secure_server_protocols.go).
// It costs one local stat of the file.
func RefreshEventCourseList() {
	db := DB
	if db == nil {
		return
	}

	info, err := globals.S3StatObject(os.Getenv("PN_SMM_CONFIG_S3_BUCKET"), "900000.bin")
	if err != nil {
		return
	}

	changed, err := refreshEventCourseList(db, info, time.Now())

	switch {
	case err != nil:
		globals.Logger.Warningf("Event course list changed, but the catalog could not be updated: %s", err.Error())
	case changed:
		globals.Logger.Successf("Event course list changed (%d bytes): the game gets the new list now", info.Size)
	}
}
