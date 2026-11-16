package index

import (
	"path/filepath"
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/difficulty"
)

// Level ids by kind, so a list can be read back as "how many of each kind".
const (
	thickEasy   = 10_000 // 200 levels at 12 % with 500 attempts
	thickMiddle = 20_000 // 200 levels at 30 % with 500 attempts
	thinEasy    = 30_000 // 60 levels at about 12 % but with only 10 attempts (1 clear)
)

func softIndex(t *testing.T, prior difficulty.Prior) *Index {
	t.Helper()

	old := difficulty.Current
	difficulty.Current = prior

	t.Cleanup(func() { difficulty.Current = old })

	b, err := Create(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	add := func(base int64, n int, clears, attempts int64) {
		for i := 0; i < n; i++ {
			r := testRecord(base + int64(i))
			r.Clears, r.Attempts = clears, attempts

			if err := b.Add(r); err != nil {
				t.Fatal(err)
			}
		}
	}

	add(thickEasy, 200, 60, 500)
	add(thickMiddle, 200, 150, 500)
	add(thinEasy, 60, 1, 10)

	if _, err := b.Finish(); err != nil {
		t.Fatal(err)
	}

	x, err := Open(b.path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { x.Close() })

	return x
}

func kind(id int64) string {
	switch {
	case id >= thinEasy:
		return "thin"
	case id >= thickMiddle:
		return "middle"
	}

	return "easy"
}

// tally samples a range many times and counts how often each kind of level is offered.
func tally(t *testing.T, x *Index, lo, hi, rounds, n int) map[string]int {
	t.Helper()

	out := map[string]int{}

	for r := 0; r < rounds; r++ {
		levels, err := x.Sample(NewSeeded("round", string(rune('a'+r%26))+string(rune('a'+r/26))), lo, hi, n, nil)
		if err != nil {
			t.Fatal(err)
		}

		for _, l := range levels {
			out[kind(l.ID)]++
		}
	}

	return out
}

func TestALevelWithFewPlaysIsPlacedInAWiderRange(t *testing.T) {
	soft := softIndex(t, difficulty.Default)
	hard := softIndex(t, difficulty.Prior{})

	if soft.Prior() != difficulty.Default.String() || hard.Prior() != "off" {
		t.Fatalf("priors %q %q", soft.Prior(), hard.Prior())
	}

	// * a range the thick 12 % levels are nowhere near (20-49): without a prior the thin levels (score 12 %) are not there
	// * either; with one they are, now and then, because 1 clear in 10 attempts could be a much easier level
	h := tally(t, hard, 20, 49, 40, 20)
	s := tally(t, soft, 20, 49, 40, 20)

	if h["thin"] != 0 || h["easy"] != 0 || h["middle"] == 0 {
		t.Fatalf("hard edges, range 20-49: %v", h)
	}

	if s["thin"] == 0 {
		t.Fatalf("soft edges, range 20-49: the thin levels never appear: %v", s)
	}

	if s["easy"] != 0 {
		t.Fatalf("soft edges, range 20-49: well-played 12 %% levels appeared %d times: %v", s["easy"], s)
	}

	if s["thin"] > s["middle"]/2 {
		t.Fatalf("soft edges, range 20-49: thin levels are not a minority: %v", s)
	}

	// * and in their own range (5-19) they are less sure of themselves than the well-played levels are
	own := tally(t, soft, 5, 19, 40, 20)
	if own["easy"] == 0 || own["thin"] == 0 {
		t.Fatalf("range 5-19: %v", own)
	}

	// * per level offered: the thin levels are 60 of the 260 levels around 12 %, but fewer than that share of the list
	if share, fair := float64(own["thin"])/float64(own["thin"]+own["easy"]), 60.0/260; share >= fair {
		t.Fatalf("thin levels take %.2f of the 5-19 list, no less than their %.2f share of the levels", share, fair)
	}
}

func TestSoftSamplesAreRepeatableAndPrefixConsistent(t *testing.T) {
	x := softIndex(t, difficulty.Default)

	a, err := x.Sample(NewSeeded("contest", "random|300000000|20|49"), 20, 49, 100, nil)
	if err != nil {
		t.Fatal(err)
	}

	b, _ := x.Sample(NewSeeded("Contest", "random|300000000|20|49"), 20, 49, 100, nil)
	short, _ := x.Sample(NewSeeded("contest", "random|300000000|20|49"), 20, 49, 32, nil)

	if len(a) != 100 || len(b) != 100 || len(short) != 32 {
		t.Fatalf("lengths %d %d %d", len(a), len(b), len(short))
	}

	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("the same seed gave different lists at %d", i)
		}
	}

	for i := range short {
		if short[i].ID != a[i].ID {
			t.Fatalf("a shorter request does not start the longer one (position %d)", i)
		}
	}

	seen := map[int64]bool{}

	for _, l := range a {
		if seen[l.ID] {
			t.Fatalf("level %d twice", l.ID)
		}

		seen[l.ID] = true
	}
}

func TestTheWholeScaleAndSmallRangesStillWork(t *testing.T) {
	x := softIndex(t, difficulty.Default)

	all, err := x.Sample(NewSeeded("s", "all"), 0, 100, 50, nil)
	if err != nil || len(all) != 50 {
		t.Fatalf("0..100: %d levels, %v", len(all), err)
	}

	// * a range with nothing near it is empty, not an endless search
	none, err := x.Sample(NewSeeded("s", "none"), 90, 100, 10, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, l := range none {
		t.Fatalf("level %d (%s) offered for 90-100", l.ID, kind(l.ID))
	}
}

func TestThePriorIsPartOfTheFingerprint(t *testing.T) {
	off := softIndex(t, difficulty.Prior{})
	on := softIndex(t, difficulty.Default)
	other := softIndex(t, difficulty.Prior{Mean: 0.2, Strength: 10})

	if off.Fingerprint() == on.Fingerprint() || on.Fingerprint() == other.Fingerprint() {
		t.Fatalf("fingerprints %s %s %s: the prior must change the code a challenge uses", off.Fingerprint(), on.Fingerprint(), other.Fingerprint())
	}

	// * with no prior the fingerprint is the one an index always had
	counts := off.Counts()

	var sum int64

	for _, l := range []struct{ base, n int64 }{{thickEasy, 200}, {thickMiddle, 200}, {thinEasy, 60}} {
		for i := int64(0); i < l.n; i++ {
			sum += l.base + i
		}
	}

	if got := fingerprint(off.Total(), sum, counts, "off"); got != off.Fingerprint() || fingerprint(off.Total(), sum, counts, "") != got {
		t.Fatalf("an index without a prior no longer has its old fingerprint (%s, stored %s)", got, off.Fingerprint())
	}
}
