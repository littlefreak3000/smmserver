package nex_datastore_super_mario_maker

import (
	"testing"

	"github.com/PretendoNetwork/nex-go/v2/types"
)

func extra(vals ...string) types.List[types.String] {
	var l types.List[types.String]
	for _, v := range vals {
		l = append(l, types.NewString(v))
	}

	return l
}

func TestNormalDoesNotReachDownIntoEasy(t *testing.T) {
	old := NarrowNormal

	t.Cleanup(func() { NarrowNormal = old })

	cases := []struct {
		name     string
		extra    types.List[types.String]
		narrow   bool
		min, max int64
	}{
		{"Easy", extra("1", "0", "34", "", "0"), true, 66, 100},
		{"Normal, narrowed", extra("1", "0", "74", "", "0"), true, 26, 65},
		{"Normal, as the game asks", extra("1", "0", "74", "", "0"), false, 26, 100},
		{"Course World Normal filter", extra("1", "35", "74", "0", "0"), true, 26, 65},
		{"Expert", extra("1", "75", "95", "", "0"), true, 5, 25},
		{"Super Expert", extra("1", "96", "100", "", "0"), true, 0, 4},
		{"no filter", extra("0", "", "", "", "0"), true, 0, 100},
		{"not a number", extra("1", "x", "74", "", "0"), true, 0, 100},
	}

	for _, c := range cases {
		NarrowNormal = c.narrow

		if lo, hi := recommendedRange(c.extra); lo != c.min || hi != c.max {
			t.Errorf("%s: got %d-%d, want %d-%d", c.name, lo, hi, c.min, c.max)
		}
	}
}

func TestTheButtonsFollowTheRanges(t *testing.T) {
	old := ChallengeRanges

	t.Cleanup(func() { ChallengeRanges = old })

	// * pool bands 0-10, 10-30, 30-60, 60-100: Super Expert, Expert, Normal, Easy
	ChallengeRanges = &[4][2]int64{{0, 9}, {10, 29}, {30, 59}, {60, 100}}

	for _, c := range []struct {
		name     string
		extra    types.List[types.String]
		min, max int64
	}{
		{"Super Expert", extra("1", "96", "100", "", "0"), 0, 9},
		{"Expert", extra("1", "75", "95", "", "0"), 10, 29},
		{"Normal in the 100 Mario Challenge", extra("1", "0", "74", "", "0"), 30, 59},
		{"Normal in Course World", extra("1", "35", "74", "0", "0"), 30, 59},
		{"Easy", extra("1", "0", "34", "", "0"), 60, 100},
		{"a range that is no button", extra("1", "10", "20", "", "0"), 80, 90},
		{"no filter", extra("0", "", "", "", "0"), 0, 100},
	} {
		if lo, hi := recommendedRange(c.extra); lo != c.min || hi != c.max {
			t.Errorf("%s: got %d-%d, want %d-%d", c.name, lo, hi, c.min, c.max)
		}
	}
}
