package pool

// Index mode: levels are known from the whole-archive index and exist in the catalog as stubs (rows without a file)
// until someone needs them. EnsureFetched downloads one on demand; Prefetch/RunPrefetch do it ahead of time for the
// levels a list has just handed to the game. A level with no usable capture is marked dead, so it is never offered again.

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

type flight struct {
	done chan struct{}
	ok   bool
	err  error
}

// IsDead reports whether the level was found to have no usable capture.
func (p *Pool) IsDead(id int64) bool {
	var one int

	return p.DB.QueryRow(`SELECT 1 FROM datastore.index_dead WHERE data_id = $1`, id).Scan(&one) == nil
}

// MarkDead records that a level cannot be fetched and removes its stub.
func (p *Pool) MarkDead(id int64) error {
	if _, err := levelimport.DropStub(p.DB, id); err != nil {
		return err
	}

	_, err := p.DB.Exec(`INSERT INTO datastore.index_dead (data_id, marked) VALUES ($1, $2)
		ON CONFLICT (data_id) DO UPDATE SET marked = excluded.marked`, id, time.Now().UTC())

	return err
}

// Stub lists a level from its index metadata without downloading it. It does nothing for levels that are already
// in the catalog or dead.
func (p *Pool) Stub(lvl upstream.Level) error {
	if p.IsDead(lvl.LevelID) {
		return nil
	}

	return levelimport.ImportStub(p.DB, lvl, p.Import)
}

// Stubs returns how many levels are stubs, and how many are complete.
func (p *Pool) Stubs() (stubs, complete int, err error) {
	err = p.DB.QueryRow(`SELECT COALESCE(SUM(fetched IS NULL), 0), COALESCE(SUM(fetched IS NOT NULL), 0) FROM datastore.level_sources`).Scan(&stubs, &complete)

	return
}

// EnsureFetched makes sure the complete level (with its file) is in the catalog. Several callers asking for the same
// level share one download. It reports false for a level that does not exist upstream (it is marked dead).
func (p *Pool) EnsureFetched(ctx context.Context, id int64) (bool, error) {
	p.flightMu.Lock()

	if f, ok := p.flights[id]; ok {
		p.flightMu.Unlock()

		select {
		case <-f.done:
			return f.ok, f.err
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	f := &flight{done: make(chan struct{})}

	if p.flights == nil {
		p.flights = map[int64]*flight{}
	}

	p.flights[id] = f
	p.flightMu.Unlock()

	f.ok, f.err = p.ensureFetched(ctx, id)

	p.flightMu.Lock()
	delete(p.flights, id)
	p.flightMu.Unlock()
	close(f.done)

	return f.ok, f.err
}

func (p *Pool) ensureFetched(ctx context.Context, id int64) (bool, error) {
	if p.IsDead(id) {
		return false, nil
	}

	stub, known, err := levelimport.IsStub(p.DB, id)
	if err != nil {
		return false, err
	}

	if known && !stub {
		return true, nil
	}

	if !known {
		if p.known(id) { // an object players uploaded to this server
			return true, nil
		}

		var lvl *upstream.Level
		if p.Lookup != nil {
			if lvl, err = p.Lookup(id); err != nil {
				return false, err
			}
		}

		if lvl == nil { // not in the index (or no index): ask the archive API the old way
			return p.Ensure(ctx, id)
		}

		if err := levelimport.ImportStub(p.DB, *lvl, p.Import); err != nil && !errors.Is(err, levelimport.ErrLocalObject) {
			return false, err
		}
	}

	var url string
	if err := p.DB.QueryRow(`SELECT url FROM datastore.level_sources WHERE data_id = $1`, id).Scan(&url); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p.known(id), nil
		}

		return false, err
	}

	bin, err := p.Src.FetchBin(ctx, url)
	if err != nil {
		if errors.Is(err, upstream.ErrNotFound) {
			p.logf("pool: level %d has no capture; it will not be offered again", id)

			return false, p.MarkDead(id)
		}

		return false, err // transient: the stub stays and the next request tries again
	}

	if err := levelimport.Complete(p.DB, id, bin, p.Import); err != nil {
		if errors.Is(err, levelimport.ErrNoStub) {
			return p.known(id), nil
		}

		p.logf("pool: level %d: the capture is not a level (%v); it will not be offered again", id, err)

		return false, p.MarkDead(id)
	}

	return true, nil
}

// Prefetch queues levels to be downloaded in the background by RunPrefetch, in the order given. It never blocks:
// when the queue is full the rest are dropped (they are fetched on demand if the game asks for them).
func (p *Pool) Prefetch(ids ...int64) {
	p.queuedMu.Lock()
	defer p.queuedMu.Unlock()

	if p.queue == nil {
		p.queue = make(chan int64, 4096)
		p.queued = map[int64]bool{}
	}

	for _, id := range ids {
		if p.queued[id] {
			continue
		}

		select {
		case p.queue <- id:
			p.queued[id] = true
		default:
			return
		}
	}
}

// RunPrefetch downloads queued levels one at a time until ctx is cancelled. Upstream pacing is done by the client.
func (p *Pool) RunPrefetch(ctx context.Context) {
	p.queuedMu.Lock()

	if p.queue == nil {
		p.queue = make(chan int64, 4096)
		p.queued = map[int64]bool{}
	}

	queue := p.queue
	p.queuedMu.Unlock()

	timeout := p.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		case id := <-queue:
			c, cancel := context.WithTimeout(ctx, timeout)

			if _, err := p.EnsureFetched(c, id); err != nil && ctx.Err() == nil {
				p.logf("pool: prefetch of level %d failed: %v", id, err)
			}

			cancel()

			p.queuedMu.Lock()
			delete(p.queued, id)
			p.queuedMu.Unlock()
		}
	}
}

// EnsureRecord makes sure the archived world record and first clear of an archive level are in the catalog, asking the
// archive API once per level (a level nobody cleared, or one the archive has no record for, is remembered as looked
// up). It reports whether the level has a record afterwards. Levels players uploaded to this server are left alone.
func (p *Pool) EnsureRecord(ctx context.Context, id int64) (bool, error) {
	has, checked, err := levelimport.RecordState(p.DB, id)
	if err != nil || checked {
		return has, err
	}

	var one int
	if p.DB.QueryRow(`SELECT 1 FROM datastore.level_sources WHERE data_id = $1`, id).Scan(&one) != nil {
		return false, nil // not an archive level
	}

	return p.lookup(ctx, id, false)
}

// lookup fetches the per-level answer of the archive API (world record, first clear, and the creator, record holder and
// first clearer with their Miis) and stores it. One lookup at a time: the API client paces them anyway, and the second
// caller finds the first one's answer. force repeats a lookup that was done before.
func (p *Pool) lookup(ctx context.Context, id int64, force bool) (bool, error) {
	p.recordMu.Lock()
	defer p.recordMu.Unlock()

	has, checked, err := levelimport.RecordState(p.DB, id)
	if err != nil || (checked && !force) {
		return has, err
	}

	lvl, err := p.Src.Level(ctx, id)
	if err != nil {
		return false, err // not remembered: try again next time
	}

	if lvl == nil {
		return false, levelimport.MarkRecordChecked(p.DB, id)
	}

	if err := levelimport.ImportRecord(p.DB, id, *lvl); err != nil {
		return false, err
	}

	if lvl.HasRecord() {
		p.logf("record of level %d: %d ms by %s (first clear by %s); creator %s", id, lvl.WorldRecordMs, lvl.WorldRecordHolderNNID, lvl.FirstClearNNID, lvl.Creator)
	}

	return lvl.HasRecord(), nil
}

// EnsureProfile makes sure the profile object (Mii and name) of a player the archive knows exists: the creator of an
// archive level, or someone who holds a world record or cleared a level first. It looks the person up through one of
// their levels and reports whether the profile exists afterwards. Players who are not in the archive are not found.
func (p *Pool) EnsureProfile(ctx context.Context, pid int64) (bool, error) {
	if p.profileMissed(pid) {
		return false, nil
	}

	var id int64

	err := p.DB.QueryRow(`SELECT s.data_id FROM datastore.level_sources s JOIN datastore.objects o ON o.data_id = s.data_id
		WHERE o.owner = $1 LIMIT 1`, pid).Scan(&id)
	if err == sql.ErrNoRows {
		err = p.DB.QueryRow(`SELECT data_id FROM datastore.course_records WHERE best_pid = $1 OR first_pid = $1 LIMIT 1`, pid).Scan(&id)
	}

	if err == sql.ErrNoRows {
		p.missProfile(pid)

		return false, nil
	}

	if err != nil {
		return false, err
	}

	if _, err := p.lookup(ctx, id, true); err != nil {
		return false, err
	}

	var one int
	if p.DB.QueryRow(`SELECT 1 FROM datastore.objects WHERE owner = $1 AND persistence_slot_id = 0 AND data_type = 1`, pid).Scan(&one) != nil {
		p.missProfile(pid) // the archive has no Mii for them

		return false, nil
	}

	return true, nil
}

// BackfillProfiles creates the profile objects (Mii, name) of the creators of the archive levels already in the catalog,
// one API call each at the normal request rate, so the game finds them at once instead of waiting for a lookup while a
// list loads. It returns when every creator has been tried (or ctx ends). New levels bring their creator along.
func (p *Pool) BackfillProfiles(ctx context.Context) {
	rows, err := p.DB.Query(`SELECT MIN(s.data_id) FROM datastore.level_sources s JOIN datastore.objects o ON o.data_id = s.data_id
		WHERE o.data_type > 2 AND o.data_type < 50 AND NOT EXISTS (
			SELECT 1 FROM datastore.objects p WHERE p.owner = o.owner AND p.persistence_slot_id = 0 AND p.data_type = 1)
		GROUP BY o.owner`)
	if err != nil {
		p.logf("profiles: %v", err)

		return
	}

	var ids []int64

	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}

	rows.Close()

	if len(ids) == 0 {
		return
	}

	p.logf("profiles: fetching the Mii and name of %d level creators in the background", len(ids))

	failed := 0

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}

		if _, err := p.lookup(ctx, id, true); err != nil {
			failed++
		}
	}

	p.logf("profiles: done (%d lookups failed)", failed)
}

func (p *Pool) profileMissed(pid int64) bool {
	p.missMu.Lock()
	defer p.missMu.Unlock()

	t, ok := p.profileMisses[pid]

	ttl := p.MissTTL
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	return ok && time.Since(t) < ttl
}

func (p *Pool) missProfile(pid int64) {
	p.missMu.Lock()
	defer p.missMu.Unlock()

	if p.profileMisses == nil {
		p.profileMisses = map[int64]time.Time{}
	}

	p.profileMisses[pid] = time.Now()
}
