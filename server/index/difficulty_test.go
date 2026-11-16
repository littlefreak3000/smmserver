package index

import (
	"testing"

	"github.com/PretendoNetwork/super-mario-maker/difficulty"
)

func TestBucketsAreDifficultyScoresAndTheIndexRemembersItsPrior(t *testing.T) {
	old := difficulty.Current

	t.Cleanup(func() { difficulty.Current = old })

	// * raw: the clear rate in percent, as before
	difficulty.Current = difficulty.Prior{}

	if bucketOf(3, 3) != 100 || bucketOf(0, 5) != 0 || bucketOf(1, 4) != 25 || bucketOf(0, 0) != 0 {
		t.Fatalf("raw buckets %d %d %d %d", bucketOf(3, 3), bucketOf(0, 5), bucketOf(1, 4), bucketOf(0, 0))
	}

	x, _, _ := buildIndex(t, 20, 1)
	defer x.Close()

	if x.Prior() != "off" {
		t.Fatalf("an index built with raw rates says its prior is %q", x.Prior())
	}

	// * with the default prior a thin level moves towards the typical level, a well-played one does not
	difficulty.Current = difficulty.Default

	if bucketOf(3, 3) != 60 || bucketOf(0, 5) != 8 || bucketOf(80, 400) != 20 {
		t.Fatalf("scored buckets %d %d %d", bucketOf(3, 3), bucketOf(0, 5), bucketOf(80, 400))
	}

	y, _, _ := buildIndex(t, 20, 1)
	defer y.Close()

	if y.Prior() != difficulty.Default.String() {
		t.Fatalf("the index's prior is %q, want %q", y.Prior(), difficulty.Default.String())
	}
}
