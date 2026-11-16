package pool_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/difficulty"
	"github.com/PretendoNetwork/super-mario-maker/pool"
)

func usePrior(t *testing.T, p difficulty.Prior) {
	t.Helper()

	old := difficulty.Current
	difficulty.Current = p

	t.Cleanup(func() { difficulty.Current = old })
}

func TestBandsFollowTheDifficultyScore(t *testing.T) {
	p, db, _ := evictEnv(t, 0)

	// * a level with 3 attempts and no clear: raw 0 % (band 0), with the default prior 10 % (band 1); a level with 400
	// * attempts and 2 clears stays at 0.5 % either way
	for id, c := range map[int64][2]float64{1: {0, 3}, 2: {0.005, 400}} {
		exec(t, db, `INSERT INTO datastore.level_sources (data_id, url, clear_rate, attempts, stars, fetched) VALUES ($1, 'u', $2, $3, 0, CURRENT_TIMESTAMP)`, id, c[0], c[1])
	}

	usePrior(t, difficulty.Prior{})

	raw, err := p.Counts()
	if err != nil {
		t.Fatal(err)
	}

	if raw[0] != 2 || raw[1] != 0 {
		t.Fatalf("raw bands %v, want both levels in band 0", raw)
	}

	usePrior(t, difficulty.Default)

	scored, err := p.Counts()
	if err != nil {
		t.Fatal(err)
	}

	if scored[0] != 1 || scored[1] != 1 {
		t.Fatalf("bands with the default prior %v, want one level in band 0 and the thin one in band 1", scored)
	}
}

func TestACacheKeepsTheDifficultyPriorItWasBuiltWith(t *testing.T) {
	p, _, courses := evictEnv(t, 0)
	dir := filepath.Dir(courses)

	m := &pool.CacheManifest{Format: 1, Seed: "x", Wanted: 4, Locked: true, PlaySeed: "x", Prior: difficulty.Default.String()}
	if err := m.Write(dir); err != nil {
		t.Fatal(err)
	}

	// * building (or extending) it under another prior is refused before anything is fetched
	usePrior(t, difficulty.Prior{})

	if _, err := p.BuildCache(context.Background(), pool.BuildOptions{Seed: "x", Levels: 4, Dir: dir}); err == nil {
		t.Fatal("a cache built with one difficulty prior was extended under another")
	}

	// * a manifest from before the prior was recorded means raw clear rates
	old := &pool.CacheManifest{}

	got, err := pool.ManifestPrior(old)
	if err != nil || got != (difficulty.Prior{}) {
		t.Fatalf("a manifest without a prior reads as %v, %v; want off", got, err)
	}

	if got, err := pool.ManifestPrior(m); err != nil || got != difficulty.Default {
		t.Fatalf("a recorded prior reads as %v, %v", got, err)
	}
}
