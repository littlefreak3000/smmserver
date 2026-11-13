package pool_test

import (
	"context"
	"testing"
	"time"
)

func TestBoostAddsLevelsOnlyToTheBandsOfTheRange(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)

	if _, err := p.Fill(context.Background()); err != nil {
		t.Fatal(err)
	}

	before, _ := p.Counts()

	// * 60-100% clear rate is the top band only
	p.Boost(context.Background(), 60, 100)

	deadline := time.Now().Add(5 * time.Second)

	for {
		counts, _ := p.Counts()
		if counts[3] >= before[3]+12 {
			for i := 0; i < 3; i++ {
				if counts[i] != before[i] {
					t.Fatalf("band %d changed: %v -> %v", i, before, counts)
				}
			}

			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the top band did not grow by 12: %v -> %v", before, counts)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestBoostOutsideEveryBandDoesNothing(t *testing.T) {
	f := newFake(t)
	p, _ := setup(t, f)

	p.Boost(context.Background(), 200, 300)
	time.Sleep(50 * time.Millisecond)

	if f.randomCalls != 0 {
		t.Fatalf("%d upstream draws for a range no band covers", f.randomCalls)
	}
}
