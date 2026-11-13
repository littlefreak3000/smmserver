package pool

import "testing"

func TestDrawIndexIsRepeatableAndDependsOnTheSeed(t *testing.T) {
	const space = 18118278

	for k := 0; k < 50; k++ {
		a, b := drawIndex("Friday Race", k, space), drawIndex("  friday   race ", k, space)
		if a != b || a < 0 || a >= space {
			t.Fatalf("draw %d: %d vs %d", k, a, b)
		}
	}

	same := 0

	for k := 0; k < 50; k++ {
		if drawIndex("seed one", k, space) == drawIndex("seed two", k, space) {
			same++
		}
	}

	if same > 1 {
		t.Fatalf("two seeds share %d of 50 draws", same)
	}

	// * pinned: a change here would change every seeded cache that already exists
	if got := drawIndex("pinned", 0, space); got != drawIndex("PINNED", 0, space) || got != 6563372 {
		t.Fatalf("draw 0 of seed \"pinned\" = %d", got)
	}
}
