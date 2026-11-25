package database

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/globals"
	sqlite "modernc.org/sqlite"
)

// DB is the catalog database. It is a SQLite file attached under the schema name "datastore",
// so every query written for the original Postgres schema ("datastore.objects", ...) works unchanged.
var DB *sql.DB

// OnMissing, when set, is asked to bring an object into the catalog (for example by fetching it from the public
// archive) when a lookup finds nothing. It reports whether it added the object. Lookups consult it at most once
// per call, so it must be fast or time-bounded; it is nil unless on-demand fetching is enabled.
var OnMissing func(dataID uint64) bool

// OnMissingProfile, when set, is asked to create the profile object of a maker (owner PID, persistence slot 0) that the
// game asks for and the catalog does not have, from what the archive knows about that person.
var OnMissingProfile func(pid uint32) bool

var registerHook sync.Once

const catalogParam = "smm_catalog"

// Open opens (creating if needed) the catalog at path and ensures the schema exists.
func Open(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}

	if err := createSchema(abs); err != nil {
		return nil, err
	}

	registerHook.Do(func() {
		sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
			u, err := url.Parse(dsn)
			if err != nil {
				return err
			}

			catalog := u.Query().Get(catalogParam)
			if catalog == "" {
				return nil // not one of our connections
			}

			stmts := []string{
				"ATTACH DATABASE '" + strings.ReplaceAll(catalog, "'", "''") + "' AS datastore",
				"PRAGMA datastore.journal_mode=WAL",
				"PRAGMA datastore.synchronous=NORMAL",
			}

			for _, stmt := range stmts {
				if _, err := conn.ExecContext(context.Background(), stmt, nil); err != nil {
					return fmt.Errorf("%s: %w", stmt, err)
				}
			}

			return nil
		})
	})

	// * The main schema is a throwaway in-memory database; all real tables live in the attached
	// * "datastore" schema. Immediate transactions + a busy timeout serialise concurrent writers.
	q := url.Values{}
	q.Set("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(0)")
	q.Set("_txlock", "immediate")
	q.Set("_time_format", "sqlite")
	q.Set(catalogParam, abs)

	db, err := sql.Open("sqlite", "file::memory:?"+q.Encode())
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// Close closes the catalog. Closing the last connection merges the write-ahead log into catalog.sqlite, so call this
// before the process exits (a killed process leaves the log behind; it is recovered on the next start, but a data folder
// copied without its -wal file would lose the newest changes).
func Close() error {
	if DB == nil {
		return nil
	}

	err := DB.Close()
	DB = nil

	return err
}

// Connect opens the catalog and assigns the package-level DB. Exits the process on failure.
func Connect(path string) {
	var err error

	DB, err = Open(path)
	if err != nil {
		globals.Logger.Criticalf("Failed to open catalog %s: %s", path, err.Error())
		os.Exit(1)
	}

	globals.Logger.Successf("Opened catalog %s", path)

	// * archive levels used to be stored in persistence slot 0 (a maker's profile slot); courses have none
	if _, err := DB.Exec(`UPDATE datastore.objects SET persistence_slot_id = 65535
		WHERE persistence_slot_id = 0 AND data_type > 2 AND data_type < 50 AND data_id IN (SELECT data_id FROM datastore.level_sources)`); err != nil {
		globals.Logger.Warningf("Could not move archive levels out of the profile slot: %s", err.Error())
	}

	// * levels players upload get IDs from 2^32 up, which no archived level has (see FirstLocalID); an older catalog's counter
	// * is raised to it. Levels uploaded before keep the IDs they have.
	if _, err := DB.Exec(`UPDATE datastore.sqlite_sequence SET seq = $1 WHERE name = 'objects' AND seq < $1`, FirstLocalID-1); err != nil {
		globals.Logger.Warningf("Could not move the ID counter of uploaded levels: %s", err.Error())
	}

	ensureEventCourseMetaDataFileExists()
}

// FirstLocalID is the lowest data ID a level a player uploads can have: 2^32. The course ID the game shows is a checksum and
// 48 bits of data ID, and every archived level has an ID below 2^32 (the second group of its course ID reads 0000), so IDs
// from here up can never collide with an archived level.
const FirstLocalID = 1 << 32

// serverID is the second group of the course ID of the levels uploaded to this server (SMM_SERVER_ID): the upper 16 bits of a
// 48-bit data ID. Servers with different values hand out different IDs, so levels can be shared between them without a
// collision. 0000 is the archive's. Until it is set (tests) it is 0001.
var serverID uint16 = 1

// SetServerID sets the server ID. 0 is refused: that group belongs to the archive.
func SetServerID(id uint16) error {
	if id == 0 {
		return errors.New("the server ID cannot be 0000: that is the archive's")
	}

	serverID = id

	return nil
}

// ParseServerID reads a server ID: exactly 4 hex digits, not 0000 (that is the archive's).
func ParseServerID(s string) (uint16, error) {
	s = strings.TrimSpace(s)

	if len(s) != 4 {
		return 0, fmt.Errorf("%q must be exactly 4 hex digits (0001 to FFFF)", s)
	}

	v, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, fmt.Errorf("%q must be exactly 4 hex digits (0001 to FFFF)", s)
	}

	if v == 0 {
		return 0, errors.New("0000 cannot be used: it is the archive's")
	}

	return uint16(v), nil
}

// ServerID returns the server ID (see SetServerID).
func ServerID() uint16 { return serverID }

// LocalIDRange is the range of data IDs this server gives to uploaded levels: 2^32 of them, from <server ID> * 2^32.
func LocalIDRange() (lo, hi int64) {
	lo = int64(serverID) << 32

	return lo, lo + 1<<32 - 1
}

// NextLocalID is an SQL expression for the next free data ID of this server's range: one above the highest ID in it, or the
// first of the range. It is evaluated by the INSERT it is part of, which SQLite runs one writer at a time, so two uploads
// never get the same ID. Changing the server ID moves new uploads to the new range; levels already uploaded keep their IDs.
func NextLocalID() string {
	lo, hi := LocalIDRange()

	return fmt.Sprintf(`(SELECT COALESCE(MAX(data_id), %d) + 1 FROM datastore.objects WHERE data_id BETWEEN %d AND %d)`, lo-1, lo, hi)
}

//go:embed schema.sql
var schemaSQL string

// createSchema applies schema.sql to the catalog file. It uses a plain connection (not the attached pool)
// because schema.sql uses unqualified table names.
func createSchema(path string) error {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("applying schema.sql: %w", err)
	}

	return nil
}

func ensureEventCourseMetaDataFileExists() {
	// * The event course metadata file is REQUIRED for
	// * Super Mario Maker to open Course World. This
	// * ensures that it exists both in S3 and in the
	// * database before booting. It has the reserved
	// * DataID 900000
	bucket := os.Getenv("PN_SMM_CONFIG_S3_BUCKET")
	key := "900000.bin"

	eventInfo, err := globals.S3StatObject(bucket, key)
	objectSizeS3 := uint64(eventInfo.Size)
	if err != nil {
		globals.Logger.Errorf("Failed to stat event course metadata file 900000.bin. Make sure it exists in the course data directory. S3 error: %s", err.Error())
		os.Exit(1)
	}

	rememberEventCourseFile(eventInfo) // the version the catalog is about to be made to match (see eventwatch.go)

	globals.Logger.Success("Event course metadata file found. Verifying database")

	var exists bool
	err = DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM datastore.objects WHERE data_id=900000) AS "exists"`).Scan(&exists)
	if err != nil {
		globals.Logger.Errorf("Error querying for event course metadata object: %s", err.Error())
		os.Exit(1)
	}

	now := time.Now().UTC()

	if !exists {
		var dataID uint64

		globals.Logger.Info("Event course metadata object not found in catalog. Creating")

		err := DB.QueryRow(`INSERT INTO datastore.objects (
			data_id,
			upload_completed,
			owner,
			size,
			name,
			data_type,
			meta_binary,
			permission,
			permission_recipients,
			delete_permission,
			delete_permission_recipients,
			flag,
			period,
			refer_data_id,
			tags,
			persistence_slot_id,
			extra_data,
			creation_date,
			update_date
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			$6,
			$7,
			$8,
			$9,
			$10,
			$11,
			$12,
			$13,
			$14,
			$15,
			$16,
			$17,
			$18,
			$19
		) RETURNING data_id`,
			900000,
			true,
			2, // * "Quazal Rendez-Vous" special account
			objectSizeS3,
			"",       // * Has no name
			50,       // * Metadata file has DataType 50. Event courses have DataType 51
			[]byte{}, // * No MetaBinary
			0,        // * Accessible by everyone
			Array([]uint32{}),
			3, // * THE REAL SERVER HAS THIS SET TO 0, FOR EVERYONE. THAT'S SUPER INSECURE.
			Array([]uint32{}),
			0,
			64306,
			0,
			Array([]string{}),
			0, // * Unsure what the slot ID actually is
			Array([]string{}),
			now,
			now,
		).Scan(&dataID)
		if err != nil {
			globals.Logger.Errorf("Error creating event course metadata object: %s", err.Error())
			os.Exit(1)
		}
	} else {
		var objectSizeDB uint32

		err := DB.QueryRow(`SELECT size FROM datastore.objects WHERE data_id=900000`).Scan(&objectSizeDB)
		if err != nil {
			globals.Logger.Errorf("Error querying event course metadata object size: %s", err.Error())
			os.Exit(1)
		}

		if objectSizeS3 != uint64(objectSizeDB) {
			globals.Logger.Success("Event course metadata object found in catalog! Updating size")

			_, err := DB.Exec(`UPDATE datastore.objects SET size=$1, update_date=$2 WHERE data_id=900000`, objectSizeS3, now)
			if err != nil {
				globals.Logger.Errorf("Error updating event course metadata object size: %s", err.Error())
				os.Exit(1)
			}
		} else {
			globals.Logger.Success("Event course metadata object found in catalog!")
		}

		globals.Logger.Info("Ensuring event course metadata object has correct delete permission")

		_, err = DB.Exec(`UPDATE datastore.objects SET delete_permission=3 WHERE data_id=900000`)
		if err != nil {
			globals.Logger.Errorf("Error updating event course metadata object delete permission: %s", err.Error())
			os.Exit(1)
		}
	}

	globals.Logger.Success("Event course metadata object found and is up to date!")
}
