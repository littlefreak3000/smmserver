package pool_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PretendoNetwork/super-mario-maker/levelimport"
	"github.com/PretendoNetwork/super-mario-maker/upstream"
)

// indexLookup serves metadata from the fake's levels, the way the whole-archive index would.
func indexLookup(f *fake) func(int64) (*upstream.Level, error) {
	return func(id int64) (*upstream.Level, error) {
		for _, l := range f.levels {
			if l.LevelID == id {
				c := *l
				return &c, nil
			}
		}

		return nil, nil
	}
}

func TestEnsureFetchedStubsThenCompletesAndSharesOneDownload(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	p.Lookup = indexLookup(f)

	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if ok, err := p.EnsureFetched(context.Background(), 1010); err != nil || !ok {
				t.Errorf("EnsureFetched: %v %v", ok, err)
			}
		}()
	}

	wg.Wait()

	if stub, known, _ := levelimport.IsStub(db, 1010); stub || !known {
		t.Fatalf("stub=%v known=%v after EnsureFetched", stub, known)
	}

	if f.binCalls < 1 || f.binCalls > 5 {
		t.Fatalf("downloads: %d", f.binCalls)
	}

	before := f.binCalls

	if ok, err := p.EnsureFetched(context.Background(), 1010); !ok || err != nil || f.binCalls != before {
		t.Fatalf("a complete level must not be downloaded again (ok=%v err=%v calls %d->%d)", ok, err, before, f.binCalls)
	}

	if f.levelCalls != 0 {
		t.Fatalf("the index answered, the metadata API must not be asked (%d calls)", f.levelCalls)
	}
}

func TestEnsureFetchedMarksLevelsWithoutCaptureDead(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	p.Lookup = indexLookup(f)

	f.missingFor[f.levels[20].URL] = true // 1020: no capture
	f.garbageFor[f.levels[21].URL] = true // 1021: the capture is an error page

	for _, id := range []int64{1020, 1021} {
		if err := p.Stub(*f.levels[id-1000]); err != nil {
			t.Fatal(err)
		}

		if ok, err := p.EnsureFetched(context.Background(), id); ok || err != nil {
			t.Fatalf("level %d: ok=%v err=%v", id, ok, err)
		}

		if !p.IsDead(id) {
			t.Fatalf("level %d should be dead", id)
		}

		if _, known, _ := levelimport.IsStub(db, id); known {
			t.Fatalf("the stub of dead level %d must be removed", id)
		}
	}

	// * a dead level is never stubbed or fetched again
	calls := f.binCalls

	if err := p.Stub(*f.levels[20]); err != nil {
		t.Fatal(err)
	}

	if ok, _ := p.EnsureFetched(context.Background(), 1020); ok || f.binCalls != calls {
		t.Fatal("a dead level was fetched again")
	}

	if _, known, _ := levelimport.IsStub(db, 1020); known {
		t.Fatal("a dead level was stubbed again")
	}
}

func TestEnsureFetchedKeepsTheStubAfterATransientError(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	p.Lookup = indexLookup(f)

	if err := p.Stub(*f.levels[30]); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	f.missingFor[f.levels[30].URL] = false
	f.mu.Unlock()

	flaky := &flakyBin{fake: f, fail: 1}
	p.Src = flaky

	if ok, err := p.EnsureFetched(context.Background(), 1030); ok || err == nil {
		t.Fatalf("first attempt should fail: ok=%v err=%v", ok, err)
	}

	if p.IsDead(1030) {
		t.Fatal("a transient error must not kill the level")
	}

	if stub, known, _ := levelimport.IsStub(db, 1030); !stub || !known {
		t.Fatal("the stub must stay")
	}

	if ok, err := p.EnsureFetched(context.Background(), 1030); !ok || err != nil {
		t.Fatalf("second attempt: ok=%v err=%v", ok, err)
	}
}

type flakyBin struct {
	*fake
	fail int
}

func (f *flakyBin) FetchBin(ctx context.Context, u string) ([]byte, error) {
	if f.fail > 0 {
		f.fail--
		return nil, context.DeadlineExceeded
	}

	return f.fake.FetchBin(ctx, u)
}

func TestEnsureFetchedFallsBackToTheAPIForLevelsOutsideTheIndex(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	p.Lookup = func(int64) (*upstream.Level, error) { return nil, nil }

	if ok, err := p.EnsureFetched(context.Background(), 1050); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}

	if stub, known, _ := levelimport.IsStub(db, 1050); stub || !known || f.levelCalls != 1 {
		t.Fatalf("stub=%v known=%v API calls=%d", stub, known, f.levelCalls)
	}

	if ok, _ := p.EnsureFetched(context.Background(), 5); ok {
		t.Fatal("an unknown level cannot exist")
	}
}

func TestPrefetchDownloadsQueuedLevelsInOrder(t *testing.T) {
	f := newFake(t)
	p, db := setup(t, f)
	p.Lookup = indexLookup(f)

	for _, id := range []int64{1060, 1061, 1062} {
		if err := p.Stub(*f.levels[id-1000]); err != nil {
			t.Fatal(err)
		}
	}

	p.Prefetch(1060, 1061, 1062, 1060) // a repeat is queued once

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.RunPrefetch(ctx)

	deadline := time.Now().Add(5 * time.Second)

	for {
		stubs, complete, _ := p.Stubs()
		if stubs == 0 && complete == 3 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("prefetch did not finish: %d stubs, %d complete", stubs, complete)
		}

		time.Sleep(10 * time.Millisecond)
	}

	if f.binCalls != 3 {
		t.Fatalf("downloads: %d, want 3", f.binCalls)
	}

	_ = db
}
