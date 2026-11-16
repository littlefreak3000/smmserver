package database

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/globals"
)

// MarkEventCourseListChanged records a new version of the event course list (data ID 900000): its size, and its update time,
// which the game compares with the one in its save data to decide whether there are new event courses. The size is read
// when the server starts; this does the same while it runs.
func MarkEventCourseListChanged(db *sql.DB, size uint64, now time.Time) error {
	_, err := db.Exec(`UPDATE datastore.objects SET size=$1, update_date=$2 WHERE data_id=900000`, size, now.UTC())

	return err
}

// WatchEventCourseFile checks every so often whether courses/900000.bin was replaced (by `smm-server events build`, or by
// hand) and, when it was, updates the catalog so the game fetches the new list the next time it opens Course World, with no
// restart. The version in place when this is called is the baseline: nothing changes until the file does.
func WatchEventCourseFile(every time.Duration) {
	bucket := os.Getenv("PN_SMM_CONFIG_S3_BUCKET")

	go func() {
		last := ""

		for {
			if db := DB; db != nil {
				info, err := globals.S3StatObject(bucket, "900000.bin")
				if err == nil {
					sig := fmt.Sprintf("%d/%s/%d", info.Size, info.ETag, info.LastModified.UnixNano())

					if last != "" && sig != last {
						if err := MarkEventCourseListChanged(db, uint64(info.Size), time.Now()); err != nil {
							globals.Logger.Warningf("Event course list changed, but the catalog could not be updated: %s", err.Error())
						} else {
							globals.Logger.Successf("Event course list changed (%d bytes): the game fetches it the next time it opens Course World", info.Size)
						}
					}

					last = sig
				}
			}

			time.Sleep(every)
		}
	}()
}
