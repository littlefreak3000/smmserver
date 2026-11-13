package pool

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// evictTo is how far below the limit an eviction goes, so it does not run again after every new level.
const evictTo = 0.9

// CourseFilesSize is how many bytes the downloaded level files take (the .bin and .jpg files in the courses folder).
func (p *Pool) CourseFilesSize() (int64, error) {
	dir := p.Import.CoursesDir
	if dir == "" {
		return 0, nil
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	var total int64

	for _, e := range entries {
		if e.IsDir() || !isCourseFile(e.Name()) {
			continue
		}

		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}

	return total, nil
}

func isCourseFile(name string) bool {
	return strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".jpg")
}

// protectedLevels are the levels Evict never removes: the ones a local account starred (the list on its profile), and the
// ones a local account made.
func (p *Pool) protectedLevels() (map[int64]bool, error) {
	keep := map[int64]bool{}

	rows, err := p.DB.Query(`SELECT b.buffer FROM datastore.buffer_queues b
		JOIN datastore.objects o ON o.data_id = b.data_id
		JOIN datastore.accounts a ON a.pid = o.owner
		WHERE o.data_type = 1 AND b.slot = 0`)
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		var buf []byte
		if rows.Scan(&buf) == nil && len(buf) >= 4 {
			keep[int64(binary.LittleEndian.Uint32(buf[:4]))] = true
		}
	}

	rows.Close()

	own, err := p.DB.Query(`SELECT data_id FROM datastore.objects
		WHERE data_type > 2 AND data_type < 50 AND owner IN (SELECT pid FROM datastore.accounts)`)
	if err != nil {
		return nil, err
	}

	defer own.Close()

	for own.Next() {
		var id int64
		if own.Scan(&id) == nil {
			keep[id] = true
		}
	}

	return keep, own.Err()
}

// Evict removes the oldest downloaded levels (by the time they were fetched) while the level files take more than
// maxBytes, and stops at 90% of it. It removes the level's files and every row that belongs to it, so the game can fetch
// it again later if it asks for it. Levels that a local account starred or made are kept. maxBytes <= 0 does nothing.
func (p *Pool) Evict(maxBytes int64) (removed int, freed int64, err error) {
	if maxBytes <= 0 || p.Import.CoursesDir == "" {
		return 0, 0, nil
	}

	used, err := p.CourseFilesSize()
	if err != nil || used <= maxBytes {
		return 0, 0, err
	}

	keep, err := p.protectedLevels()
	if err != nil {
		return 0, 0, err
	}

	rows, err := p.DB.Query(`SELECT data_id FROM datastore.level_sources WHERE fetched IS NOT NULL ORDER BY fetched, data_id`)
	if err != nil {
		return 0, 0, err
	}

	var oldest []int64

	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && !keep[id] {
			oldest = append(oldest, id)
		}
	}

	rows.Close()

	low := int64(float64(maxBytes) * evictTo)

	for _, id := range oldest {
		if used <= low {
			break
		}

		size, ok := p.fileSize(id)
		if !ok {
			continue // nothing on disk to free
		}

		if err := p.removeLevel(id); err != nil {
			return removed, freed, err
		}

		used -= size
		freed += size
		removed++
	}

	return removed, freed, nil
}

func (p *Pool) fileSize(id int64) (int64, bool) {
	var total int64

	found := false

	for _, ext := range []string{".bin", ".jpg"} {
		if info, err := os.Stat(filepath.Join(p.Import.CoursesDir, strconv.FormatInt(id, 10)+ext)); err == nil {
			total += info.Size()
			found = true
		}
	}

	return total, found
}

// removeLevel deletes one level: its rows first (in one transaction), then its files.
func (p *Pool) removeLevel(id int64) error {
	tx, err := p.DB.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback() //nolint:errcheck

	for _, t := range []string{"objects", "object_ratings", "object_custom_rankings", "buffer_queues", "course_records", "level_sources", "level_lookup"} {
		if _, err := tx.Exec(`DELETE FROM datastore.`+t+` WHERE data_id = $1`, id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	for _, ext := range []string{".bin", ".jpg"} {
		_ = os.Remove(filepath.Join(p.Import.CoursesDir, strconv.FormatInt(id, 10)+ext))
	}

	return nil
}

// KeepWithin runs Evict now and then (first after a short wait) until ctx is cancelled. maxBytes <= 0 does nothing.
func (p *Pool) KeepWithin(ctx context.Context, maxBytes int64, every time.Duration) {
	if maxBytes <= 0 {
		return
	}

	if every <= 0 {
		every = 5 * time.Minute
	}

	wait := time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		wait = every

		removed, freed, err := p.Evict(maxBytes)

		switch {
		case err != nil:
			p.logf("pool: cleaning up old levels failed: %v", err)
		case removed > 0:
			p.logf("pool: the level files are over %d MB; removed the %d oldest levels (%.1f MB)", maxBytes>>20, removed, float64(freed)/(1<<20))
		}
	}
}
