package index

import (
	"fmt"
	"testing"
)

// A seed is a promise between servers: "friday race" must give the same draws on every machine and in every future
// release. If this test fails, the algorithm changed. Do not edit the numbers: bump AlgorithmVersion instead (which
// intentionally changes every challenge code) and record the new vector next to the old one.
func TestSeededDrawsAreAFixedContract(t *testing.T) {
	if AlgorithmVersion != 1 {
		t.Skip("the vectors below belong to algorithm version 1")
	}

	src := NewSeeded("Friday  Race", "random|300000000|1|49")

	var got []int
	for i := 0; i < 8; i++ {
		got = append(got, src.IntN(1_000_000))
	}

	t.Logf("draws: %v", got)

	if want := fmt.Sprint(vectorV1); fmt.Sprint(got) != want {
		t.Fatalf("seeded draws changed:\n got  %v\n want %v", got, vectorV1)
	}

	// * spelling does not matter, the context does
	again := NewSeeded("friday race", "random|300000000|1|49")
	if again.IntN(1_000_000) != vectorV1[0] {
		t.Fatal("normalized seeds must give the same stream")
	}

	other := NewSeeded("friday race", "random|300000000|50|100")
	if other.IntN(1_000_000) == vectorV1[0] && other.IntN(1_000_000) == vectorV1[1] {
		t.Fatal("a different list must give a different stream")
	}
}

// vectorV1 was computed by this package and independently re-computed with a separate implementation (Node, BigInt:
// SHA-256 of "smm-seed-v1\0friday race\0random|300000000|1|49", first 8 bytes little-endian as the SplitMix64 state).
var vectorV1 = []int{486204, 654896, 166393, 153521, 873882, 40648, 343240, 240452}
