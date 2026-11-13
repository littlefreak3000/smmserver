package pool

import (
	"archive/zip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/PretendoNetwork/super-mario-maker/database"
)

// A cache can be packed into one zip (ExportCache) and unpacked into an empty data folder on another machine
// (ImportCache), so people do not have to fetch the levels from the volunteer services one by one.

var entryName = regexp.MustCompile(`^(catalog\.sqlite|cache\.json|courses/\d+\.(bin|jpg))$`)

// ExportCache writes the cache of a data folder to a zip: the manifest, the level files and a catalog that holds the
// levels but nothing about the people who played on this server (accounts, their profiles, their records, seeds).
// db is the open catalog of dataDir. What players did to the levels (ratings, star counts) stays as it is, so build a cache
// to share in a data folder nobody has played in.
func ExportCache(db *sql.DB, dataDir, zipPath string) (*CacheManifest, error) {
	m, err := ReadManifest(dataDir)
	if err != nil {
		return nil, err
	}

	if m == nil {
		return nil, errors.New("this data folder is not a cache (there is no cache.json); build one with: smm-server cache build")
	}

	tmp, err := os.MkdirTemp("", "smm-cache-export")
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(tmp)

	copyPath := filepath.Join(tmp, "catalog.sqlite")

	// * a consistent copy of the catalog (the live one may have recent changes only in its -wal file)
	if _, err := db.Exec(`VACUUM datastore INTO ` + quote(copyPath)); err != nil {
		return nil, fmt.Errorf("copying the catalog: %w", err)
	}

	if err := stripLocalData(copyPath); err != nil {
		return nil, err
	}

	fp, n, err := fingerprintOf(copyPath)
	if err != nil {
		return nil, err
	}

	out := *m
	out.Fingerprint, out.Levels = fp, n

	rows, err := db.Query(`SELECT data_id FROM datastore.level_sources WHERE fetched IS NOT NULL ORDER BY data_id`)
	if err != nil {
		return nil, err
	}

	var ids []int64

	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}

	rows.Close()

	f, err := os.Create(zipPath)
	if err != nil {
		return nil, err
	}

	zw := zip.NewWriter(f)

	add := func(name, path string) error {
		in, err := os.Open(path)
		if err != nil {
			return err
		}

		defer in.Close()

		w, err := zw.Create(name)
		if err != nil {
			return err
		}

		_, err = io.Copy(w, in)

		return err
	}

	err = add("catalog.sqlite", copyPath)

	if err == nil {
		manifestPath := filepath.Join(tmp, manifestName)
		if err = out.Write(tmp); err == nil {
			err = add(manifestName, manifestPath)
		}
	}

	for _, id := range ids {
		if err != nil {
			break
		}

		for _, ext := range []string{".bin", ".jpg"} {
			name := strconv.FormatInt(id, 10) + ext

			src := filepath.Join(dataDir, "courses", name)
			if _, statErr := os.Stat(src); statErr == nil {
				err = add("courses/"+name, src)
			}
		}
	}

	if cerr := zw.Close(); err == nil {
		err = cerr
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		os.Remove(zipPath)

		return nil, err
	}

	return &out, nil
}

func quote(path string) string {
	out := []byte{'\''}

	for _, c := range []byte(path) {
		if c == '\'' {
			out = append(out, '\'')
		}

		out = append(out, c)
	}

	return string(append(out, '\''))
}

// stripLocalData removes everything about local players from a copy of the catalog.
func stripLocalData(path string) error {
	db, err := database.Open(path)
	if err != nil {
		return err
	}

	defer db.Close()

	for _, q := range []string{
		`DELETE FROM datastore.course_records WHERE first_pid IN (SELECT pid FROM datastore.accounts) OR best_pid IN (SELECT pid FROM datastore.accounts)`,
		`DELETE FROM datastore.objects WHERE owner IN (SELECT pid FROM datastore.accounts)`,
		`DELETE FROM datastore.object_ratings WHERE data_id NOT IN (SELECT data_id FROM datastore.objects)`,
		`DELETE FROM datastore.object_custom_rankings WHERE data_id NOT IN (SELECT data_id FROM datastore.objects)`,
		`DELETE FROM datastore.buffer_queues WHERE data_id NOT IN (SELECT data_id FROM datastore.objects)`,
		`DELETE FROM datastore.course_records WHERE data_id NOT IN (SELECT data_id FROM datastore.objects)`,
		`DELETE FROM datastore.accounts`,
		`DELETE FROM datastore.challenge_seeds`,
	} {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("cleaning the copy: %w", err)
		}
	}

	if _, err := db.Exec(`VACUUM datastore`); err != nil {
		return err
	}

	return nil
}

func fingerprintOf(path string) (string, int, error) {
	db, err := database.Open(path)
	if err != nil {
		return "", 0, err
	}

	defer db.Close()

	return CacheFingerprint(db)
}

// ImportCache unpacks an exported cache into a data folder that holds no levels yet (or any folder with force, which
// replaces its catalog and level files). It checks that what it unpacked matches the fingerprint in the manifest.
func ImportCache(zipPath, dataDir string, force bool) (*CacheManifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", zipPath, err)
	}

	defer zr.Close()

	for _, f := range zr.File {
		if !entryName.MatchString(f.Name) {
			return nil, fmt.Errorf("%s does not look like a cache: unexpected file %q", zipPath, f.Name)
		}
	}

	if !force {
		if _, err := os.Stat(filepath.Join(dataDir, "catalog.sqlite")); err == nil {
			return nil, fmt.Errorf("%s already has a catalog. Use an empty data folder (SMM_DATA_DIR), or --force to replace it", dataDir)
		}
	} else {
		for _, name := range []string{"catalog.sqlite", "catalog.sqlite-wal", "catalog.sqlite-shm", manifestName} {
			os.Remove(filepath.Join(dataDir, name))
		}

		_ = os.RemoveAll(filepath.Join(dataDir, "courses"))
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "courses"), 0o755); err != nil {
		return nil, err
	}

	for _, f := range zr.File {
		target := filepath.Join(dataDir, filepath.FromSlash(f.Name))

		in, err := f.Open()
		if err != nil {
			return nil, err
		}

		out, err := os.Create(target)
		if err != nil {
			in.Close()

			return nil, err
		}

		_, err = io.Copy(out, in)
		in.Close()

		if cerr := out.Close(); err == nil {
			err = cerr
		}

		if err != nil {
			return nil, err
		}
	}

	m, err := ReadManifest(dataDir)
	if err != nil || m == nil {
		return nil, errors.New("the zip has no usable cache.json")
	}

	fp, n, err := fingerprintOf(filepath.Join(dataDir, "catalog.sqlite"))
	if err != nil {
		return nil, err
	}

	if fp != m.Fingerprint || n != m.Levels {
		return nil, fmt.Errorf("the unpacked cache does not match its manifest (levels %d, fingerprint %s; the manifest says %d, %s)", n, fp, m.Levels, m.Fingerprint)
	}

	return m, nil
}
