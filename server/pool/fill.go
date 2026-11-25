package pool

// The fill: two workers that work at the same time.
//
// The archive offers one random level per call, and a level's file comes from a different service (the Wayback Machine). The
// two are paced separately, so there is no reason to wait for one before using the other. The drawer asks for random levels,
// works out which band each one belongs in and keeps its details (a few hundred bytes) in a queue for that band; the downloader
// takes the band that is furthest behind and fetches the file of one of its queued levels. So:
//
//   - the downloader never waits for a rare band: while it fetches a file, the drawer has already found more levels, and a
//     level for a thin band is taken before one for a band that has plenty;
//   - nothing is downloaded that is not wanted: the need of a band is re-checked at the moment a level is taken from its queue.
//
// Each service is still called at its own polite pace (the limiters live in the upstream client).

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// lookahead is how many levels beyond its need a band keeps queued, so the downloader always has one ready.
const lookahead = 10

type filler struct {
	p       *Pool
	targets []int
	maxDraw int

	mu       sync.Mutex
	cond     *sync.Cond
	counts   []int
	inflight []int
	queue    [][]*upstream.Level
	seen     map[int64]bool // levels queued, being fetched or given up on in this fill
	st       Stats

	done       bool  // every band has what it needs
	drawerDone bool  // the drawer has stopped
	drawErr    error // why it stopped, if not because everything was found
}

// need is how many more files band b still wants (not counting the ones being fetched).
func (f *filler) need(b int) int { return f.targets[b] - f.counts[b] - f.inflight[b] }

// room is how many more candidates band b can take into its queue.
func (f *filler) room(b int) int {
	if f.need(b) <= 0 {
		return 0
	}

	return f.need(b) + lookahead - len(f.queue[b])
}

func (f *filler) satisfied() bool {
	for b := range f.counts {
		if f.counts[b] < f.targets[b] {
			return false
		}
	}

	return true
}

func (f *filler) anyRoom() bool {
	for b := range f.queue {
		if f.room(b) > 0 {
			return true
		}
	}

	return false
}

// thinnest picks the band to fetch next: of the bands that still want files and have a level queued, the one that holds the
// smallest share of its target.
func (f *filler) thinnest() int {
	best, bestShare := -1, 0.0

	for b := range f.queue {
		if f.need(b) <= 0 || len(f.queue[b]) == 0 {
			continue
		}

		share := float64(f.counts[b]+f.inflight[b]) / float64(max(f.targets[b], 1))
		if best < 0 || share < bestShare {
			best, bestShare = b, share
		}
	}

	return best
}

// fillTo imports random levels until band i holds targets[i] of them (a band with target 0 is left alone), giving up
// after maxDraws API draws (0 = 40 draws per wanted level).
func (p *Pool) fillTo(ctx context.Context, targets []int, maxDraws int) (Stats, error) {
	counts, err := p.Counts()
	if err != nil {
		return Stats{}, err
	}

	if maxDraws <= 0 {
		for i, c := range counts {
			maxDraws += 40 * max(targets[i]-c, 0)
		}
	}

	f := &filler{
		p: p, targets: targets, maxDraw: maxDraws,
		counts: counts, inflight: make([]int, len(counts)), queue: make([][]*upstream.Level, len(counts)),
		seen: map[int64]bool{},
	}
	f.cond = sync.NewCond(&f.mu)

	// * a cancelled context must wake a worker that is waiting on the condition
	stop := context.AfterFunc(ctx, func() {
		f.mu.Lock()
		f.cond.Broadcast()
		f.mu.Unlock()
	})
	defer stop()

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		f.draw(ctx)
	}()

	f.download(ctx)

	// * the downloader is done (everything found, or nothing more can arrive): stop the drawer too
	f.mu.Lock()
	f.done = true
	f.cond.Broadcast()
	f.mu.Unlock()

	wg.Wait()

	f.mu.Lock()
	defer f.mu.Unlock()

	f.st.PerBand = append([]int(nil), f.counts...)

	switch {
	case ctx.Err() != nil:
		return f.st, ctx.Err()
	case f.satisfied():
		return f.st, nil
	case f.drawErr != nil:
		return f.st, f.drawErr
	}

	return f.st, ErrMaxDraws
}

// draw asks the archive for random levels and queues the ones a band can still use.
func (f *filler) draw(ctx context.Context) {
	p := f.p

	backoff := p.ErrorBackoff
	if backoff == 0 {
		backoff = 30 * time.Second
	}

	consecutive, cycles := 0, 0

	finish := func(err error) {
		f.mu.Lock()
		f.drawerDone = true

		if err != nil && f.drawErr == nil {
			f.drawErr = err
		}

		f.cond.Broadcast()
		f.mu.Unlock()
	}

	for {
		f.mu.Lock()

		for !f.done && ctx.Err() == nil && !f.anyRoom() {
			f.cond.Wait() // every queue is full: wait for the downloader to take some
		}

		if f.done || ctx.Err() != nil {
			f.mu.Unlock()
			finish(nil)

			return
		}

		if f.st.Draws >= f.maxDraw {
			f.mu.Unlock()
			finish(ErrMaxDraws)

			return
		}

		f.st.Draws++
		f.mu.Unlock()

		lvl, err := p.Src.RandomLevel(ctx, p.randIndex())
		if err != nil {
			if ctx.Err() != nil {
				finish(nil)

				return
			}

			f.mu.Lock()
			f.st.APIErrors++
			f.mu.Unlock()

			consecutive++

			if consecutive >= 6 {
				cycles++
				if cycles > 3 {
					finish(fmt.Errorf("pool: upstream keeps failing: %w", err))

					return
				}

				p.logf("pool: %d upstream errors in a row (%v); pausing %s", consecutive, err, backoff)

				if err := sleep(ctx, backoff); err != nil {
					finish(nil)

					return
				}

				consecutive = 0
			}

			continue
		}

		consecutive = 0

		f.keep(lvl)
	}
}

// keep sorts one drawn level into its band's queue, or counts why it is not wanted.
func (f *filler) keep(lvl *upstream.Level) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case lvl == nil:
		f.st.Empty++

		return
	case lvl.TotalAttempts <= 0:
		f.st.NoAttempts++ // no win rate to sort it by

		return
	}

	b := f.p.bandOfLevel(lvl)
	if b < 0 {
		return
	}

	if f.room(b) <= 0 {
		f.st.BandFull++

		return
	}

	if f.seen[lvl.LevelID] {
		f.st.Duplicates++

		return
	}

	// * known() asks the database: do it without holding the lock for the others
	f.mu.Unlock()
	known := f.p.known(lvl.LevelID)
	f.mu.Lock()

	if known || f.seen[lvl.LevelID] || f.room(b) <= 0 {
		f.st.Duplicates++

		return
	}

	f.seen[lvl.LevelID] = true
	f.queue[b] = append(f.queue[b], lvl)
	f.cond.Broadcast()
}

// download fetches and imports the files of queued levels, thinnest band first, until every band has what it needs or nothing more
// can arrive.
func (f *filler) download(ctx context.Context) {
	p := f.p

	for {
		f.mu.Lock()

		var b int

		for {
			if f.satisfied() || ctx.Err() != nil {
				f.mu.Unlock()

				return
			}

			if b = f.thinnest(); b >= 0 {
				break
			}

			if f.drawerDone {
				f.mu.Unlock() // nothing queued, nothing coming

				return
			}

			f.cond.Wait()
		}

		lvl := f.queue[b][0]
		f.queue[b] = f.queue[b][1:]
		f.inflight[b]++
		f.cond.Broadcast() // the queue has room again
		f.mu.Unlock()

		bin, err := p.Src.FetchBin(ctx, lvl.URL)

		var impErr error

		switch {
		case err != nil:
			if ctx.Err() != nil {
				f.mu.Lock()
				f.inflight[b]--
				f.mu.Unlock()

				return
			}

			p.logf("pool: level %d: no usable capture (%v)", lvl.LevelID, err)
		default:
			impErr = levelimport.Import(p.DB, *lvl, bin, p.Import)
			if impErr != nil {
				p.logf("pool: level %d skipped: %v", lvl.LevelID, impErr)
			}
		}

		f.mu.Lock()
		f.inflight[b]--

		switch {
		case err != nil:
			f.st.FetchFailed++
		case impErr != nil:
			f.st.Invalid++
		default:
			f.counts[b]++
			f.st.Imported++

			if f.st.Imported%10 == 0 {
				p.logf("pool: %d imported so far; bands %v of %v (%d draws)", f.st.Imported, f.counts, f.targets, f.st.Draws)
			}
		}

		f.cond.Broadcast()
		f.mu.Unlock()
	}
}

